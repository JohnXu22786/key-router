package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"key-router/db"
	"key-router/model"
)

// TestCreateModelGroupDuplicateGroupIDRejected: creating a group whose
// group_id is already taken must be a user-actionable 400, not a raw SQLite
// UNIQUE constraint 500. Regression test for the model-group variant of the
// duplicate-resource bug (CreateRoute/UpdatePricing already guard theirs).
func TestCreateModelGroupDuplicateGroupIDRejected(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	first := `{"group_id":"gpt-4o","name":"First"}`
	req := httptest.NewRequest("POST", "/api/model-groups", strings.NewReader(first))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first POST /api/model-groups status = %d: %s", rec.Code, rec.Body.String())
	}

	dup := `{"group_id":"gpt-4o","name":"Second"}`
	req = httptest.NewRequest("POST", "/api/model-groups", strings.NewReader(dup))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:9999"
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate POST /api/model-groups status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "UNIQUE constraint") {
		t.Errorf("duplicate error must be actionable, not a raw SQLite message: %s", rec.Body.String())
	}
}

// TestUpdateModelGroupDuplicateGroupIDRejected: renaming a group's group_id
// to one another group already uses must be a 400, not a raw SQLite UNIQUE
// constraint 500. Regression test: the PUT path excludes the group being
// edited itself so its own unchanged group_id is not a false positive.
func TestUpdateModelGroupDuplicateGroupIDRejected(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	create := func(groupID string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/model-groups",
			strings.NewReader(`{"group_id":"`+groupID+`","name":"`+groupID+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Host = "localhost:9999"
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	rec := create("gpt-4o")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create gpt-4o status = %d: %s", rec.Code, rec.Body.String())
	}
	var a struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}

	rec = create("claude-3-5")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create claude-3-5 status = %d: %s", rec.Code, rec.Body.String())
	}

	// Rename group A to the group_id group B already owns.
	req := httptest.NewRequest("PUT", "/api/model-groups/"+strconv.FormatInt(a.ID, 10),
		strings.NewReader(`{"group_id":"claude-3-5","name":"Renamed"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:9999"
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate PUT /api/model-groups/%d status = %d, want 400 (body: %s)", a.ID, rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "UNIQUE constraint") {
		t.Errorf("duplicate error must be actionable, not a raw SQLite message: %s", rec.Body.String())
	}
}

// TestUpdateModelGroupKeepsOwnGroupID: saving a group without changing its
// group_id must succeed — the update duplicate check must exclude the row
// being edited, or every GET->PUT round-trip would 400.
func TestUpdateModelGroupKeepsOwnGroupID(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	req := httptest.NewRequest("POST", "/api/model-groups",
		strings.NewReader(`{"group_id":"gpt-4o","name":"First"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/model-groups status = %d: %s", rec.Code, rec.Body.String())
	}
	var g struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}

	// Round-trip the same group_id back through PUT.
	req = httptest.NewRequest("PUT", "/api/model-groups/"+strconv.FormatInt(g.ID, 10),
		strings.NewReader(`{"group_id":"gpt-4o","name":"First","enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:9999"
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/model-groups/%d keeping its own group_id status = %d, want 200 (body: %s)", g.ID, rec.Code, rec.Body.String())
	}
}

func TestUpdateModelGroupRejectsEmptyGroupIDWithoutChangingRoutes(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	create := httptest.NewRequest("POST", "/api/model-groups",
		strings.NewReader(`{"group_id":"gpt-4o","name":"Original","enabled":true}`))
	create.Header.Set("Content-Type", "application/json")
	create.Host = "localhost:9999"
	created := httptest.NewRecorder()
	e.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("POST /api/model-groups status = %d: %s", created.Code, created.Body.String())
	}
	var group struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &group); err != nil {
		t.Fatalf("unmarshal created model group: %v", err)
	}

	provider := model.Provider{Name: "Test", Type: "openai", BaseURL: "http://test"}
	if err := db.GetDB().Create(&provider).Error; err != nil {
		t.Fatal(err)
	}
	route := model.Route{ModelGroupID: group.ID, ProviderID: provider.ID, TargetModel: "gpt-4o"}
	if err := db.GetDB().Create(&route).Error; err != nil {
		t.Fatal(err)
	}

	update := httptest.NewRequest("PUT", "/api/model-groups/"+strconv.FormatInt(group.ID, 10),
		strings.NewReader(`{"group_id":"","name":"Changed","enabled":false}`))
	update.Header.Set("Content-Type", "application/json")
	update.Host = "localhost:9999"
	updated := httptest.NewRecorder()
	e.ServeHTTP(updated, update)
	if updated.Code != http.StatusBadRequest {
		t.Fatalf("PUT /api/model-groups/%d with empty group_id status = %d, want 400 (body: %s)", group.ID, updated.Code, updated.Body.String())
	}

	getGroups := httptest.NewRequest("GET", "/api/model-groups", nil)
	getGroups.Host = "localhost:9999"
	groupsRec := httptest.NewRecorder()
	e.ServeHTTP(groupsRec, getGroups)
	if groupsRec.Code != http.StatusOK {
		t.Fatalf("GET /api/model-groups status = %d: %s", groupsRec.Code, groupsRec.Body.String())
	}
	var groups []struct {
		ID      int64  `json:"id"`
		GroupID string `json:"group_id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(groupsRec.Body.Bytes(), &groups); err != nil {
		t.Fatalf("unmarshal model groups: %v", err)
	}
	var found bool
	for _, got := range groups {
		if got.ID == group.ID {
			found = true
			if got.GroupID != "gpt-4o" || got.Name != "Original" || !got.Enabled {
				t.Errorf("model group after rejected update = %+v, want original group_id, name, and enabled state", got)
			}
		}
	}
	if !found {
		t.Fatalf("model group %d missing after rejected update", group.ID)
	}

	getRoutes := httptest.NewRequest("GET", "/api/routes?model_group_id="+strconv.FormatInt(group.ID, 10), nil)
	getRoutes.Host = "localhost:9999"
	routesRec := httptest.NewRecorder()
	e.ServeHTTP(routesRec, getRoutes)
	if routesRec.Code != http.StatusOK {
		t.Fatalf("GET /api/routes status = %d: %s", routesRec.Code, routesRec.Body.String())
	}
	var routes []struct {
		ID           int64  `json:"id"`
		ModelGroupID int64  `json:"model_group_id"`
		TargetModel  string `json:"target_model"`
	}
	if err := json.Unmarshal(routesRec.Body.Bytes(), &routes); err != nil {
		t.Fatalf("unmarshal routes: %v", err)
	}
	if len(routes) != 1 || routes[0].ID != route.ID || routes[0].ModelGroupID != group.ID || routes[0].TargetModel != "gpt-4o" {
		t.Errorf("routes after rejected update = %+v, want the original route attached to model group %d", routes, group.ID)
	}
}
