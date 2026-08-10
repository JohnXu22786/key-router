package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"key-router/billing"
	"key-router/db"
	"key-router/health"
	"key-router/model"
	"key-router/selector"

	"github.com/gin-gonic/gin"
)

// version is injected at build time via -ldflags "-X main.version=..." — the
// handler package mirrors the value so /api/health reports the release version.
var version = "0.1.0"

// AdminHandler handles management API endpoints
type AdminHandler struct {
	Engine        *selector.Engine
	HealthChecker *health.Checker
}

// NewAdminHandler creates a new admin handler
func NewAdminHandler(engine *selector.Engine, checker *health.Checker) *AdminHandler {
	return &AdminHandler{
		Engine:        engine,
		HealthChecker: checker,
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
	// Only HTTP(S) makes sense for an API gateway; a typo'd scheme (ftp://…)
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

	// Drop window state for the deleted keys so memory/windows.json don't grow
	for _, kid := range keyIDs {
		h.Engine.WindowManager.Remove(kid)
	}
	h.Engine.Refresh()
	c.JSON(http.StatusNoContent, nil)
}

// GetKeys returns all keys, optionally filtered by provider
func (h *AdminHandler) GetKeys(c *gin.Context) {
	var keys []model.Key
	query := db.GetDB().Preload("Provider")
	if providerID := c.Query("provider_id"); providerID != "" {
		query = query.Where("provider_id = ?", providerID)
	}
	if err := query.Find(&keys).Error; err != nil {
		log.Printf("[admin] GetKeys error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load keys"})
		return
	}

	// Add window counts
	type KeyWithCounts struct {
		model.Key
		Counts map[string]struct {
			Count      int64 `json:"count"`
			TokenCount int64 `json:"token_count"`
		} `json:"counts"`
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
	if err := db.GetDB().Create(&k).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// A fresh key must not inherit a backoff latch or rate-limit window
	// state from a deleted key whose SQLite rowid was reused
	h.HealthChecker.ResetFailCount(k.ID)
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

	// Status/cooldown/reason/strategy are owned by the relay and health
	// checker — the edit form doesn't send them, and a stale page-load
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
	// must clear a stale auto-recovery reason: the health checker only
	// auto-recovers keys whose disabled_reason is "auth_failed". This applies
	// even when the key is already disabled (re-affirming the disable).
	if statusInPayload && k.Status == model.KeyStatusDisabled {
		k.DisabledReason = ""
	}
	if err := db.GetDB().Save(&k).Error; err != nil {
		log.Printf("[admin] UpdateKey save error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// A key edit (e.g. fixing the key_value of an auth_failed key) must
	// resume health-check probing — clear the consecutive-failure latch.
	h.HealthChecker.ResetFailCount(id)
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
	// Clear any health-checker backoff latch for the deleted id (rowid reuse)
	h.HealthChecker.ResetFailCount(id)
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

// GetRoutes returns all routes, ordered by priority (drag position)
func (h *AdminHandler) GetRoutes(c *gin.Context) {
	var routes []model.Route
	query := db.GetDB().Preload("ModelGroup").Preload("Provider").Order("priority ASC")
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
	if since := c.Query("since"); since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			query = query.Where("hour_bucket >= ?", t.Local())
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

	// Generous cap: 24h × 7d = 168 rows per key, so this covers hundreds of
	// keys without truncating the Stats page charts.
	if err := query.Order("hour_bucket DESC").Limit(100000).Find(&consumptions).Error; err != nil {
		log.Printf("[admin] GetStatsConsumptions error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load consumptions"})
		return
	}
	c.JSON(http.StatusOK, consumptions)
}

// GetKeyStatuses returns health status of all keys
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
