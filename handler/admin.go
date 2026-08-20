package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"key-router/billing"
	"key-router/db"
	"key-router/events"
	"key-router/health"
	"key-router/middleware"
	"key-router/model"
	"key-router/selector"
	"key-router/update"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// version is injected at build time via -ldflags "-X main.version=..." Ã¢â‚¬â€ the
// handler package mirrors the value so /api/health reports the release version.
var version = "0.1.0"

// Updater abstracts the update client for the update endpoints: a real
// *update.Client in production, fakes in tests.
type Updater interface {
	CurrentVersion() string
	InstallMode() string
	Check() (*update.UpdateInfo, error)
	Apply(*update.UpdateInfo) error
}

// AdminHandler handles management API endpoints
type AdminHandler struct {
	Engine        *selector.Engine
	HealthChecker *health.Checker
	Updater       Updater
	// Events is the SSE push hub: background state changes (key status
	// flips from the relay/health checker) are published here so connected
	// UIs re-fetch immediately instead of waiting for their next poll.
	Events *events.Hub
	// AutostartEnabled reports whether launch-at-login is on (nil = unsupported).
	AutostartEnabled func() bool
	// AutostartSet enables/disables launch-at-login (nil = unsupported).
	AutostartSet func(enabled bool) error
	// ExitAfterUpdate is set by main (via router.SetUpdateExitHook). Called
	// by ApplyUpdate AFTER the response is written: the process must exit so
	// the new binary / installer can replace it (portable swap script,
	// installed installer). Nil = no exit (used in tests).
	ExitAfterUpdate func()
	// RestartSchedule is set by main (via router.SetRestartHook). Called by
	// Restart BEFORE the response is written: it schedules a fresh instance
	// (wait-for-exit helper). An error means no restart will happen — the
	// request fails with 500 and a later attempt can succeed. Nil = no
	// restart (used in tests).
	RestartSchedule func() error
	// RestartQuit is set by main (via router.SetRestartHook). Called by
	// Restart AFTER the response has been written and flushed: it triggers
	// the graceful shutdown — new requests are rejected while in-flight API
	// calls drain, then the process exits so the scheduled fresh instance
	// can take over. Nil = no restart (used in tests).
	RestartQuit func()
	// restartMu/restarting guard Restart against concurrent calls: only the
	// first request may schedule the relaunch — two fresh instances would
	// fight over the server port.
	restartMu  sync.Mutex
	restarting bool
	// autoCheckInfo holds the most recent auto-check result (set by
	// AutoCheck's callback; read by GetAutoCheckState).
	autoCheckMu   sync.Mutex
	autoCheckInfo *update.UpdateInfo
}

// SetAutoCheckInfo stores the latest auto-check result.
func (h *AdminHandler) SetAutoCheckInfo(info *update.UpdateInfo) {
	h.autoCheckMu.Lock()
	h.autoCheckInfo = info
	h.autoCheckMu.Unlock()
}

// lastCheckInfo returns the most recent server-side check result (auto-check
// or manual CheckUpdate), or nil before the first check.
func (h *AdminHandler) lastCheckInfo() *update.UpdateInfo {
	h.autoCheckMu.Lock()
	defer h.autoCheckMu.Unlock()
	return h.autoCheckInfo
}

// GetAutoCheckState returns the last auto-check result (checked=false when no
// auto-check has found an update yet) plus the always-known local facts —
// current version and install mode — so the UI can label the copy (portable
// vs installed) correctly before the first check.
func (h *AdminHandler) GetAutoCheckState(c *gin.Context) {
	h.autoCheckMu.Lock()
	info := h.autoCheckInfo
	h.autoCheckMu.Unlock()

	resp := gin.H{
		"current_version":  h.Updater.CurrentVersion(),
		"latest_version":   h.Updater.CurrentVersion(),
		"update_available": false,
		"install_mode":     h.Updater.InstallMode(),
		"checked":          info != nil,
	}
	if info != nil {
		resp["latest_version"] = info.LatestVersion
		resp["update_available"] = info.UpdateAvailable
		resp["checked_at"] = info.CheckedAt
		if info.AssetName != "" {
			resp["asset_name"] = info.AssetName
			resp["asset_url"] = info.AssetURL
			resp["asset_size"] = info.AssetSize
		}
	}
	c.JSON(http.StatusOK, resp)
}

// NewAdminHandler creates a new admin handler
func NewAdminHandler(engine *selector.Engine, checker *health.Checker, hub *events.Hub) *AdminHandler {
	return &AdminHandler{
		Engine:        engine,
		HealthChecker: checker,
		Updater:       update.NewClient(version),
		Events:        hub,
	}
}

// StreamEvents serves the SSE push channel (GET /api/events). The UI keeps
// one connection open and re-fetches the affected resource when an event
// arrives — the "hot reload" contract: update in place when something
// changed, nothing when it didn't. Heartbeat comments keep the connection
// alive through proxies and make a dead client detectable via request
// context cancellation.
func (h *AdminHandler) StreamEvents(c *gin.Context) {
	ch := h.Events.Subscribe()
	defer h.Events.Unsubscribe(ch)

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-middleware.ShutdownSignal():
			// App is quitting: terminate the stream so http.Server.Shutdown
			// doesn't wait the full grace period for an immortal connection.
			return
		case e := <-ch:
			data, err := json.Marshal(e)
			if err != nil {
				continue
			}
			fmt.Fprintf(c.Writer, "data: %s\n\n", data)
			c.Writer.Flush()
		case <-heartbeat.C:
			fmt.Fprint(c.Writer, ": ping\n\n")
			c.Writer.Flush()
		}
	}
}

// Health returns service status
func (h *AdminHandler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"version": version,
		"time":    time.Now().Unix(),
	})
}

// GetProviders returns all providers
func (h *AdminHandler) GetProviders(c *gin.Context) {
	var providers []model.Provider
	if err := db.GetDB().Find(&providers).Error; err != nil {
		log.Printf("[admin] GetProviders error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load providers"})
		return
	}
	c.JSON(http.StatusOK, providers)
}

// CreateProvider creates a new provider
func (h *AdminHandler) CreateProvider(c *gin.Context) {
	var p model.Provider
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	p.ID = 0 // client-supplied ids must not force rowids
	if err := validateProvider(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := db.GetDB().Create(&p).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.Engine.Refresh()
	c.JSON(http.StatusCreated, p)
}

// validateProvider checks required provider fields (base URL + known type)
func validateProvider(p *model.Provider) error {
	if p.Name == "" {
		return errors.New("name is required")
	}
	if p.BaseURL == "" {
		return errors.New("base_url is required")
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("base_url must be a valid URL (scheme + host)")
	}
	// Only HTTP(S) makes sense for an API gateway; a typo'd scheme (ftp://Ã¢â‚¬Â¦)
	// would otherwise surface as a confusing 502 at request time
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("base_url must use http or https")
	}
	if p.Type != model.ProviderTypeOpenAI && p.Type != model.ProviderTypeAnthropic {
		return errors.New("type must be \"openai\" or \"anthropic\"")
	}
	if p.ExtraHeaders != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(p.ExtraHeaders), &m); err != nil {
			return errors.New("extra_headers must be a JSON object")
		}
	}
	return nil
}

