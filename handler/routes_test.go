package handler_test

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
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

	"gorm.io/gorm"
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
	}, "order_version": map[string]any{"timestamp": 1, "client_id": "test", "sequence": 1}})
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
	var currentVersion model.RouteOrderVersion
	if err := json.Unmarshal([]byte(rec.Header().Get("X-Route-Order-Version")), &currentVersion); err != nil {
		t.Fatalf("bad route order version header: %v", err)
	}
	if currentVersion.Timestamp != 1 || currentVersion.ClientID != "test" {
		t.Fatalf("route order version header = %+v, want committed version", currentVersion)
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

func TestReorderRoutesSerializesOverlappingRequests(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	provider := model.Provider{Name: "A", Type: "openai", BaseURL: "http://a"}
	if err := db.GetDB().Create(&provider).Error; err != nil {
		t.Fatal(err)
	}
	group := model.ModelGroup{GroupID: "g1", Name: "G1", Enabled: true}
	if err := db.GetDB().Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	routes := []*model.Route{
		{ModelGroupID: group.ID, ProviderID: provider.ID, TargetModel: "a", Priority: 0},
		{ModelGroupID: group.ID, ProviderID: provider.ID, TargetModel: "b", Priority: 1},
		{ModelGroupID: group.ID, ProviderID: provider.ID, TargetModel: "c", Priority: 2},
	}
	for _, route := range routes {
		if err := db.GetDB().Create(route).Error; err != nil {
			t.Fatal(err)
		}
	}
	makePayload := func(order []*model.Route, timestamp float64, sequence int) []byte {
		rows := make([]map[string]any, 0, len(order))
		for priority, route := range order {
			rows = append(rows, map[string]any{"id": route.ID, "priority": priority})
		}
		payload, err := json.Marshal(map[string]any{
			"routes":        rows,
			"order_version": map[string]any{"timestamp": timestamp, "client_id": "same-tab", "sequence": sequence},
		})
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}

	olderRelease := make(chan struct{})
	olderStarted := make(chan struct{})
	olderBody := &delayedBody{
		reader:  bytes.NewReader(makePayload([]*model.Route{routes[1], routes[2], routes[0]}, 1, 1)),
		started: olderStarted,
		release: olderRelease,
	}
	olderRequest := httptest.NewRequest("POST", "/api/routes/reorder", olderBody)
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
		reader:  bytes.NewReader(makePayload([]*model.Route{routes[2], routes[0], routes[1]}, 2, 2)),
		started: newerStarted,
		release: newerRelease,
	}
	newerRequest := httptest.NewRequest("POST", "/api/routes/reorder", newerBody)
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
	req := httptest.NewRequest("GET", "/api/routes", nil)
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
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
	for _, route := range out {
		got = append(got, route.TargetModel)
	}
	want := []string{"c", "a", "b"}
	if !slices.Equal(got, want) {
		t.Fatalf("GET /api/routes order = %v, want the later reorder %v", got, want)
	}
}

