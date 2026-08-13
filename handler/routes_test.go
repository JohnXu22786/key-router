package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"key-router/db"
	"key-router/model"
)

// TestGetRoutesReturnsDragOrder: the Models page drags routes within a model
// group and re-fetches /routes on every 10s poll. Routes must come back
// grouped by model_group_id with per-group priority order preserved —
// otherwise each group's rows are not a contiguous slice of the array and
// the drag hook's target mapping (local row index -> global index) lands on
// another group's row, silently blocking every reorder. Regression test: a
// drag must survive a re-fetch with two groups whose priorities interleave.
func TestGetRoutesReturnsDragOrder(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	prov := model.Provider{Name: "A", Type: "openai", BaseURL: "http://a"}
	if err := db.GetDB().Create(&prov).Error; err != nil {
		t.Fatal(err)
	}
	g1 := model.ModelGroup{GroupID: "g1", Name: "G1", Enabled: true}
	g2 := model.ModelGroup{GroupID: "g2", Name: "G2", Enabled: true}
	for _, g := range []*model.ModelGroup{&g1, &g2} {
		if err := db.GetDB().Create(g).Error; err != nil {
			t.Fatal(err)
		}
	}
	r1 := model.Route{ModelGroupID: g1.ID, ProviderID: prov.ID, TargetModel: "g1r1"}
	r2 := model.Route{ModelGroupID: g1.ID, ProviderID: prov.ID, TargetModel: "g1r2"}
	r3 := model.Route{ModelGroupID: g2.ID, ProviderID: prov.ID, TargetModel: "g2r1"}
	for _, r := range []*model.Route{&r1, &r2, &r3} {
		if err := db.GetDB().Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}

	// Simulate the frontend drag commit: r2 moved above r1 within g1.
	payload, _ := json.Marshal(map[string]any{"routes": []map[string]any{
		{"id": r2.ID, "priority": 0},
		{"id": r1.ID, "priority": 1},
		{"id": r3.ID, "priority": 0},
	}})
	req := httptest.NewRequest("POST", "/api/routes/reorder", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/routes/reorder status = %d: %s", rec.Code, rec.Body.String())
	}

	// The re-fetch must keep each group's rows contiguous, in dragged order.
	req = httptest.NewRequest("GET", "/api/routes", nil)
	req.Host = "localhost:9999"
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/routes status = %d: %s", rec.Code, rec.Body.String())
	}
	var out []struct {
		TargetModel string `json:"target_model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec.Body.String())
	}
	got := make([]string, 0, len(out))
	for _, r := range out {
		got = append(got, r.TargetModel)
	}
	want := []string{"g1r2", "g1r1", "g2r1"}
	if !slices.Equal(got, want) {
		t.Fatalf("GET /api/routes order = %v, want %v (groups must stay contiguous)", got, want)
	}
}
