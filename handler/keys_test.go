package handler_test

import (
	"embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"key-router/db"
	"key-router/events"
	"key-router/health"
	"key-router/model"
	"key-router/router"
	"key-router/selector"

	"github.com/gin-gonic/gin"
)

// bootstrapKeys sets up the app and returns the router for API-level
// assertions about key ordering (the Providers page renders keys in the
// order GET /api/keys returns and re-fetches it every 10s).
func bootstrapKeys(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "data")); err != nil {
		t.Fatal(err)
	}
	db.SetSetting(model.SettingPort, "9999")
	engine := selector.NewEngine()
	checker := health.NewChecker()
	return router.Setup(embed.FS{}, engine, checker, events.NewHub())
}

func closeTestDB(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})
}

func getNames(t *testing.T, e *gin.Engine) []string {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/keys", nil)
	req.Host = "localhost:9999" // LocalOnlyMiddleware requires localhost
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/keys status = %d: %s", rec.Code, rec.Body.String())
	}
	var out []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec.Body.String())
	}
	names := make([]string, 0, len(out))
	for _, k := range out {
		names = append(names, k.Name)
	}
	return names
}

// TestGetKeysReturnsDragOrder: the Providers page drags keys within a
// provider, commits via /keys/reorder, then re-fetches /keys on every 10s
// poll and page load. If the endpoint does not return keys in
// (provider_id, sort_order) order, the UI order "snaps back" a few seconds
// after a drag. Regression test: a drag must survive a re-fetch.
func TestGetKeysReturnsDragOrder(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	provA := model.Provider{Name: "A", Type: "openai", BaseURL: "http://a"}
	provB := model.Provider{Name: "B", Type: "openai", BaseURL: "http://b"}
	if err := db.GetDB().Create(&provA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Create(&provB).Error; err != nil {
		t.Fatal(err)
	}
	k1 := model.Key{ProviderID: provA.ID, Name: "a1", KeyValue: "ka1"}
	k2 := model.Key{ProviderID: provA.ID, Name: "a2", KeyValue: "ka2"}
	k3 := model.Key{ProviderID: provB.ID, Name: "b1", KeyValue: "kb1"}
	for _, k := range []*model.Key{&k1, &k2, &k3} {
		if err := db.GetDB().Create(k).Error; err != nil {
			t.Fatal(err)
		}
	}

	// Simulate the frontend drag commit: a2 moved above a1 within provider A.
	payload, _ := json.Marshal(map[string]any{"keys": []map[string]any{
		{"id": k2.ID, "sort_order": 0},
		{"id": k1.ID, "sort_order": 1},
		{"id": k3.ID, "sort_order": 0},
	}})
	req := httptest.NewRequest("POST", "/api/keys/reorder", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/keys/reorder status = %d: %s", rec.Code, rec.Body.String())
	}

	// The re-fetch must preserve the dragged order (per provider).
	got := getNames(t, e)
	want := []string{"a2", "a1", "b1"}
	if !slices.Equal(got, want) {
		t.Fatalf("GET /api/keys order = %v, want %v (drag order must survive re-fetch)", got, want)
	}
}

// TestCreateKeyAppendsAtEndOfProvider: a brand-new key must land at the END
// of its provider's call order, not tie at sort_order 0 (a fresh 0 would
// interleave to the top by id and silently reorder existing keys).
func TestCreateKeyAppendsAtEndOfProvider(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	prov := model.Provider{Name: "A", Type: "openai", BaseURL: "http://a"}
	if err := db.GetDB().Create(&prov).Error; err != nil {
		t.Fatal(err)
	}
	k1 := model.Key{ProviderID: prov.ID, Name: "a1", KeyValue: "ka1"}
	k2 := model.Key{ProviderID: prov.ID, Name: "a2", KeyValue: "ka2"}
	for _, k := range []*model.Key{&k1, &k2} {
		if err := db.GetDB().Create(k).Error; err != nil {
			t.Fatal(err)
		}
	}

	// Drag so the current order is a2 (0), a1 (1).
	payload, _ := json.Marshal(map[string]any{"keys": []map[string]any{
		{"id": k2.ID, "sort_order": 0},
		{"id": k1.ID, "sort_order": 1},
	}})
	req := httptest.NewRequest("POST", "/api/keys/reorder", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/keys/reorder status = %d: %s", rec.Code, rec.Body.String())
	}

	// Create a new key through the API.
	newKey := `{"provider_id":` + jsonInt(prov.ID) + `,"name":"a3","key_value":"ka3"}`
	req = httptest.NewRequest("POST", "/api/keys", strings.NewReader(newKey))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:9999"
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/keys status = %d: %s", rec.Code, rec.Body.String())
	}

	got := getNames(t, e)
	want := []string{"a2", "a1", "a3"}
	if !slices.Equal(got, want) {
		t.Fatalf("GET /api/keys order = %v, want %v (new key must append at the end)", got, want)
	}
}

func jsonInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
