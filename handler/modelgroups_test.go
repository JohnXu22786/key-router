package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
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
