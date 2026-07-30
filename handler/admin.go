package handler

import (
	"net/http"
	"strconv"
	"time"

	"local-router/billing"
	"local-router/db"
	"local-router/health"
	"local-router/model"
	"local-router/selector"

	"github.com/gin-gonic/gin"
)

// AdminHandler handles management API endpoints
type AdminHandler struct {
	Engine       *selector.Engine
	HealthChecker *health.Checker
}

// NewAdminHandler creates a new admin handler
func NewAdminHandler(engine *selector.Engine, checker *health.Checker) *AdminHandler {
	return &AdminHandler{
		Engine:       engine,
		HealthChecker: checker,
	}
}

// Health returns service status
func (h *AdminHandler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"version": "0.1.0",
		"time":    time.Now().Unix(),
	})
}

// GetProviders returns all providers
func (h *AdminHandler) GetProviders(c *gin.Context) {
	var providers []model.Provider
	db.GetDB().Find(&providers)
	c.JSON(http.StatusOK, providers)
}

// CreateProvider creates a new provider
func (h *AdminHandler) CreateProvider(c *gin.Context) {
	var p model.Provider
	if err := c.ShouldBindJSON(&p); err != nil {
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
	db.GetDB().Save(&p)
	h.Engine.Refresh()
	c.JSON(http.StatusOK, p)
}

// DeleteProvider deletes a provider
func (h *AdminHandler) DeleteProvider(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	db.GetDB().Delete(&model.Provider{}, id)
	db.GetDB().Where("provider_id = ?", id).Delete(&model.Key{})
	db.GetDB().Where("provider_id = ?", id).Delete(&model.Route{})
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
	query.Find(&keys)

	// Add window counts
	type KeyWithCounts struct {
		model.Key
		Counts map[string]struct{ Count, TokenCount int64 } `json:"counts"`
	}
	result := make([]KeyWithCounts, 0, len(keys))
	for _, k := range keys {
		kwc := KeyWithCounts{Key: k}
		kwc.Counts = make(map[string]struct{ Count, TokenCount int64 })
		for _, wt := range []model.WindowType{
			model.WindowRPM, model.WindowTPM, model.WindowRP5h,
			model.WindowRPD, model.WindowRPW, model.WindowRPMo,
		} {
			kwc.Counts[string(wt)] = struct{ Count, TokenCount int64 }{
				Count:      h.Engine.WindowManager.GetCount(k.ID, wt),
				TokenCount: h.Engine.WindowManager.GetTokens(k.ID, wt),
			}
		}
		result = append(result, kwc)
	}

	c.JSON(http.StatusOK, result)
}

// CreateKey creates a new key
func (h *AdminHandler) CreateKey(c *gin.Context) {
	var k model.Key
	if err := c.ShouldBindJSON(&k); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := db.GetDB().Create(&k).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.Engine.Refresh()
	c.JSON(http.StatusCreated, k)
}

// UpdateKey updates a key
func (h *AdminHandler) UpdateKey(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var k model.Key
	if err := db.GetDB().First(&k, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "key not found"})
		return
	}
	if err := c.ShouldBindJSON(&k); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	k.ID = id
	db.GetDB().Save(&k)
	h.Engine.Refresh()
	c.JSON(http.StatusOK, k)
}

// DeleteKey deletes a key
func (h *AdminHandler) DeleteKey(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	db.GetDB().Delete(&model.Key{}, id)
	h.Engine.Refresh()
	c.JSON(http.StatusNoContent, nil)
}

// GetModelGroups returns all model groups
func (h *AdminHandler) GetModelGroups(c *gin.Context) {
	var groups []model.ModelGroup
	db.GetDB().Find(&groups)
	c.JSON(http.StatusOK, groups)
}