func TestReorderRoutesRejectsStaleVersionsAndHandlesClockEdges(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	provider := model.Provider{Name: "A", Type: "openai", BaseURL: "http://a"}
	if err := db.GetDB().Create(&provider).Error; err != nil {
		t.Fatal(err)
	}
	group := model.ModelGroup{GroupID: "g1", Name: "G1", Enabled: true}
	if err := db.GetDB().Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	routes := []*model.Route{
		{ModelGroupID: group.ID, ProviderID: provider.ID, TargetModel: "a", Priority: 0},
		{ModelGroupID: group.ID, ProviderID: provider.ID, TargetModel: "b", Priority: 1},
		{ModelGroupID: group.ID, ProviderID: provider.ID, TargetModel: "c", Priority: 2},
	}
	for _, route := range routes {
		if err := db.GetDB().Create(route).Error; err != nil {
			t.Fatal(err)
		}
	}
	postOrder := func(order []*model.Route, timestamp float64, clientID string, sequence int) (int, string) {
		rows := make([]map[string]any, 0, len(order))
		for priority, route := range order {
			rows = append(rows, map[string]any{"id": route.ID, "priority": priority})
		}
		payload, err := json.Marshal(map[string]any{
			"routes": rows,
			"order_version": map[string]any{
				"timestamp": timestamp,
				"client_id": clientID,
				"sequence":  sequence,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("POST", "/api/routes/reorder", strings.NewReader(string(payload)))
		request.Header.Set("Content-Type", "application/json")
		request.Host = "localhost:9999"
		response := httptest.NewRecorder()
		e.ServeHTTP(response, request)
		if response.Code != http.StatusOK && response.Code != http.StatusConflict && response.Code != http.StatusBadRequest {
			t.Fatalf("POST /api/routes/reorder status = %d: %s", response.Code, response.Body.String())
		}
		var result map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatalf("bad reorder response: %v\n%s", err, response.Body.String())
		}
		if response.Code == http.StatusConflict && result["status"] != "stale" {
			t.Fatalf("conflict response status = %q, want stale", result["status"])
		}
		return response.Code, result["status"]
	}
	readOrder := func() []string {
		request := httptest.NewRequest("GET", "/api/routes", nil)
		request.Host = "localhost:9999"
		response := httptest.NewRecorder()
		e.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET /api/routes status = %d: %s", response.Code, response.Body.String())
		}
		var out []struct {
			TargetModel string `json:"target_model"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad routes response: %v\n%s", err, response.Body.String())
		}
		if response.Header().Get("X-Route-Order-Version") == "" {
			t.Fatal("GET /api/routes omitted the persisted order version")
		}
		order := make([]string, 0, len(out))
		for _, route := range out {
			order = append(order, route.TargetModel)
		}
		return order
	}

	readVersion := func() string {
		var setting model.Setting
		if err := db.GetDB().Where("key = ?", model.SettingRouteOrderVersion).First(&setting).Error; err != nil {
			t.Fatalf("read route order version setting: %v", err)
		}
		return setting.Value
	}

	legacyPayload, err := json.Marshal(map[string]any{"routes": []map[string]any{{"id": routes[0].ID, "priority": 0}}})
	if err != nil {
		t.Fatal(err)
	}
	legacyRequest := httptest.NewRequest("POST", "/api/routes/reorder", strings.NewReader(string(legacyPayload)))
	legacyRequest.Header.Set("Content-Type", "application/json")
	legacyRequest.Host = "localhost:9999"
	legacyResponse := httptest.NewRecorder()
	e.ServeHTTP(legacyResponse, legacyRequest)
	if legacyResponse.Code != http.StatusBadRequest {
		t.Fatalf("versionless reorder status = %d, want 400: %s", legacyResponse.Code, legacyResponse.Body.String())
	}
	versionBeforeFutureRequest := readVersion()
	if code, status := postOrder([]*model.Route{routes[2], routes[0], routes[1]}, 1e16, "poisoned-clock", 1); code != http.StatusBadRequest || status != "" {
		t.Fatalf("far-future timestamp request = %d/%q, want 400", code, status)
	}
	if got, want := readOrder(), []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Fatalf("far-future request changed route priorities = %v, want %v", got, want)
	}
	if got := readVersion(); got != versionBeforeFutureRequest {
		t.Fatalf("far-future request changed version from %s to %s", versionBeforeFutureRequest, got)
	}
	if code, status := postOrder([]*model.Route{routes[2], routes[0], routes[1]}, 200, "tab-b", 1); code != http.StatusOK || status != "ok" {
		t.Fatalf("newer request = %d/%q, want 200/ok", code, status)
	}

	// Reconstruct the single local handler while retaining the same SQLite DB.
	e = router.Setup(embed.FS{}, selector.NewEngine(), health.NewChecker(), events.NewHub())
	if code, status := postOrder([]*model.Route{routes[1], routes[2], routes[0]}, 200, "tab-b", 1); code != http.StatusConflict || status != "stale" {
		t.Fatalf("pagehide retry after handler restart = %d/%q, want 409/stale", code, status)
	}
	if code, status := postOrder([]*model.Route{routes[1], routes[2], routes[0]}, 199, "tab-a", 1); code != http.StatusConflict || status != "stale" {
		t.Fatalf("late older request after handler restart = %d/%q, want 409/stale", code, status)
	}
	if code, status := postOrder([]*model.Route{routes[1], routes[2], routes[0]}, 200, "tab-a", 2); code != http.StatusConflict || status != "stale" {
		t.Fatalf("lower client ID at equal timestamp = %d/%q, want 409/stale", code, status)
	}
	if code, status := postOrder([]*model.Route{routes[0], routes[1], routes[2]}, 150, "tab-b", 2); code != http.StatusOK || status != "ok" {
		t.Fatalf("same-client sequence after clock regression = %d/%q, want 200/ok", code, status)
	}
	if code, status := postOrder([]*model.Route{routes[1], routes[2], routes[0]}, 199, "tab-z", 1); code != http.StatusConflict || status != "stale" {
		t.Fatalf("older cross-client request after clock regression = %d/%q, want 409/stale", code, status)
	}
	if got, want := readOrder(), []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Fatalf("GET /api/routes order = %v, want %v", got, want)
	}
}

func TestGetRoutesReturnsVersionAndPrioritiesFromOneSnapshot(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	provider := model.Provider{Name: "snapshot-provider", Type: "openai", BaseURL: "http://snapshot.test"}
	if err := db.GetDB().Create(&provider).Error; err != nil {
		t.Fatal(err)
	}
	group := model.ModelGroup{GroupID: "snapshot-group", Name: "Snapshot Group", Enabled: true}
	if err := db.GetDB().Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	routes := []*model.Route{
		{ModelGroupID: group.ID, ProviderID: provider.ID, TargetModel: "old-first", Priority: 0},
		{ModelGroupID: group.ID, ProviderID: provider.ID, TargetModel: "old-second", Priority: 1},
	}
	for _, route := range routes {
		if err := db.GetDB().Create(route).Error; err != nil {
			t.Fatal(err)
		}
	}

	routeQueryStarted := make(chan struct{})
	resumeRouteQuery := make(chan struct{})
	var once sync.Once
	if err := db.GetDB().Callback().Query().Before("gorm:query").Register("test:pause_route_snapshot", func(tx *gorm.DB) {
		if tx.Statement.Table == "routes" {
			once.Do(func() {
				close(routeQueryStarted)
				<-resumeRouteQuery
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.GetDB().Callback().Query().Remove("test:pause_route_snapshot")
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(resumeRouteQuery) }) }
	defer resume()

	getRequest := httptest.NewRequest("GET", "/api/routes", nil)
	getRequest.Host = "localhost:9999"
	getResponse := httptest.NewRecorder()
	getDone := make(chan struct{})
	go func() {
		e.ServeHTTP(getResponse, getRequest)
		close(getDone)
	}()
	select {
	case <-routeQueryStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("GET did not reach the route query")
	}
	sqlDB, err := db.GetDB().DB()
	if err != nil {
		t.Fatal(err)
	}
	waitCount := sqlDB.Stats().WaitCount

	writePayload, err := json.Marshal(map[string]any{
		"routes": []map[string]any{
			{"id": routes[1].ID, "priority": 0},
			{"id": routes[0].ID, "priority": 1},
		},
		"order_version": map[string]any{"timestamp": 100, "client_id": "snapshot-writer", "sequence": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeRequest := httptest.NewRequest("POST", "/api/routes/reorder", strings.NewReader(string(writePayload)))
	writeRequest.Header.Set("Content-Type", "application/json")
	writeRequest.Host = "localhost:9999"
	writeResponse := httptest.NewRecorder()
	writeDone := make(chan struct{})
	go func() {
		e.ServeHTTP(writeResponse, writeRequest)
		close(writeDone)
	}()

	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for sqlDB.Stats().WaitCount == waitCount {
		select {
		case <-writeDone:
			resume()
			t.Fatalf("reorder completed while GET was paused between version and route reads: %d", writeResponse.Code)
		case <-deadline:
			resume()
			t.Fatal("reorder did not wait for the route snapshot transaction")
		case <-ticker.C:
		}
	}

	resume()
	select {
	case <-getDone:
	case <-time.After(5 * time.Second):
		t.Fatal("GET remained blocked after releasing the snapshot query")
	}
	select {
	case <-writeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("reorder remained blocked after GET committed")
	}
	if getResponse.Code != http.StatusOK {
		t.Fatalf("GET /api/routes status = %d: %s", getResponse.Code, getResponse.Body.String())
	}
	if writeResponse.Code != http.StatusOK {
		t.Fatalf("POST /api/routes/reorder status = %d: %s", writeResponse.Code, writeResponse.Body.String())
	}
	var version model.RouteOrderVersion
	if err := json.Unmarshal([]byte(getResponse.Header().Get("X-Route-Order-Version")), &version); err != nil {
		t.Fatalf("bad snapshot version header: %v", err)
	}
	if version != (model.RouteOrderVersion{}) {
		t.Fatalf("GET version = %+v, want pre-reorder snapshot", version)
	}
	var got []struct {
		TargetModel string `json:"target_model"`
	}
	if err := json.Unmarshal(getResponse.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TargetModel != "old-first" || got[1].TargetModel != "old-second" {
		t.Fatalf("GET routes = %+v, want pre-reorder snapshot", got)
	}
}

func TestReorderRoutesDoesNotBlockKeyReorderDuringSlowBody(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	provider := model.Provider{Name: "A", Type: "openai", BaseURL: "http://a"}
	if err := db.GetDB().Create(&provider).Error; err != nil {
		t.Fatal(err)
	}
	group := model.ModelGroup{GroupID: "g1", Name: "G1", Enabled: true}
	if err := db.GetDB().Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	route := model.Route{ModelGroupID: group.ID, ProviderID: provider.ID, TargetModel: "a", Priority: 0}
	if err := db.GetDB().Create(&route).Error; err != nil {
		t.Fatal(err)
	}
	key := model.Key{ProviderID: provider.ID, Name: "key", KeyValue: "secret"}
	if err := db.GetDB().Create(&key).Error; err != nil {
		t.Fatal(err)
	}

	versionedPayload, err := json.Marshal(map[string]any{
		"routes":        []map[string]any{{"id": route.ID, "priority": 0}},
		"order_version": map[string]any{"timestamp": 1, "client_id": "slow-tab", "sequence": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	routeRelease := make(chan struct{})
	routeStarted := make(chan struct{})
	routeBody := &delayedBody{
		reader:  bytes.NewReader(versionedPayload),
		started: routeStarted,
		release: routeRelease,
	}
	routeRequest := httptest.NewRequest("POST", "/api/routes/reorder", routeBody)
	routeRequest.Header.Set("Content-Type", "application/json")
	routeRequest.Host = "localhost:9999"
	routeResponse := httptest.NewRecorder()
	routeDone := make(chan struct{})
	go func() {
		e.ServeHTTP(routeResponse, routeRequest)
		close(routeDone)
	}()
	select {
	case <-routeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("route request did not begin reading its body")
	}

	keyPayload, err := json.Marshal(map[string]any{"keys": []map[string]any{{"id": key.ID, "sort_order": 0}}})
	if err != nil {
		t.Fatal(err)
	}
	keyRequest := httptest.NewRequest("POST", "/api/keys/reorder", strings.NewReader(string(keyPayload)))
	keyRequest.Header.Set("Content-Type", "application/json")
	keyRequest.Host = "localhost:9999"
	keyResponse := httptest.NewRecorder()
	keyDone := make(chan struct{})
	go func() {
		e.ServeHTTP(keyResponse, keyRequest)
		close(keyDone)
	}()
	select {
	case <-keyDone:
	case <-time.After(time.Second):
		close(routeRelease)
		<-routeDone
		<-keyDone
		t.Fatal("slow route body blocked an unrelated key reorder")
	}
	if keyResponse.Code != http.StatusOK {
		close(routeRelease)
		<-routeDone
		t.Fatalf("POST /api/keys/reorder status = %d: %s", keyResponse.Code, keyResponse.Body.String())
	}

	close(routeRelease)
	<-routeDone
	if routeResponse.Code != http.StatusOK {
		t.Fatalf("POST /api/routes/reorder status = %d: %s", routeResponse.Code, routeResponse.Body.String())
	}
}

func TestReorderRoutesCancelsWhenWaitingForDatabase(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	provider := model.Provider{Name: "A", Type: "openai", BaseURL: "http://a"}
	if err := db.GetDB().Create(&provider).Error; err != nil {
		t.Fatal(err)
	}
	group := model.ModelGroup{GroupID: "g1", Name: "G1", Enabled: true}
	if err := db.GetDB().Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	routes := []*model.Route{
		{ModelGroupID: group.ID, ProviderID: provider.ID, TargetModel: "a", Priority: 0},
		{ModelGroupID: group.ID, ProviderID: provider.ID, TargetModel: "b", Priority: 1},
	}
	for _, route := range routes {
		if err := db.GetDB().Create(route).Error; err != nil {
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
	payload, err := json.Marshal(map[string]any{"routes": []map[string]any{
		{"id": routes[1].ID, "priority": 0},
		{"id": routes[0].ID, "priority": 1},
	}, "order_version": map[string]any{"timestamp": 100, "client_id": "cancel-test", "sequence": 1}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/routes/reorder", strings.NewReader(string(payload))).WithContext(ctx)
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

	req := httptest.NewRequest("GET", "/api/routes", nil)
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
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
	for _, route := range out {
		got = append(got, route.TargetModel)
	}
	want := []string{"a", "b"}
	if !slices.Equal(got, want) {
		t.Fatalf("GET /api/routes order = %v, canceled reorder must not commit", got)
	}
}