// UpdateProvider updates a provider
func (h *AdminHandler) UpdateProvider(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var p model.Provider
	if err := db.GetDB().First(&p, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
		return
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	p.ID = id
	if err := validateProvider(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := db.GetDB().Save(&p).Error; err != nil {
		log.Printf("[admin] UpdateProvider save error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.Engine.Refresh()
	c.JSON(http.StatusOK, p)
}

// DeleteProvider deletes a provider and its keys/routes (transactional)
func (h *AdminHandler) DeleteProvider(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)

	// Collect the provider's key IDs so their window state can be dropped too
	var keyIDs []int64
	if err := db.GetDB().Model(&model.Key{}).Where("provider_id = ?", id).Pluck("id", &keyIDs).Error; err != nil {
		log.Printf("[admin] DeleteProvider key-id query error (window state will linger): %v", err)
	}

	tx := db.GetDB().Begin()

	if err := tx.Delete(&model.Provider{}, id).Error; err != nil {
		tx.Rollback()
		log.Printf("[admin] DeleteProvider error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Where("provider_id = ?", id).Delete(&model.Key{}).Error; err != nil {
		tx.Rollback()
		log.Printf("[admin] DeleteProvider cascade key delete error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Where("provider_id = ?", id).Delete(&model.Route{}).Error; err != nil {
		tx.Rollback()
		log.Printf("[admin] DeleteProvider cascade route delete error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := tx.Commit().Error; err != nil {
		log.Printf("[admin] DeleteProvider commit error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Drop window state and state-machine streaks for the deleted keys so
	// memory/windows.json and the outcome tracker don't grow
	for _, kid := range keyIDs {
		h.Engine.WindowManager.Remove(kid)
		h.Engine.ResetOutcome(kid)
	}
	h.Engine.Refresh()
	c.JSON(http.StatusNoContent, nil)
}

// GetKeys returns all keys, optionally filtered by provider, ordered by
// (provider_id, sort_order, id). sort_order is per-provider and is the call
// order the UI arranges by dragging; without the explicit order the rows
// come back in rowid order and the UI reverts to it on every 10s poll.
func (h *AdminHandler) GetKeys(c *gin.Context) {
	var keys []model.Key
	query := db.GetDB().Preload("Provider").Order("provider_id ASC, sort_order ASC, id ASC")
	if providerID := c.Query("provider_id"); providerID != "" {
		query = query.Where("provider_id = ?", providerID)
	}
	if err := query.Find(&keys).Error; err != nil {
		log.Printf("[admin] GetKeys error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load keys"})
		return
	}

	// Add window counts and the currently-exhausted windows (why the
	// selector skips a key whose status is still "active" — e.g. its RPM
	// budget is spent, so traffic falls through to the next key).
	type KeyWithCounts struct {
		model.Key
		Counts map[string]struct {
			Count      int64 `json:"count"`
			TokenCount int64 `json:"token_count"`
		} `json:"counts"`
		LimitedWindows []string `json:"limited_windows,omitempty"`
	}
	result := make([]KeyWithCounts, 0, len(keys))
	for _, k := range keys {
		kwc := KeyWithCounts{Key: k}
		kwc.Counts = make(map[string]struct {
			Count      int64 `json:"count"`
			TokenCount int64 `json:"token_count"`
		})
		for _, wt := range []model.WindowType{
			model.WindowRPM, model.WindowTPM, model.WindowRP5h,
			model.WindowRPD, model.WindowRPW, model.WindowRPMo,
		} {
			kwc.Counts[string(wt)] = struct {
				Count      int64 `json:"count"`
				TokenCount int64 `json:"token_count"`
			}{
				Count:      h.Engine.WindowManager.GetCount(k.ID, wt),
				TokenCount: h.Engine.WindowManager.GetTokens(k.ID, wt),
			}
		}
		kwc.LimitedWindows = h.Engine.LimitedWindows(&k)
		result = append(result, kwc)
	}

	c.JSON(http.StatusOK, result)
}

// validateKeyStatus checks the status and recovery strategy values
func validateKeyStatus(k *model.Key) error {
	switch k.Status {
	case "", model.KeyStatusActive, model.KeyStatusRateLimited, model.KeyStatusDisabled:
	default:
		return errors.New("status must be one of: active, rate_limited, disabled")
	}
	switch k.RecoveryStrategy {
	case "", model.RecoveryImmediate, model.RecoveryLazy:
	default:
		return errors.New("recovery_strategy must be one of: immediate, lazy")
	}
	return nil
}

// CreateKey creates a new key
func (h *AdminHandler) CreateKey(c *gin.Context) {
	var k model.Key
	if err := c.ShouldBindJSON(&k); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	k.ID = 0 // client-supplied ids must not force rowids
	if k.KeyValue == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key_value is required"})
		return
	}
	if err := validateKeyStatus(&k); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if k.ProviderID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider_id is required"})
		return
	}
	// Display names are unique per provider: the UI identifies keys by name
	// (no numeric IDs), so a duplicate would make them indistinguishable.
	if k.Name != "" {
		var dup int64
		if err := db.GetDB().Model(&model.Key{}).
			Where("provider_id = ? AND name = ?", k.ProviderID, k.Name).
			Count(&dup).Error; err != nil {
			log.Printf("[admin] CreateKey name check error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate key name"})
			return
		}
		if dup > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "a key with this name already exists for the provider"})
			return
		}
	}
	var count int64
	if err := db.GetDB().Model(&model.Provider{}).Where("id = ?", k.ProviderID).Count(&count).Error; err != nil {
		log.Printf("[admin] CreateKey provider check error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate provider"})
		return
	}
	if count == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider not found"})
		return
	}
	// A new key must not tie with existing sort_orders (drag reorder assigns
	// 0..n-1, so a fresh 0 would silently interleave by rowid and jump the
	// new key to the top). Place it at the end of its provider's order.
	if k.SortOrder <= 0 {
		var maxSort *int64
		if err := db.GetDB().Model(&model.Key{}).
			Where("provider_id = ?", k.ProviderID).
			Select("COALESCE(MAX(sort_order), -1)").
			Scan(&maxSort).Error; err != nil {
			log.Printf("[admin] CreateKey max-sort-order query error (defaulting to 0): %v", err)
		}
		k.SortOrder = 0
		if maxSort != nil && *maxSort >= 0 {
			k.SortOrder = *maxSort + 1
		}
	}
	if err := db.GetDB().Create(&k).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// A fresh key must not inherit a backoff latch, a half-built failure/
	// success streak, or rate-limit window state from a deleted key whose
	// SQLite rowid was reused
	h.HealthChecker.ResetFailCount(k.ID)
	h.Engine.ResetOutcome(k.ID)
	h.Engine.WindowManager.Remove(k.ID)
	h.Engine.Refresh()
	c.JSON(http.StatusCreated, k)
}