// CreateModelGroup creates a new model group
func (h *AdminHandler) CreateModelGroup(c *gin.Context) {
	var mg model.ModelGroup
	if err := c.ShouldBindJSON(&mg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
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
	db.GetDB().Save(&mg)
	h.Engine.Refresh()
	c.JSON(http.StatusOK, mg)
}

// DeleteModelGroup deletes a model group
func (h *AdminHandler) DeleteModelGroup(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	db.GetDB().Delete(&model.ModelGroup{}, id)
	db.GetDB().Where("model_group_id = ?", id).Delete(&model.Route{})
	h.Engine.Refresh()
	c.JSON(http.StatusNoContent, nil)
}

// GetRoutes returns all routes
func (h *AdminHandler) GetRoutes(c *gin.Context) {
	var routes []model.Route
	query := db.GetDB().Preload("ModelGroup").Preload("Provider")
	if groupID := c.Query("model_group_id"); groupID != "" {
		query = query.Where("model_group_id = ?", groupID)
	}
	query.Find(&routes)
	c.JSON(http.StatusOK, routes)
}

// CreateRoute creates a new route
func (h *AdminHandler) CreateRoute(c *gin.Context) {
	var r model.Route
	if err := c.ShouldBindJSON(&r); err != nil {
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
	db.GetDB().Save(&r)
	h.Engine.Refresh()
	c.JSON(http.StatusOK, r)
}

// DeleteRoute deletes a route
func (h *AdminHandler) DeleteRoute(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	db.GetDB().Delete(&model.Route{}, id)
	h.Engine.Refresh()
	c.JSON(http.StatusNoContent, nil)
}

// GetPricings returns all pricing rules
func (h *AdminHandler) GetPricings(c *gin.Context) {
	var pricings []model.Pricing
	db.GetDB().Find(&pricings)
	c.JSON(http.StatusOK, pricings)
}

// CreatePricing creates a new pricing rule
func (h *AdminHandler) CreatePricing(c *gin.Context) {
	var p model.Pricing
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
	db.GetDB().Save(&p)
	h.Engine.Calculator.RefreshPricing()
	c.JSON(http.StatusOK, p)
}

// DeletePricing deletes a pricing rule
func (h *AdminHandler) DeletePricing(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	db.GetDB().Delete(&model.Pricing{}, id)
	h.Engine.Calculator.RefreshPricing()
	c.JSON(http.StatusNoContent, nil)
}

// GetSettings returns all settings
func (h *AdminHandler) GetSettings(c *gin.Context) {
	var settings []model.Setting
	db.GetDB().Find(&settings)
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
	for k, v := range settings {
		db.SetSetting(k, v)
	}
	// Restart health checker if interval changed
	if _, ok := settings[model.SettingHealthCheck]; ok {
		h.HealthChecker.Restart()
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

	// Get recent consumption
	since := time.Now().Add(-24 * time.Hour)
	consumptions, _ := billing.GetConsumptionSummary(id, since, time.Now())

	totalCost, _ := billing.GetTotalCost(id)

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
	query := db.GetDB().Preload("Key")

	if keyID := c.Query("key_id"); keyID != "" {
		query = query.Where("key_id = ?", keyID)
	}
	if since := c.Query("since"); since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			query = query.Where("hour_bucket >= ?", t)
		}
	}
	if until := c.Query("until"); until != "" {
		if t, err := time.Parse(time.RFC3339, until); err == nil {
			query = query.Where("hour_bucket <= ?", t)
		}
	}

	query.Order("hour_bucket DESC").Limit(100).Find(&consumptions)
	c.JSON(http.StatusOK, consumptions)
}

// GetKeyStatuses returns health status of all keys
func (h *AdminHandler) GetKeyStatuses(c *gin.Context) {
	results := health.GetKeyStatuses()
	c.JSON(http.StatusOK, results)
}

// ReloadConfig reloads routing and pricing data
func (h *AdminHandler) ReloadConfig(c *gin.Context) {
	h.Engine.Refresh()
	h.Engine.Calculator.RefreshPricing()
	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "configuration reloaded"})
}
