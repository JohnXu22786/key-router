package handler_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"key-router/db"
	"key-router/model"
)

func TestUpdatePricingRejectsEmptyModelNameWithoutChangingPricing(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	want := model.Pricing{
		ModelName:       "original-model",
		PromptPer1M:     1.25,
		CompletionPer1M: 2.5,
		CacheReadPer1M:  0.75,
		CacheWritePer1M: 3.5,
	}
	if err := db.GetDB().Create(&want).Error; err != nil {
		t.Fatalf("create pricing: %v", err)
	}

	req := httptest.NewRequest("PUT", "/api/pricings/"+strconv.FormatInt(want.ID, 10),
		strings.NewReader(`{"model_name":"","prompt_per_1m":10,"completion_per_1m":20,"cache_read_per_1m":30,"cache_write_per_1m":40}`))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT /api/pricings/%d with empty model_name status = %d, want 400 (body: %s)", want.ID, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"model_name is required"`) {
		t.Errorf("empty model_name error = %s, want required-field response", rec.Body.String())
	}

	var got model.Pricing
	if err := db.GetDB().First(&got, want.ID).Error; err != nil {
		t.Fatalf("load pricing after rejected update: %v", err)
	}
	if got.ModelName != want.ModelName || got.PromptPer1M != want.PromptPer1M ||
		got.CompletionPer1M != want.CompletionPer1M || got.CacheReadPer1M != want.CacheReadPer1M ||
		got.CacheWritePer1M != want.CacheWritePer1M {
		t.Errorf("pricing after rejected update = %+v, want original name and rates %+v", got, want)
	}
}