// UpdateKey updates a key
func (h *AdminHandler) UpdateKey(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)

	// Inspect the payload BEFORE binding (binding consumes the body)
	body, _ := io.ReadAll(c.Request.Body)
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	var raw map[string]json.RawMessage
	json.Unmarshal(body, &raw)
	_, statusInPayload := raw["status"]
	_, cooldownInPayload := raw["rate_limited_until"]
	_, reasonInPayload := raw["disabled_reason"]
	_, strategyInPayload := raw["recovery_strategy"]
	// Explicit null on any relay-owned field is treated as absent: a null
	// status would persist an empty string (key unusable), null cooldown
	// would instantly re-admit a hot key, null reason would strip
	// "auth_failed" so the health checker never recovers the key, and null
	// recovery_strategy would silently flip a lazy key to immediate.
	for _, f := range []string{"status", "rate_limited_until", "disabled_reason", "recovery_strategy"} {
		if rv, ok := raw[f]; ok && string(rv) == "null" {
			switch f {
			case "status":
				statusInPayload = false
			case "rate_limited_until":
				cooldownInPayload = false
			case "disabled_reason":
				reasonInPayload = false
			case "recovery_strategy":
				strategyInPayload = false
			}
		}
	}

	var k model.Key
	if err := db.GetDB().First(&k, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "key not found"})
		return
	}
	orig := k
	if err := c.ShouldBindJSON(&k); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	k.ID = id
	if err := validateKeyStatus(&k); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if k.KeyValue == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key_value is required"})
		return
	}
	var providerCount int64
	if err := db.GetDB().Model(&model.Provider{}).Where("id = ?", k.ProviderID).Count(&providerCount).Error; err != nil {
		log.Printf("[admin] UpdateKey provider check error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate provider"})
		return
	}
	if providerCount == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider not found"})
		return
	}
	// Display-name uniqueness per provider (excluding the key being edited).
	if k.Name != "" && k.Name != orig.Name {
		var dup int64
		if err := db.GetDB().Model(&model.Key{}).
			Where("provider_id = ? AND name = ? AND id <> ?", k.ProviderID, k.Name, id).
			Count(&dup).Error; err != nil {
			log.Printf("[admin] UpdateKey name check error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate key name"})
			return
		}
		if dup > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "a key with this name already exists for the provider"})
			return
		}
	}

	// Status/cooldown/reason/strategy are owned by the relay and health
	// checker Ã¢â‚¬â€ the edit form doesn't send them, and a stale page-load
	// snapshot must not re-enable a key the relay just disabled (or shrink a
	// fresh cooldown). Only an EXPLICIT status transition in the payload is
	// honored.
	if !statusInPayload {
		k.Status = orig.Status
	} else if k.Status == "" {
		// An explicit empty status would make the key permanently unusable
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must not be empty"})
		return
	}
	if !cooldownInPayload {
		k.RateLimitedUntil = orig.RateLimitedUntil
	}
	if !reasonInPayload {
		k.DisabledReason = orig.DisabledReason
	}
	if !strategyInPayload {
		k.RecoveryStrategy = orig.RecoveryStrategy
	}

	// A deliberate admin disable (explicit "status":"disabled" in the payload)
	// must clear a stale auto-recovery reason: the engine's state machine
	// only auto-recovers disabled keys whose disabled_reason is non-empty
	// (system-set: auth_failed / insufficient_quota), so an empty reason
	// keeps the key out of rotation until an admin re-enables it. This
	// applies even when the key is already disabled (re-affirming the
	// disable).
	if statusInPayload && k.Status == model.KeyStatusDisabled {
		k.DisabledReason = ""
	}
	if err := db.GetDB().Save(&k).Error; err != nil {
		log.Printf("[admin] UpdateKey save error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// A key edit (e.g. fixing the key_value of an auth_failed key) must
	// resume health-check probing (clear the consecutive-failure latch) and
	// drop any half-built state-machine streak — a pre-edit failure must
	// not pre-dispose the edited key.
	h.HealthChecker.ResetFailCount(id)
	h.Engine.ResetOutcome(id)
	h.Engine.Refresh()
	c.JSON(http.StatusOK, k)
}

// DeleteKey deletes a key
func (h *AdminHandler) DeleteKey(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if err := db.GetDB().Delete(&model.Key{}, id).Error; err != nil {
		log.Printf("[admin] DeleteKey error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Drop the key's window state so it can't grow memory/windows.json forever
	h.Engine.WindowManager.Remove(id)
	// Clear any health-checker backoff latch and state-machine streak for
	// the deleted id (rowid reuse)
	h.HealthChecker.ResetFailCount(id)
	h.Engine.ResetOutcome(id)
	h.Engine.Refresh()
	c.JSON(http.StatusNoContent, nil)
}

// GetModelGroups returns all model groups
func (h *AdminHandler) GetModelGroups(c *gin.Context) {
	var groups []model.ModelGroup
	if err := db.GetDB().Find(&groups).Error; err != nil {
		log.Printf("[admin] GetModelGroups error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load model groups"})
		return
	}
	c.JSON(http.StatusOK, groups)
}

// CreateModelGroup creates a new model group
func (h *AdminHandler) CreateModelGroup(c *gin.Context) {
	// Inspect the payload BEFORE binding (binding consumes the body)
	enabledProvided := bodyHasKey(c, "enabled")

	var mg model.ModelGroup
	if err := c.ShouldBindJSON(&mg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	mg.ID = 0 // client-supplied ids must not force rowids
	if mg.GroupID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group_id is required"})
		return
	}
	if mg.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	// Default new groups to enabled unless the payload explicitly said false
	if !enabledProvided {
		mg.Enabled = true
	}
	if err := db.GetDB().Create(&mg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.Engine.Refresh()
	c.JSON(http.StatusCreated, mg)
}

// UpdateModelGroup updates a model group
func (h *AdminHandler) UpdateModelGroup(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var mg model.ModelGroup
	if err := db.GetDB().First(&mg, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "model group not found"})
		return
	}
	if err := c.ShouldBindJSON(&mg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	mg.ID = id
	if err := db.GetDB().Save(&mg).Error; err != nil {
		log.Printf("[admin] UpdateModelGroup save error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.Engine.Refresh()
	c.JSON(http.StatusOK, mg)
}

// DeleteModelGroup deletes a model group and its routes (transactional)
func (h *AdminHandler) DeleteModelGroup(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	tx := db.GetDB().Begin()

	if err := tx.Delete(&model.ModelGroup{}, id).Error; err != nil {
		tx.Rollback()
		log.Printf("[admin] DeleteModelGroup error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Where("model_group_id = ?", id).Delete(&model.Route{}).Error; err != nil {
		tx.Rollback()
		log.Printf("[admin] DeleteModelGroup cascade route delete error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := tx.Commit().Error; err != nil {
		tx.Rollback()
		log.Printf("[admin] DeleteModelGroup commit error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	h.Engine.Refresh()
	c.JSON(http.StatusNoContent, nil)
}

// GetRoutes returns all routes, ordered by (model_group_id, priority, id).
// priority is per-group, so ordering by it alone would interleave groups'
// rows (g1:0, g2:0, g1:1, ...) — the UI renders each group's rows as a
// contiguous block and relies on that when reordering by drag.
func (h *AdminHandler) GetRoutes(c *gin.Context) {
	var routes []model.Route
	query := db.GetDB().Preload("ModelGroup").Preload("Provider").Order("model_group_id ASC, priority ASC, id ASC")
	if groupID := c.Query("model_group_id"); groupID != "" {
		query = query.Where("model_group_id = ?", groupID)
	}
	if err := query.Find(&routes).Error; err != nil {
		log.Printf("[admin] GetRoutes error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load routes"})
		return
	}
	c.JSON(http.StatusOK, routes)
}

// bodyHasKey reports whether the raw request body contains the given key with
// a non-null value (read once; the body is restored for binding). Explicit
// null is treated as absent, matching the UpdateKey convention.
func bodyHasKey(c *gin.Context, key string) bool {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	v, ok := m[key]
	if !ok || string(v) == "null" {
		return false
	}
	return true
}

// validateExtraParams ensures the model group's extra params is either empty
// or a valid JSON object (a top-level array/scalar would break the relay
// merge).
func validateExtraParams(extra string) error {
	if extra == "" {
		return nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(extra), &m); err != nil {
		return fmt.Errorf("extra_params must be a valid JSON object: %v", err)
	}
	return nil
}

// CreateRoute creates a new route
func (h *AdminHandler) CreateRoute(c *gin.Context) {
	// Inspect the payload BEFORE binding (binding consumes the body)
	enabledProvided := bodyHasKey(c, "enabled")

	var r model.Route
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	r.ID = 0 // client-supplied ids must not force rowids
	// Validate references before inserting
	var groupCount, providerCount int64
	if err := db.GetDB().Model(&model.ModelGroup{}).Where("id = ?", r.ModelGroupID).Count(&groupCount).Error; err != nil {
		log.Printf("[admin] CreateRoute group check error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate model group"})
		return
	}
	if err := db.GetDB().Model(&model.Provider{}).Where("id = ?", r.ProviderID).Count(&providerCount).Error; err != nil {
		log.Printf("[admin] CreateRoute provider check error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate provider"})
		return
	}
	if groupCount == 0 || providerCount == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model_group_id and provider_id must reference existing records"})
		return
	}
	// Default new routes to enabled unless the payload explicitly said false
	if !enabledProvided {
		r.Enabled = true
	}
	// Bound weight so weightedOrder's int sum can't overflow and panic
	if r.Weight < 1 || r.Weight > 1000000 {
		r.Weight = 10
	}
	// A new route must not tie with existing priorities (drag reorder assigns
	// 0..n-1, so a fresh 0 would silently interleave by rowid). Place it at
	// the end of its group's priority order.
	if r.Priority <= 0 {
		var maxPrio *int
		if err := db.GetDB().Model(&model.Route{}).
			Where("model_group_id = ?", r.ModelGroupID).
			Select("COALESCE(MAX(priority), -1)").
			Scan(&maxPrio).Error; err != nil {
			log.Printf("[admin] CreateRoute max-priority query error (defaulting to 0): %v", err)
		}
		r.Priority = 0
		if maxPrio != nil && *maxPrio >= 0 {
			r.Priority = *maxPrio + 1
		}
	}
	// A route is identified by (model group, provider, target model): the UI
	// shows these names instead of the numeric ID, so duplicates would make
	// routes indistinguishable.
	var dup int64
	if err := db.GetDB().Model(&model.Route{}).
		Where("model_group_id = ? AND provider_id = ? AND target_model = ?",
			r.ModelGroupID, r.ProviderID, r.TargetModel).
		Count(&dup).Error; err != nil {
		log.Printf("[admin] CreateRoute uniqueness check error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate route"})
		return
	}
	if dup > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a route for this model group / provider / target model already exists"})
		return
	}
	if err := validateExtraParams(r.ExtraParams); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := db.GetDB().Create(&r).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.Engine.Refresh()
	c.JSON(http.StatusCreated, r)
}

// UpdateRoute updates a route
func (h *AdminHandler) UpdateRoute(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var r model.Route
	if err := db.GetDB().First(&r, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "route not found"})
		return
	}
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	r.ID = id
	// Validate references (same as CreateRoute) so a route can't silently
	// point at a missing provider/group and vanish from rotation on Refresh
	var groupCount, providerCount int64
	if err := db.GetDB().Model(&model.ModelGroup{}).Where("id = ?", r.ModelGroupID).Count(&groupCount).Error; err != nil {
		log.Printf("[admin] UpdateRoute group check error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate model group"})
		return
	}
	if err := db.GetDB().Model(&model.Provider{}).Where("id = ?", r.ProviderID).Count(&providerCount).Error; err != nil {
		log.Printf("[admin] UpdateRoute provider check error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate provider"})
		return
	}
	if groupCount == 0 || providerCount == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model_group_id and provider_id must reference existing records"})
		return
	}
	// Bound weight so weightedOrder's int sum can't overflow and panic
	if r.Weight < 1 || r.Weight > 1000000 {
		r.Weight = 10
	}
	// Route identity uniqueness (excluding this route itself)
	var dup int64
	if err := db.GetDB().Model(&model.Route{}).
		Where("model_group_id = ? AND provider_id = ? AND target_model = ? AND id <> ?",
			r.ModelGroupID, r.ProviderID, r.TargetModel, id).
		Count(&dup).Error; err != nil {
		log.Printf("[admin] UpdateRoute uniqueness check error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate route"})
		return
	}
	if dup > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a route for this model group / provider / target model already exists"})
		return
	}
	if err := validateExtraParams(r.ExtraParams); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := db.GetDB().Save(&r).Error; err != nil {
		log.Printf("[admin] UpdateRoute save error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.Engine.Refresh()
	c.JSON(http.StatusOK, r)
}

// ReorderRoutes batch-updates route priorities based on visual ordering
func (h *AdminHandler) ReorderRoutes(c *gin.Context) {
	var req struct {
		Routes []struct {
			ID       int64 `json:"id"`
			Priority int   `json:"priority"`
		} `json:"routes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tx := db.GetDB().Begin()
	for _, r := range req.Routes {
		if err := tx.Model(&model.Route{}).Where("id = ?", r.ID).Update("priority", r.Priority).Error; err != nil {
			tx.Rollback()
			log.Printf("[admin] ReorderRoutes error for route %d: %v", r.ID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "reorder failed"})
			return
		}
	}
	if err := tx.Commit().Error; err != nil {
		log.Printf("[admin] ReorderRoutes commit error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reorder commit failed"})
		return
	}

	h.Engine.Refresh()
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// DeleteRoute deletes a route
func (h *AdminHandler) DeleteRoute(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if err := db.GetDB().Delete(&model.Route{}, id).Error; err != nil {
		log.Printf("[admin] DeleteRoute error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.Engine.Refresh()
	c.JSON(http.StatusNoContent, nil)
}

// ReorderKeys batch-updates key sort_order based on visual ordering (the
// order the user arranged keys within a provider; sort_order = call order).
// sort_order is per-provider: the payload carries all keys with their
// per-provider sort_order indices (0..n-1 within each provider).
func (h *AdminHandler) ReorderKeys(c *gin.Context) {
	var req struct {
		Keys []struct {
			ID        int64 `json:"id"`
			SortOrder int64 `json:"sort_order"`
		} `json:"keys"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tx := db.GetDB().Begin()
	for _, r := range req.Keys {
		if err := tx.Model(&model.Key{}).Where("id = ?", r.ID).Update("sort_order", r.SortOrder).Error; err != nil {
			tx.Rollback()
			log.Printf("[admin] ReorderKeys error for key %d: %v", r.ID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "reorder failed"})
			return
		}
	}
	if err := tx.Commit().Error; err != nil {
		log.Printf("[admin] ReorderKeys commit error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reorder commit failed"})
		return
	}

	h.Engine.Refresh()
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// GetPricings returns all pricing rules
func (h *AdminHandler) GetPricings(c *gin.Context) {
	var pricings []model.Pricing
	if err := db.GetDB().Find(&pricings).Error; err != nil {
		log.Printf("[admin] GetPricings error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load pricings"})
		return
	}
	c.JSON(http.StatusOK, pricings)
}

