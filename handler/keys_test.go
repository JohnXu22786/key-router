package handler_test

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"key-router/db"
	"key-router/events"
	"key-router/health"
	"key-router/model"
	"key-router/router"
	"key-router/selector"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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

type delayedBody struct {
	reader  *bytes.Reader
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (r *delayedBody) Read(p []byte) (int, error) {
	r.once.Do(func() {
		close(r.started)
		<-r.release
	})
	return r.reader.Read(p)
}

func (r *delayedBody) Close() error { return nil }

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

func TestReorderKeysSerializesOverlappingRequests(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	provider := model.Provider{Name: "A", Type: "openai", BaseURL: "http://a"}
	if err := db.GetDB().Create(&provider).Error; err != nil {
		t.Fatal(err)
	}
	keys := []*model.Key{
		{ProviderID: provider.ID, Name: "a", KeyValue: "ka"},
		{ProviderID: provider.ID, Name: "b", KeyValue: "kb"},
		{ProviderID: provider.ID, Name: "c", KeyValue: "kc"},
	}
	for _, key := range keys {
		if err := db.GetDB().Create(key).Error; err != nil {
			t.Fatal(err)
		}
	}
	makePayload := func(order []*model.Key) []byte {
		rows := make([]map[string]any, 0, len(order))
		for sortOrder, key := range order {
			rows = append(rows, map[string]any{"id": key.ID, "sort_order": sortOrder})
		}
		payload, err := json.Marshal(map[string]any{"keys": rows})
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}

	olderRelease := make(chan struct{})
	olderStarted := make(chan struct{})
	olderBody := &delayedBody{
		reader:  bytes.NewReader(makePayload([]*model.Key{keys[1], keys[2], keys[0]})),
		started: olderStarted,
		release: olderRelease,
	}
	olderRequest := httptest.NewRequest("POST", "/api/keys/reorder", olderBody)
	olderRequest.Header.Set("Content-Type", "application/json")
	olderRequest.Host = "localhost:9999"
	olderResponse := httptest.NewRecorder()
	olderDone := make(chan struct{})
	go func() {
		e.ServeHTTP(olderResponse, olderRequest)
		close(olderDone)
	}()
	select {
	case <-olderStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("older request did not begin reading its body")
	}

	newerRelease := make(chan struct{})
	newerStarted := make(chan struct{})
	newerBody := &delayedBody{
		reader:  bytes.NewReader(makePayload([]*model.Key{keys[2], keys[0], keys[1]})),
		started: newerStarted,
		release: newerRelease,
	}
	newerRequest := httptest.NewRequest("POST", "/api/keys/reorder", newerBody)
	newerRequest.Header.Set("Content-Type", "application/json")
	newerRequest.Host = "localhost:9999"
	newerResponse := httptest.NewRecorder()
	newerDone := make(chan struct{})
	go func() {
		e.ServeHTTP(newerResponse, newerRequest)
		close(newerDone)
	}()

	select {
	case <-newerStarted:
		close(newerRelease)
		<-newerDone
		close(olderRelease)
		<-olderDone
		t.Fatal("newer request read its body while the older reorder was still in flight")
	case <-time.After(250 * time.Millisecond):
	}

	close(olderRelease)
	<-olderDone
	select {
	case <-newerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("newer request did not resume after the older reorder completed")
	}
	close(newerRelease)
	<-newerDone

	if olderResponse.Code != http.StatusOK || newerResponse.Code != http.StatusOK {
		t.Fatalf("reorder statuses = %d, %d; want 200, 200", olderResponse.Code, newerResponse.Code)
	}
	got := getNames(t, e)
	want := []string{"c", "a", "b"}
	if !slices.Equal(got, want) {
		t.Fatalf("GET /api/keys order = %v, want the later reorder %v", got, want)
	}
}

func TestReorderKeysCancelsWhenWaitingForDatabase(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	provider := model.Provider{Name: "A", Type: "openai", BaseURL: "http://a"}
	if err := db.GetDB().Create(&provider).Error; err != nil {
		t.Fatal(err)
	}
	keys := []*model.Key{
		{ProviderID: provider.ID, Name: "a", KeyValue: "ka"},
		{ProviderID: provider.ID, Name: "b", KeyValue: "kb"},
	}
	for _, key := range keys {
		if err := db.GetDB().Create(key).Error; err != nil {
			t.Fatal(err)
		}
	}

	blocker := db.GetDB().Begin()
	if blocker.Error != nil {
		t.Fatal(blocker.Error)
	}
	defer blocker.Rollback()
	sqlDB, err := db.GetDB().DB()
	if err != nil {
		t.Fatal(err)
	}
	waitCount := sqlDB.Stats().WaitCount
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	payload, err := json.Marshal(map[string]any{"keys": []map[string]any{
		{"id": keys[1].ID, "sort_order": 0},
		{"id": keys[0].ID, "sort_order": 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/keys/reorder", strings.NewReader(string(payload))).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Host = "localhost:9999"
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		e.ServeHTTP(response, request)
		close(done)
	}()

	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for sqlDB.Stats().WaitCount == waitCount {
		select {
		case <-done:
			t.Fatal("reorder returned before waiting for the occupied DB connection")
		case <-deadline:
			t.Fatal("reorder did not wait for the occupied DB connection")
		case <-ticker.C:
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled reorder remained blocked on the DB connection")
	}
	blocker.Rollback()

	got := getNames(t, e)
	want := []string{"a", "b"}
	if !slices.Equal(got, want) {
		t.Fatalf("GET /api/keys order = %v, canceled reorder must not commit", got)
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

func TestUpdateKeyRejectsProviderMoveToDuplicateName(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	source := model.Provider{Name: "source", Type: "openai", BaseURL: "http://source"}
	destination := model.Provider{Name: "destination", Type: "openai", BaseURL: "http://destination"}
	for _, provider := range []*model.Provider{&source, &destination} {
		if err := db.GetDB().Create(provider).Error; err != nil {
			t.Fatal(err)
		}
	}

	moving := model.Key{ProviderID: source.ID, Name: "shared", KeyValue: "moving-key"}
	existing := model.Key{ProviderID: destination.ID, Name: "shared", KeyValue: "destination-key"}
	for _, key := range []*model.Key{&moving, &existing} {
		if err := db.GetDB().Create(key).Error; err != nil {
			t.Fatal(err)
		}
	}

	var before []model.Key
	if err := db.GetDB().Order("id").Find(&before).Error; err != nil {
		t.Fatal(err)
	}

	payload := `{"provider_id":` + jsonInt(destination.ID) + `}`
	req := httptest.NewRequest("PUT", "/api/keys/"+strconv.FormatInt(moving.ID, 10), strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT /api/keys/%d status = %d, want 400: %s", moving.ID, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "a key with this name already exists for the provider") {
		t.Fatalf("PUT /api/keys/%d error = %s, want duplicate-name rejection", moving.ID, rec.Body.String())
	}

	var after []model.Key
	if err := db.GetDB().Order("id").Find(&after).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("key rows changed after rejected move:\nbefore: %+v\nafter:  %+v", before, after)
	}
}

// TestUpdateKeyPreservesExplicitDisabledReason: an admin who disables a key
// WITH a justification (PUT {"status":"disabled","disabled_reason":"..."})
// must have that reason persisted. The explicit-status wipe of DisabledReason
// must not clobber a reason the caller just supplied in the same payload —
// the payload-honoring convention (a non-null disabled_reason survives
// binding) must win over the stale-reason cleanup, or the admin's recorded
// justification is silently lost.
func TestUpdateKeyPreservesExplicitDisabledReason(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	prov := model.Provider{Name: "A", Type: "openai", BaseURL: "http://a"}
	if err := db.GetDB().Create(&prov).Error; err != nil {
		t.Fatal(err)
	}
	k := model.Key{ProviderID: prov.ID, Name: "a1", KeyValue: "ka1", Status: model.KeyStatusActive}
	if err := db.GetDB().Create(&k).Error; err != nil {
		t.Fatal(err)
	}

	payload := `{"status":"disabled","disabled_reason":"Suspended due to abuse"}`
	req := httptest.NewRequest("PUT", "/api/keys/"+strconv.FormatInt(k.ID, 10), strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/keys/%d status = %d: %s", k.ID, rec.Code, rec.Body.String())
	}

	var after model.Key
	if err := db.GetDB().First(&after, k.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != model.KeyStatusDisabled {
		t.Errorf("status = %q, want disabled", after.Status)
	}
	if after.DisabledReason != "Suspended due to abuse" {
		t.Errorf("disabled_reason = %q, want %q (caller-supplied reason must survive the disable)", after.DisabledReason, "Suspended due to abuse")
	}
}

// TestUpdateKeyClearsStaleAutoRecoveryReasonOnDisable: a bare
// PUT {"status":"disabled"} (no disabled_reason) on a key auto-disabled by
// the relay (reason "auth_failed") must clear the stale reason. A non-empty
// system reason would let the engine auto-recover an admin's deliberately
// disabled key, so the cleanup only yields when the caller did NOT provide a
// reason of their own.
func TestUpdateKeyClearsStaleAutoRecoveryReasonOnDisable(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	prov := model.Provider{Name: "A", Type: "openai", BaseURL: "http://a"}
	if err := db.GetDB().Create(&prov).Error; err != nil {
		t.Fatal(err)
	}
	k := model.Key{
		ProviderID:     prov.ID,
		Name:           "a1",
		KeyValue:       "ka1",
		Status:         model.KeyStatusDisabled,
		DisabledReason: model.ReasonAuthFailed,
	}
	if err := db.GetDB().Create(&k).Error; err != nil {
		t.Fatal(err)
	}

	payload := `{"status":"disabled"}`
	req := httptest.NewRequest("PUT", "/api/keys/"+strconv.FormatInt(k.ID, 10), strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/keys/%d status = %d: %s", k.ID, rec.Code, rec.Body.String())
	}

	var after model.Key
	if err := db.GetDB().First(&after, k.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.DisabledReason != "" {
		t.Errorf("disabled_reason = %q, want cleared (a bare disable must not inherit the stale auth_failed reason)", after.DisabledReason)
	}
}

func TestUpdateKeyNameEditPreservesConcurrentRelayAndSpendChanges(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	prov := model.Provider{Name: "A", Type: "openai", BaseURL: "http://a"}
	if err := db.GetDB().Create(&prov).Error; err != nil {
		t.Fatal(err)
	}
	k := model.Key{
		ProviderID:       prov.ID,
		Name:             "before",
		KeyValue:         "ka1",
		Status:           model.KeyStatusActive,
		RecoveryStrategy: model.RecoveryLazy,
	}
	if err := db.GetDB().Create(&k).Error; err != nil {
		t.Fatal(err)
	}

	const callbackName = "test:update-key-concurrent-change"
	concurrentCooldown := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	const concurrentSpend int64 = 123456
	concurrentWriteRan := false
	callback := db.GetDB().Callback().Update().Before("gorm:update")
	if err := callback.Register(callbackName, func(tx *gorm.DB) {
		if concurrentWriteRan || tx.Statement.Table != "keys" {
			return
		}
		result := tx.Exec(
			"UPDATE keys SET total_spent = ?, status = ?, rate_limited_until = ?, disabled_reason = ?, recovery_strategy = ? WHERE id = ?",
			concurrentSpend,
			model.KeyStatusDisabled,
			concurrentCooldown,
			model.ReasonAuthFailed,
			model.RecoveryImmediate,
			k.ID,
		)
		if result.Error != nil {
			tx.AddError(result.Error)
			return
		}
		concurrentWriteRan = true
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.GetDB().Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove concurrent update callback: %v", err)
		}
	})

	req := httptest.NewRequest("PUT", "/api/keys/"+strconv.FormatInt(k.ID, 10), strings.NewReader(`{"name":"after"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/keys/%d status = %d: %s", k.ID, rec.Code, rec.Body.String())
	}
	if !concurrentWriteRan {
		t.Fatal("concurrent database change was not injected before the handler update")
	}

	var after model.Key
	if err := db.GetDB().First(&after, k.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Name != "after" {
		t.Errorf("name = %q, want after", after.Name)
	}
	if after.TotalSpent != concurrentSpend {
		t.Errorf("total_spent = %d, want concurrent value %d", after.TotalSpent, concurrentSpend)
	}
	if after.Status != model.KeyStatusDisabled {
		t.Errorf("status = %q, want concurrent value %q", after.Status, model.KeyStatusDisabled)
	}
	if after.RateLimitedUntil == nil || !after.RateLimitedUntil.Equal(concurrentCooldown) {
		t.Errorf("rate_limited_until = %v, want concurrent value %v", after.RateLimitedUntil, concurrentCooldown)
	}
	if after.DisabledReason != model.ReasonAuthFailed {
		t.Errorf("disabled_reason = %q, want concurrent value %q", after.DisabledReason, model.ReasonAuthFailed)
	}
	if after.RecoveryStrategy != model.RecoveryImmediate {
		t.Errorf("recovery_strategy = %q, want concurrent value %q", after.RecoveryStrategy, model.RecoveryImmediate)
	}
}