// CreatePricing creates a new pricing rule
func (h *AdminHandler) CreatePricing(c *gin.Context) {
	var p model.Pricing
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	p.ID = 0 // client-supplied ids must not force rowids
	if p.ModelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model_name is required"})
		return
	}
	var count int64
	if err := db.GetDB().Model(&model.Pricing{}).Where("model_name = ?", p.ModelName).Count(&count).Error; err != nil {
		log.Printf("[admin] CreatePricing check error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate pricing"})
		return
	}
	if count > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pricing for this model already exists"})
		return
	}
	if err := db.GetDB().Create(&p).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.Engine.Calculator.RefreshPricing()
	c.JSON(http.StatusCreated, p)
}

// UpdatePricing updates a pricing rule
func (h *AdminHandler) UpdatePricing(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var p model.Pricing
	if err := db.GetDB().First(&p, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "pricing not found"})
		return
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	p.ID = id
	// Changing model_name to an existing value must be a 400, not a raw
	// SQLite constraint 500 (same as CreatePricing)
	var dup int64
	if err := db.GetDB().Model(&model.Pricing{}).Where("model_name = ? AND id <> ?", p.ModelName, id).Count(&dup).Error; err != nil {
		log.Printf("[admin] UpdatePricing dup check error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate pricing"})
		return
	}
	if dup > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pricing for this model already exists"})
		return
	}
	if err := db.GetDB().Save(&p).Error; err != nil {
		log.Printf("[admin] UpdatePricing save error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.Engine.Calculator.RefreshPricing()
	c.JSON(http.StatusOK, p)
}

// DeletePricing deletes a pricing rule
func (h *AdminHandler) DeletePricing(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if err := db.GetDB().Delete(&model.Pricing{}, id).Error; err != nil {
		log.Printf("[admin] DeletePricing error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.Engine.Calculator.RefreshPricing()
	c.JSON(http.StatusNoContent, nil)
}

// GetSettings returns all settings
func (h *AdminHandler) GetSettings(c *gin.Context) {
	var settings []model.Setting
	if err := db.GetDB().Find(&settings).Error; err != nil {
		log.Printf("[admin] GetSettings error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load settings"})
		return
	}
	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	c.JSON(http.StatusOK, result)
}

// UpdateSettings updates settings
func (h *AdminHandler) UpdateSettings(c *gin.Context) {
	var settings map[string]string
	if err := c.ShouldBindJSON(&settings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Validate known settings so a bad value can't brick startup
	// (e.g. an invalid port that fails to bind, or a 1-second health interval)
	if v, ok := settings[model.SettingPort]; ok {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1024 || port > 65535 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "server.port must be between 1024 and 65535"})
			return
		}
	}
	if v, ok := settings[model.SettingHealthCheck]; ok {
		sec, err := strconv.Atoi(v)
		if err != nil || sec < 10 || sec > 3600 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "server.health_check_interval must be between 10 and 3600 seconds"})
			return
		}
	}
	var failed []string
	for k, v := range settings {
		if err := db.SetSetting(k, v); err != nil {
			log.Printf("[admin] UpdateSettings error for %s: %v", k, err)
			failed = append(failed, k)
		}
	}
	if len(failed) > 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save settings: " + strings.Join(failed, ", ")})
		return
	}
	// Restart health checker if interval changed. Do it asynchronously: Stop()
	// waits for in-flight key probes (up to ~10s each), which would otherwise
	// block the settings request past the UI's axios timeout.
	if _, ok := settings[model.SettingHealthCheck]; ok {
		go h.HealthChecker.Restart()
	}
	// Refresh engine if retry changed
	if _, ok := settings[model.SettingRetryTimes]; ok {
		h.Engine.Refresh()
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// GetOverview returns aggregate statistics
func (h *AdminHandler) GetOverview(c *gin.Context) {
	stats, err := billing.GetOverview()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, stats)
}

// GetKeyDetail returns detailed stats for a single key
func (h *AdminHandler) GetKeyDetail(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)

	var key model.Key
	if err := db.GetDB().Preload("Provider").First(&key, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "key not found"})
		return
	}

	// Get window counts
	counts := make(map[string]gin.H)
	for _, wt := range []model.WindowType{
		model.WindowRPM, model.WindowTPM, model.WindowRP5h,
		model.WindowRPD, model.WindowRPW, model.WindowRPMo,
	} {
		counts[string(wt)] = gin.H{
			"count":       h.Engine.WindowManager.GetCount(id, wt),
			"token_count": h.Engine.WindowManager.GetTokens(id, wt),
			"cost":        h.Engine.WindowManager.GetCost(id, wt), // micro-USD
		}
	}

	// Get recent consumption (most recent first)
	since := time.Now().Add(-24 * time.Hour)
	consumptions, err := billing.GetConsumptionSummary(id, since, time.Now())
	if err != nil {
		log.Printf("[admin] GetKeyDetail consumption error for key %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load consumption"})
		return
	}

	totalCost, err := billing.GetTotalCost(id)
	if err != nil {
		log.Printf("[admin] GetKeyDetail total-cost error for key %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load total cost"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"key":          key,
		"counts":       counts,
		"consumptions": consumptions,
		"total_cost":   totalCost,
	})
}

// GetStatsConsumptions returns consumption records
func (h *AdminHandler) GetStatsConsumptions(c *gin.Context) {
	var consumptions []model.Consumption
	// No Preload("Key"): embedding the full key row would leak key_value and
	// bloat the response (the UI only uses key_id).
	query := db.GetDB()

	if keyID := c.Query("key_id"); keyID != "" {
		query = query.Where("key_id = ?", keyID)
	}
	// Activity-page entity filter (Model / API Key / App).
	filtered, err := applyActivityFilter(query, c.Query("filter_type"), c.Query("filter_value"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	query = filtered
	if since := c.Query("since"); since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			// Floor to the LOCAL hour: hour_bucket holds the whole hour's
			// usage, so a 15m/30m window starting at 16:05 must still match
			// the 16:00 bucket — otherwise those presets show nothing for
			// most of the hour (the chart axis floors the same way).
			l := t.Local()
			query = query.Where("hour_bucket >= ?", time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), 0, 0, 0, l.Location()))
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid since parameter"})
			return
		}
	}
	if until := c.Query("until"); until != "" {
		if t, err := time.Parse(time.RFC3339, until); err == nil {
			query = query.Where("hour_bucket <= ?", t.Local())
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid until parameter"})
			return
		}
	}

	// Generous cap. Rows are hourly per (key, model, app): 24h × 7d is 168
	// per (key, model, app), so a key serving several model/app combos emits
	// multiples of that. 100000 still covers hundreds of keys without
	// truncating the Stats page charts.
	if err := query.Order("hour_bucket DESC").Limit(100000).Find(&consumptions).Error; err != nil {
		log.Printf("[admin] GetStatsConsumptions error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load consumptions"})
		return
	}
	c.JSON(http.StatusOK, consumptions)
}

// GetKeyStatuses returns health status of all keys
// ResetKeySpend resets a key's lifetime spend budget usage back to zero and
// re-enables the key (undoes a "spend_limit_exhausted" disable). The UI
// confirms before calling this.
func (h *AdminHandler) ResetKeySpend(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid key id"})
		return
	}
	res := db.GetDB().Model(&model.Key{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"total_spent":     0,
			"status":          model.KeyStatusActive,
			"disabled_reason": "",
		})
	if res.Error != nil {
		log.Printf("[admin] ResetKeySpend error: %v", res.Error)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reset key spend"})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "key not found"})
		return
	}
	h.Engine.ResetOutcome(id) // fresh budget = fresh streaks
	h.Engine.Refresh()        // reload in-memory status
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
func (h *AdminHandler) GetKeyStatuses(c *gin.Context) {
	results, err := health.GetKeyStatuses()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load key statuses"})
		return
	}
	c.JSON(http.StatusOK, results)
}

// ReloadConfig reloads routing and pricing data
func (h *AdminHandler) ReloadConfig(c *gin.Context) {
	h.Engine.Refresh()
	h.Engine.Calculator.RefreshPricing()
	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "configuration reloaded"})
}

// Restart accepts a gateway restart: new requests' connections are closed
// without a response (clients see a connection failure and auto-retry)
// while in-flight API calls finish naturally, then the process exits and a
// fresh instance takes over (so settings like the server port take
// effect). The fresh instance is scheduled BEFORE the response so a
// scheduling failure is reported as 500 (and a later attempt can succeed);
// the 200 is then written and flushed before the quit hook runs — the
// process may exit as soon as the drain completes, so callers must not
// depend on the connection staying open.
func (h *AdminHandler) Restart(c *gin.Context) {
	if h.RestartSchedule == nil || h.RestartQuit == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "restart is not supported in this build"})
		return
	}
	h.restartMu.Lock()
	if h.restarting {
		h.restartMu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "restart already in progress"})
		return
	}
	if err := h.RestartSchedule(); err != nil {
		h.restartMu.Unlock()
		log.Printf("[admin] restart scheduling failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to schedule restart: " + err.Error()})
		return
	}
	h.restarting = true
	h.restartMu.Unlock()

	c.JSON(http.StatusOK, gin.H{"status": "restarting"})
	c.Writer.Flush()
	h.RestartQuit()
}

// GetAutostart returns the current launch-at-login state.
func (h *AdminHandler) GetAutostart(c *gin.Context) {
	enabled := false
	supported := h.AutostartEnabled != nil
	if supported {
		enabled = h.AutostartEnabled()
	}
	c.JSON(http.StatusOK, gin.H{"enabled": enabled, "supported": supported})
}

// SetAutostart enables or disables launch-at-login.
func (h *AdminHandler) SetAutostart(c *gin.Context) {
	if h.AutostartSet == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "autostart is only supported on Windows"})
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.AutostartSet(req.Enabled); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"enabled": req.Enabled})
}

// CheckUpdate queries GitHub Releases for a newer version. Errors are
// reported as a 200 with update_available=false + error message so the UI can
// show them inline (a GitHub outage must not look like "no update").
func (h *AdminHandler) CheckUpdate(c *gin.Context) {
	info, err := h.Updater.Check()
	if err != nil {
		log.Printf("[admin] update check failed: %v", err)
		c.JSON(http.StatusOK, gin.H{
			"current_version":  h.Updater.CurrentVersion(),
			"latest_version":   h.Updater.CurrentVersion(),
			"update_available": false,
			"install_mode":     h.Updater.InstallMode(),
			"error":            err.Error(),
		})
		return
	}
	// Cache the result so a subsequent Apply uses it without a second GitHub
	// roundtrip (unauthenticated rate limits).
	h.SetAutoCheckInfo(info)
	c.JSON(http.StatusOK, info)
}

// ApplyUpdate applies the latest release (portable: replace exe; installed:
// launch the installer). It uses the most recent server-side check result
// (auto-check or manual CheckUpdate) and falls back to a live Check() when
// none is cached or it is stale. The request body is IGNORED: the
// management API is unauthenticated on localhost, so trusting a
// client-supplied asset URL would let any local process trigger the
// elevated launch of an arbitrary binary. The app then exits via the exit
// hook so the new binary can replace it.
func (h *AdminHandler) ApplyUpdate(c *gin.Context) {
	info := h.lastCheckInfo()
	if info == nil || !info.UpdateAvailable || time.Since(info.CheckedAt) > 24*time.Hour {
		checked, err := h.Updater.Check()
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "update check failed: " + err.Error()})
			return
		}
		info = checked
	}
	if !info.UpdateAvailable {
		c.JSON(http.StatusConflict, gin.H{"error": "no update available"})
		return
	}
	if err := h.Updater.Apply(info); err != nil {
		log.Printf("[admin] update apply failed: %v", err)
		if errors.Is(err, update.ErrUpdateCancelled) {
			// The user declined the UAC prompt — not a failure, and the app
			// must NOT exit (there is no update to apply).
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "applied", "install_mode": info.InstallMode})
	// Flush the response, then exit so the new binary / installer can
	// replace this one (portable: the swap script relaunches the app;
	// installed: the installer's Finish page starts the updated copy).
	c.Writer.Flush()
	if h.ExitAfterUpdate != nil {
		h.ExitAfterUpdate()
	}
}

// ActivitySeriesPoint is one (time bucket, group) value for the stacked chart.
type ActivitySeriesPoint struct {
	Bucket   string  `json:"bucket"`             // "MM-DD" or "MM-DD HH:00"
	Group    string  `json:"group"`              // model name / key name / app name
	Subgroup string  `json:"subgroup,omitempty"` // second dimension (empty when no subgroup)
	Value    float64 `json:"value"`
	IsZero   bool    `json:"is_zero"` // explicit zero for chart stacking
}

// activityAcc accumulates one (bucket, group) cell. sum is the selected
// metric's value; spend/tokens/requests/cache track every metric so "rank
// by <other metric>" can re-rank groups without re-reading the rows.
type activityAcc struct {
	sum      float64 // selected metric
	spend    float64
	tokens   float64
	requests float64
	cache    float64
}

// blendedRate converts total spend/tokens into the blended $/1M rate (cost
// per million tokens). Zero-token traffic has no rate. Rates are never
// accumulated across rows — spend and tokens are, and the rate is derived
// at output time, so a group's rate is ALWAYS total spend / total tokens.
func blendedRate(spend, tokens float64) float64 {
	if tokens <= 0 {
		return 0
	}
	return spend / tokens * 1e6
}

// accValue returns the charted metric's value for one (bucket, group) cell:
// the accumulated sum for additive metrics, the cell's derived rate for
// blended $/1M (per-row rate sums would over-weight low-volume rows).
func accValue(metric string, a *activityAcc) float64 {
	if metric == "blended" {
		return blendedRate(a.spend, a.tokens)
	}
	return a.sum
}

// ActivityGroupSummary is one row of the Explore-style summary table.
type ActivityGroupSummary struct {
	Group   string  `json:"group"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Avg     float64 `json:"avg"`
	Sum     float64 `json:"sum"`
	Value   float64 `json:"value"`   // the last bucket's value (OpenRouter shows Value column)
	Percent float64 `json:"percent"` // % of total (0-100)
}

// ActivityResponse is the payload for the Activity page (Overview/Trends/Explore).
type ActivityResponse struct {
	Metric  string                 `json:"metric"`
	GroupBy string                 `json:"group_by"`
	Rollup  string                 `json:"rollup"`
	Series  []ActivitySeriesPoint  `json:"series"`
	Summary []ActivityGroupSummary `json:"summary"`
	Buckets []string               `json:"buckets"` // ordered bucket labels
	Totals  map[string]float64     `json:"totals"`  // metric totals: spend/tokens/requests/cache
}

// applyActivityFilter constrains a consumption query by the Activity page's
// entity filter (filter_type=model|key|app + filter_value). Shared by
// GetActivity and GetStatsConsumptions so both endpoints see the same rows.
// "Unknown" matches empty model/app names — the label the UI shows for rows
// without one. filter_value for filter_type=key is the key's numeric id.
func applyActivityFilter(q *gorm.DB, filterType, filterValue string) (*gorm.DB, error) {
	if filterType == "" {
		if filterValue != "" {
			return nil, errors.New("filter_value requires filter_type")
		}
		return q, nil
	}
	if filterType != "model" && filterType != "key" && filterType != "app" {
		return nil, errors.New("filter_type must be model|key|app")
	}
	if filterValue == "" {
		return nil, fmt.Errorf("filter_value is required for filter_type=%s", filterType)
	}
	switch filterType {
	case "model":
		if filterValue == "Unknown" {
			return q.Where("model_name = '' OR model_name IS NULL"), nil
		}
		return q.Where("model_name = ?", filterValue), nil
	case "key":
		id, err := strconv.ParseInt(filterValue, 10, 64)
		if err != nil {
			return nil, errors.New("filter_value must be a key id for filter_type=key")
		}
		return q.Where("key_id = ?", id), nil
	default: // app
		if filterValue == "Unknown" {
			return q.Where("app_name = '' OR app_name IS NULL"), nil
		}
		return q.Where("app_name = ?", filterValue), nil
	}
}

// GetActivity aggregates consumption for the Activity page.
// Query params:
//
//	metric:   spend | tokens | requests | cache | blended   (default spend;
//	          blended = $/1M tokens = spend/tokens*1e6, a RATE computed per
//	          bucket/group at output time, never summed across rows)
//	group_by: model | key | app                    (default model)
//	subgroup: model | key | app                    (optional second dimension,
//	          must differ from group_by; splits each group's series into
//	          per-subgroup stacks, e.g. spend by model, split by API key)
//	rollup:   hour | day | week | month | total    (default day; total
//	          collapses the whole range into a single "Total" bucket)
//	rank_by:  current | spend | tokens | requests | cache | blended (default
//	          current; ranks the series Top-N (and the summary's default
//	          order) by the given metric, which may differ from the charted
//	          one. For the blended RATE metrics the rank uses the group's
//	          overall rate — sums of per-bucket rates are not rates)
//	since / until: RFC3339, inclusive range
//	filter_type / filter_value: restrict rows to one entity before
//	          aggregating (the Activity page's filter button). filter_type is
//	          model|key|app; filter_value is the model/app name or a key id.
//	          "Unknown" matches rows with an empty model/app name.
func (h *AdminHandler) GetActivity(c *gin.Context) {
	metric := c.DefaultQuery("metric", "spend")
	groupBy := c.DefaultQuery("group_by", "model")
	subgroup := c.DefaultQuery("subgroup", "")
	rollup := c.DefaultQuery("rollup", "day")
	rankBy := c.DefaultQuery("rank_by", "current")
	// Top-N for the chart: series beyond this many groups are folded into an
	// "Other" series (#94a3b8) like OpenRouter. 0 = no folding.
	topN := 0
	if t := c.Query("top"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n > 0 {
			topN = n
		}
	}
	if metric != "spend" && metric != "tokens" && metric != "requests" && metric != "cache" && metric != "blended" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "metric must be spend|tokens|requests|cache|blended"})
		return
	}
	if groupBy != "model" && groupBy != "key" && groupBy != "app" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group_by must be model|key|app"})
		return
	}
	if subgroup != "" {
		if subgroup != "model" && subgroup != "key" && subgroup != "app" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "subgroup must be model|key|app"})
			return
		}
		if subgroup == groupBy {
			c.JSON(http.StatusBadRequest, gin.H{"error": "subgroup must differ from group_by"})
			return
		}
	}
	if rollup != "hour" && rollup != "day" && rollup != "week" && rollup != "month" && rollup != "total" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rollup must be hour|day|week|month|total"})
		return
	}
	if rankBy != "current" && rankBy != "spend" && rankBy != "tokens" && rankBy != "requests" && rankBy != "cache" && rankBy != "blended" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rank_by must be current|spend|tokens|requests|cache|blended"})
		return
	}

	since := time.Now().Add(-7 * 24 * time.Hour)
	if s := c.Query("since"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			since = t.Local()
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid since parameter"})
			return
		}
	}
	until := time.Now()
	if u := c.Query("until"); u != "" {
		if t, err := time.Parse(time.RFC3339, u); err == nil {
			until = t.Local()
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid until parameter"})
			return
		}
	}

	// Load consumption in range, restricted by the entity filter. The window
	// is widened to the rollup buckets CONTAINING its endpoints: hour_bucket
	// rows are truncated to the local hour, so a range starting at 16:05 must
	// still include the 16:00 bucket (it holds the 16:00–17:00 usage) —
	// without the floor the 15m/30m presets show nothing for most of the
	// hour. buildActivityAxis floors the same way, so the query and the
	// response axis always agree.
	from, to := activityWindow(since, until, rollup)
	query := db.GetDB().Where("hour_bucket >= ? AND hour_bucket < ?", from, to)
	query, err := applyActivityFilter(query, c.Query("filter_type"), c.Query("filter_value"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var rows []model.Consumption
	if err := query.Order("hour_bucket ASC").Find(&rows).Error; err != nil {
		log.Printf("[admin] GetActivity load error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load consumption"})
		return
	}

	// Load key names (for group_by/subgroup = key display).
	keyNames := make(map[int64]string)
	var keys []model.Key
	if groupBy == "key" || groupBy == "app" || subgroup == "key" {
		if err := db.GetDB().Find(&keys).Error; err == nil {
			for i := range keys {
				keyNames[keys[i].ID] = keys[i].Name
			}
		}
	}

	// groupOf maps a consumption row to its display group.
	groupOf := func(r *model.Consumption) string {
		switch groupBy {
		case "model":
			if r.ModelName == "" {
				return "Unknown"
			}
			return r.ModelName
		case "key":
			if n, ok := keyNames[r.KeyID]; ok && n != "" {
				return n
			}
			return fmt.Sprintf("Key #%d", r.KeyID)
		default: // app = the attribution-detected client app ("" = "Unknown")
			if r.AppName != "" {
				return r.AppName
			}
			return "Unknown"
		}
	}

	// valueOf extracts the metric value from a row. The blended metric has no
	// per-row value: it is a RATE derived at output time (accValue) from the
	// cell's accumulated spend/tokens, so sum stays 0 for it.
	valueOf := func(r *model.Consumption) float64 {
		switch metric {
		case "spend":
			return r.CostUSD
		case "tokens":
			return float64(r.InputTokens + r.OutputTokens)
		case "requests":
			return float64(r.RequestCount)
		case "blended":
			return 0
		default: // cache
			return float64(r.CacheHitTokens)
		}
	}

	// subgroupOf maps a row to its subgroup label (only used when the
	// subgroup param is set).
	subgroupOf := func(r *model.Consumption) string {
		switch subgroup {
		case "model":
			if r.ModelName == "" {
				return "Unknown"
			}
			return r.ModelName
		case "key":
			if n, ok := keyNames[r.KeyID]; ok && n != "" {
				return n
			}
			return fmt.Sprintf("Key #%d", r.KeyID)
		default: // app
			if r.AppName != "" {
				return r.AppName
			}
			return "Unknown"
		}
	}

	// Aggregate: bucket -> group -> sum.
	agg := make(map[string]map[string]*activityAcc)
	// Bucket labels are year-qualified ("YYYY-MM-DD", "YYYY-MM-DD 15:00",
	// "2006-01-02", "YYYY-MM") so a long or year-spanning range never
	// collides two same-month-day buckets into one aggregate.
	bucketOrder := buildActivityAxis(since, until, rollup)
	// bucketOf labels a consumption row's hour bucket; must match the axis
	// labels exactly (shared formatter) so rows land on the axis.
	bucketOf := func(t time.Time) string {
		return activityBucketLabel(t, rollup)
	}
	groupOrder := make([]string, 0)
	seenGroup := make(map[string]bool)

	for _, b := range bucketOrder {
		agg[b] = make(map[string]*activityAcc)
	}

	for i := range rows {
		b := bucketOf(rows[i].HourBucket)
		g := groupOf(&rows[i])
		if _, ok := agg[b][g]; !ok {
			agg[b][g] = &activityAcc{}
		}
		a := agg[b][g]
		a.sum += valueOf(&rows[i])
		a.spend += rows[i].CostUSD
		a.tokens += float64(rows[i].InputTokens + rows[i].OutputTokens)
		a.requests += float64(rows[i].RequestCount)
		a.cache += float64(rows[i].CacheHitTokens)
		if !seenGroup[g] {
			seenGroup[g] = true
			groupOrder = append(groupOrder, g)
		}
	}

	// Subgroup aggregation: bucket -> group -> subgroup -> sum. Only built
	// when requested; the summary table stays per primary group, the series
	// is split per subgroup.
	subAgg := make(map[string]map[string]map[string]*activityAcc)
	subgroupOrder := make(map[string][]string) // group -> subgroups, by sum desc
	if subgroup != "" {
		seenSubgroup := make(map[string]bool)
		for i := range rows {
			b := bucketOf(rows[i].HourBucket)
			g := groupOf(&rows[i])
			sg := subgroupOf(&rows[i])
			if subAgg[b] == nil {
				subAgg[b] = make(map[string]map[string]*activityAcc)
			}
			if subAgg[b][g] == nil {
				subAgg[b][g] = make(map[string]*activityAcc)
			}
			if subAgg[b][g][sg] == nil {
				subAgg[b][g][sg] = &activityAcc{}
			}
			subAgg[b][g][sg].sum += valueOf(&rows[i])
			// Spend/tokens are always tracked so subgroup series can derive
			// rate metrics (blended $/1M) the same way the main cells do.
			subAgg[b][g][sg].spend += rows[i].CostUSD
			subAgg[b][g][sg].tokens += float64(rows[i].InputTokens + rows[i].OutputTokens)
			if !seenSubgroup[g+"\x00"+sg] {
				seenSubgroup[g+"\x00"+sg] = true
				subgroupOrder[g] = append(subgroupOrder[g], sg)
			}
		}
		// Order each group's subgroups by total sum (desc, name asc on ties)
		// so the chart's stack order is deterministic.
		for g := range subgroupOrder {
			sort.Slice(subgroupOrder[g], func(i, j int) bool {
				var si, sj float64
				for _, b := range bucketOrder {
					if m, ok := subAgg[b][g]; ok {
						if a, ok := m[subgroupOrder[g][i]]; ok {
							si += a.sum
						}
						if a, ok := m[subgroupOrder[g][j]]; ok {
							sj += a.sum
						}
					}
				}
				if si != sj {
					return si > sj
				}
				return subgroupOrder[g][i] < subgroupOrder[g][j]
			})
		}
	}

	// Series groups are ordered by the rank metric (desc) so the chart's
	// Top-N set matches the summary table's ordering. "current" means the
	// selected chart metric (its totals live in acc.sum).
	rankTotal := func(acc *activityAcc) float64 {
		switch rankBy {
		case "spend":
			return acc.spend
		case "tokens":
			return acc.tokens
		case "requests":
			return acc.requests
		case "cache":
			return acc.cache
		default:
			return acc.sum
		}
	}
	// rateRank: the rank metric is a RATE (blended $/1M) — either explicitly
	// via rank_by=blended, or because the charted metric is blended and
	// rank_by is "current". Rates don't sum across buckets, so the rank uses
	// the group's OVERALL rate (total spend / total tokens), not the sum of
	// per-bucket rates. rankTotal's switch never sees "blended": every
	// blended ranking goes through this branch.
	rateRank := (metric == "blended" && rankBy == "current") || rankBy == "blended"
	// groupSpend/groupTokens: per-group totals of spend/tokens over the whole
	// range — the inputs for every blended-rate derivation (ranking, summary
	// Sum). Computed once and shared by both consumers.
	groupSpend := make(map[string]float64, len(groupOrder))
	groupTokens := make(map[string]float64, len(groupOrder))
	for _, g := range groupOrder {
		for _, b := range bucketOrder {
			if m, ok := agg[b][g]; ok {
				groupSpend[g] += m.spend
				groupTokens[g] += m.tokens
			}
		}
	}
	// rankTotals: per-group total of the rank metric, computed once and used
	// by both the series ordering and the summary sort.
	rankTotals := make(map[string]float64, len(groupOrder))
	for _, g := range groupOrder {
		var s float64
		if rateRank {
			s = blendedRate(groupSpend[g], groupTokens[g])
		} else {
			for _, b := range bucketOrder {
				if m, ok := agg[b][g]; ok {
					s += rankTotal(m)
				}
			}
		}
		rankTotals[g] = s
	}
	sort.Slice(groupOrder, func(i, j int) bool {
		si, sj := rankTotals[groupOrder[i]], rankTotals[groupOrder[j]]
		if si != sj {
			return si > sj
		}
		return groupOrder[i] < groupOrder[j]
	})

	// Build the response.
	resp := ActivityResponse{
		Metric:  metric,
		GroupBy: groupBy,
		Rollup:  rollup,
		Series:  make([]ActivitySeriesPoint, 0),
		Summary: make([]ActivityGroupSummary, 0),
		Buckets: bucketOrder,
		Totals:  map[string]float64{"spend": 0, "tokens": 0, "requests": 0, "cache": 0},
	}
	for i := range rows {
		resp.Totals["spend"] += rows[i].CostUSD
		resp.Totals["tokens"] += float64(rows[i].InputTokens + rows[i].OutputTokens)
		resp.Totals["requests"] += float64(rows[i].RequestCount)
		resp.Totals["cache"] += float64(rows[i].CacheHitTokens)
	}

	// Series: for each bucket, for each group. Groups beyond the Top-N are
	// folded into a single "Other" series (OR behavior). With a subgroup,
	// each top group is split into per-subgroup stacks (subgroups ordered by
	// sum desc); "Other" stays a single aggregated stack.
	seriesGroups := groupOrder
	otherActive := false
	if topN > 0 && len(groupOrder) > topN {
		seriesGroups = groupOrder[:topN]
		otherActive = true
	}
	for _, b := range bucketOrder {
		for _, g := range seriesGroups {
			if subgroup != "" {
				for _, sg := range subgroupOrder[g] {
					v := float64(0)
					if m, ok := subAgg[b][g]; ok {
						if a, ok := m[sg]; ok {
							v = accValue(metric, a)
						}
					}
					resp.Series = append(resp.Series, ActivitySeriesPoint{
						Bucket:   b,
						Group:    g,
						Subgroup: sg,
						Value:    v,
						IsZero:   v == 0,
					})
				}
				continue
			}
			v := float64(0)
			if m, ok := agg[b][g]; ok {
				v = accValue(metric, m)
			}
			resp.Series = append(resp.Series, ActivitySeriesPoint{
				Bucket: b,
				Group:  g,
				Value:  v,
				IsZero: v == 0,
			})
		}
		if otherActive {
			// Other folds the remaining groups' cells into one series. For
			// the blended rate the fold combines spend AND tokens first and
			// derives the rate at the end (a sum of rates is not a rate).
			var ov float64
			if metric == "blended" {
				var spend, tokens float64
				for _, g := range groupOrder[topN:] {
					if m, ok := agg[b][g]; ok {
						spend += m.spend
						tokens += m.tokens
					}
				}
				ov = blendedRate(spend, tokens)
			} else {
				for _, g := range groupOrder[topN:] {
					if m, ok := agg[b][g]; ok {
						ov += m.sum
					}
				}
			}
			resp.Series = append(resp.Series, ActivitySeriesPoint{
				Bucket: b,
				Group:  "Other",
				Value:  ov,
				IsZero: ov == 0,
			})
		}
	}

	// Summary: per group Min/Max/Avg/Sum/Value/Percent. Value = sum in the
	// LAST bucket (OpenRouter's "Value" column). Min/Max/Avg are computed
	// over the group's NON-EMPTY buckets only (OR semantics — an idle day is
	// not a $0 sample; a model that ran one day shows Min==Max==Avg==that
	// day's value). For the blended rate, Sum is the group's OVERALL rate
	// (total spend / total tokens), Min/Max/Avg span the per-bucket rates,
	// and Percent is the group's share of total spend (rates don't sum to a
	// meaningful total).
	groupTotals := make(map[string]float64)
	groupBucketCount := make(map[string]int)
	groupRateSum := make(map[string]float64)
	for _, g := range groupOrder {
		for _, b := range bucketOrder {
			if m, ok := agg[b][g]; ok {
				groupTotals[g] += m.sum
				groupBucketCount[g]++
				if metric == "blended" {
					groupRateSum[g] += accValue(metric, m)
				}
			}
		}
		if metric == "blended" {
			groupTotals[g] = blendedRate(groupSpend[g], groupTokens[g])
		}
	}
	totalSum := float64(0)
	totalSpend := float64(0)
	for _, v := range groupTotals {
		totalSum += v
	}
	for _, v := range groupSpend {
		totalSpend += v
	}
	for _, g := range groupOrder {
		avg := float64(0)
		if n := groupBucketCount[g]; n > 0 {
			if metric == "blended" {
				avg = groupRateSum[g] / float64(n)
			} else {
				avg = groupTotals[g] / float64(n)
			}
		}
		percent := float64(0)
		if metric == "blended" {
			if totalSpend > 0 {
				percent = groupSpend[g] / totalSpend * 100
			}
		} else if totalSum > 0 {
			percent = groupTotals[g] / totalSum * 100
		}
		var min, max float64
		first := true
		for _, b := range bucketOrder {
			if m, ok := agg[b][g]; ok {
				v := accValue(metric, m)
				if first {
					min, max = v, v
					first = false
				} else {
					if v < min {
						min = v
					}
					if v > max {
						max = v
					}
				}
			}
		}
		resp.Summary = append(resp.Summary, ActivityGroupSummary{
			Group:   g,
			Min:     min,
			Max:     max,
			Avg:     avg,
			Sum:     groupTotals[g],
			Value:   lastBucketValue(agg, bucketOrder, g, metric),
			Percent: percent,
		})
	}
	// Sort summary by the rank metric descending (OpenRouter's "Rank by"
	// control; "current" = the chart metric's sum).
	sort.Slice(resp.Summary, func(i, j int) bool {
		si, sj := rankTotals[resp.Summary[i].Group], rankTotals[resp.Summary[j].Group]
		if si != sj {
			return si > sj
		}
		return resp.Summary[i].Group < resp.Summary[j].Group
	})

	c.JSON(http.StatusOK, resp)
}

// lastBucketValue returns the group's value in the final bucket (or 0).
func lastBucketValue(agg map[string]map[string]*activityAcc, bucketOrder []string, g, metric string) float64 {
	if len(bucketOrder) == 0 {
		return 0
	}
	last := bucketOrder[len(bucketOrder)-1]
	if m, ok := agg[last]; ok {
		if a, ok := m[g]; ok {
			return accValue(metric, a)
		}
	}
	return 0
}

// mondayOf returns the Monday (week start, midnight) of t's week.
// Used for week rollup buckets: weeks are labeled by their start date.
func mondayOf(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	off := (int(d.Weekday()) + 6) % 7 // days since Monday (Sunday = 6)
	return d.AddDate(0, 0, -off)
}

// activityBucketLabel is the ONE place that maps a time to a rollup bucket
// label (year-qualified "YYYY-MM-DD", "YYYY-MM-DD 15:00", "2006-01-02",
// "YYYY-MM", or "Total" for the total rollup). Both the axis builder and the
// row aggregation use it, so a row can never be labeled off the axis and
// silently dropped.
func activityBucketLabel(t time.Time, rollup string) string {
	switch rollup {
	case "hour":
		return t.Format("2006-01-02 15:00")
	case "week":
		return mondayOf(t).Format("2006-01-02")
	case "month":
		return t.Format("2006-01")
	case "total":
		return "Total"
	default:
		return t.Format("2006-01-02")
	}
}

// activityWindow widens a query range to the rollup buckets CONTAINING its
// endpoints. hour_bucket rows are truncated to the LOCAL hour, so a window
// starting at 16:05 must still match the 16:00 bucket (it holds the
// 16:00–17:00 usage) — without the floor, short presets (15m/30m) return
// nothing for most of the hour. `from` is the bucket start of since; `to`
// is the first bucket start AFTER until, so the bucket containing until is
// complete. Matches buildActivityAxis (same floor and step), so the query
// and the response axis always agree.
func activityWindow(since, until time.Time, rollup string) (from, to time.Time) {
	loc := since.Location()
	switch rollup {
	case "hour", "total":
		// total aggregates the whole range into one bucket, so its window is
		// the same as hour: since..until widened to the LOCAL-hour bucket
		// boundaries of the endpoints (billing truncates hour_bucket to the
		// local hour). The month branch below would widen a mid-month range
		// to whole containing months and pull out-of-range usage into the
		// single Total bucket.
		from = time.Date(since.Year(), since.Month(), since.Day(), since.Hour(), 0, 0, 0, loc)
		to = time.Date(until.Year(), until.Month(), until.Day(), until.Hour(), 0, 0, 0, loc).Add(time.Hour)
	case "day":
		from = time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, loc)
		to = time.Date(until.Year(), until.Month(), until.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	case "week":
		from = mondayOf(since)
		to = mondayOf(until).AddDate(0, 0, 7)
	default: // month
		from = time.Date(since.Year(), since.Month(), 1, 0, 0, 0, 0, loc)
		to = time.Date(until.Year(), until.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, 1, 0)
	}
	return from, to
}

// buildActivityAxis builds a CONTINUOUS bucket axis over since..until at the
// requested rollup (hour|day|week|month|total), so days/hours without traffic
// still appear (OR renders the full range). Hour buckets are floored to the
// LOCAL hour (billing truncates hour_bucket to the local hour too — UTC
// Truncate would misalign in half-hour-offset zones like +05:30); day/week
// step calendar-wise (AddDate) so a 25-hour DST fall-back day neither
// duplicates a date label nor skips a week; the repeated wall-clock hour of
// a fall-back keeps a single bucket (both passes aggregate into it by
// label). The total rollup collapses everything into one "Total" bucket.
func buildActivityAxis(since, until time.Time, rollup string) []string {
	bucketOf := func(t time.Time) string {
		return activityBucketLabel(t, rollup)
	}

	var bucketOrder []string
	switch rollup {
	case "hour":
		start := time.Date(since.Year(), since.Month(), since.Day(), since.Hour(), 0, 0, 0, since.Location())
		for t := start; !t.After(until); t = t.Add(time.Hour) {
			if b := bucketOf(t); len(bucketOrder) == 0 || bucketOrder[len(bucketOrder)-1] != b {
				bucketOrder = append(bucketOrder, b)
			}
		}
	case "day":
		start := time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, since.Location())
		for t := start; !t.After(until); t = t.AddDate(0, 0, 1) {
			bucketOrder = append(bucketOrder, bucketOf(t))
		}
	case "week":
		for t := mondayOf(since); !t.After(until); t = t.AddDate(0, 0, 7) {
			bucketOrder = append(bucketOrder, bucketOf(t))
		}
	case "total":
		bucketOrder = append(bucketOrder, "Total")
	default: // month
		start := time.Date(since.Year(), since.Month(), 1, 0, 0, 0, 0, since.Location())
		for t := start; !t.After(until); t = t.AddDate(0, 1, 0) {
			bucketOrder = append(bucketOrder, bucketOf(t))
		}
	}
	return bucketOrder
}
