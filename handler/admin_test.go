package handler_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"key-router/handler"
	"key-router/update"

	"github.com/gin-gonic/gin"
)

// TestGetAutoCheckStateAlwaysReportsInstallMode guards the Settings page:
// the Installed/Portable label must be correct BEFORE the first update
// check, so the endpoint reports the local facts (current version + install
// mode) even when no auto-check has run yet.
func TestGetAutoCheckStateAlwaysReportsInstallMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := handler.NewAdminHandler(nil, nil)
	// Pin the mode deterministically (skip the marker-file detection that
	// reads os.Executable of the test runner).
	upd := update.NewClient(h.Updater.CurrentVersion())
	upd.SetInstallMode("installed")
	h.Updater = upd

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	h.GetAutoCheckState(c)

	var body struct {
		Checked         bool   `json:"checked"`
		CurrentVersion  string `json:"current_version"`
		InstallMode     string `json:"install_mode"`
		UpdateAvailable bool   `json:"update_available"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body.Checked {
		t.Errorf("checked = true, want false (no auto-check has run yet)")
	}
	if body.InstallMode != "installed" {
		t.Errorf("install_mode = %q, want %q before any check", body.InstallMode, "installed")
	}
	if body.CurrentVersion != h.Updater.CurrentVersion() {
		t.Errorf("current_version = %q, want %q", body.CurrentVersion, h.Updater.CurrentVersion())
	}
}

// TestGetAutoCheckStateSurfacesLastResult verifies that once an auto-check
// has run, its outcome is served alongside the local facts.
func TestGetAutoCheckStateSurfacesLastResult(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := handler.NewAdminHandler(nil, nil)
	h.SetAutoCheckInfo(&update.UpdateInfo{
		CurrentVersion:  "0.1.8",
		LatestVersion:   "0.1.9",
		UpdateAvailable: true,
		InstallMode:     "portable",
		AssetName:       "KeyRouter-v0.1.9-windows-amd64.exe",
		CheckedAt:       time.Now(),
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	h.GetAutoCheckState(c)

	var body struct {
		Checked         bool   `json:"checked"`
		LatestVersion   string `json:"latest_version"`
		UpdateAvailable bool   `json:"update_available"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !body.Checked {
		t.Errorf("checked = false, want true after an auto-check")
	}
	if !body.UpdateAvailable || body.LatestVersion != "0.1.9" {
		t.Errorf("auto-check result not surfaced: update_available=%v latest_version=%q", body.UpdateAvailable, body.LatestVersion)
	}
}

// fakeUpdater is a test double for handler.Updater.
type fakeUpdater struct {
	info     *update.UpdateInfo
	checkErr error
	applyErr error
	applied  *update.UpdateInfo
}

func (f *fakeUpdater) CurrentVersion() string { return "0.1.8" }
func (f *fakeUpdater) InstallMode() string    { return "installed" }
func (f *fakeUpdater) Check() (*update.UpdateInfo, error) {
	if f.checkErr != nil {
		return nil, f.checkErr
	}
	if f.info == nil {
		return &update.UpdateInfo{CurrentVersion: "0.1.8"}, nil
	}
	return f.info, nil
}
func (f *fakeUpdater) Apply(info *update.UpdateInfo) error {
	f.applied = info
	return f.applyErr
}

// applyContext builds a gin test context for ApplyUpdate.
func applyContext(t *testing.T) (*handler.AdminHandler, *httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := handler.NewAdminHandler(nil, nil)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/api/updates/apply", nil)
	return h, rec, c
}

// freshInfo is an update info that passes the handler's freshness window.
func freshInfo() *update.UpdateInfo {
	return &update.UpdateInfo{
		CurrentVersion:  "0.1.8",
		LatestVersion:   "0.1.9",
		UpdateAvailable: true,
		InstallMode:     "installed",
		AssetName:       "KeyRouter-v0.1.9-windows-amd64-setup.exe",
		AssetURL:        "https://github.com/JohnXu22786/key-router/releases/download/v0.1.9/KeyRouter-v0.1.9-windows-amd64-setup.exe",
		AssetSize:       12345,
		CheckedAt:       time.Now(),
	}
}

// TestApplyUpdateResponseBeforeExit guards the core ordering of the update
// flow: the 200 response must be written (and flushed) BEFORE the exit hook
// runs, so the UI never shows an error for an applied update; and the hook
// must run exactly once.
func TestApplyUpdateResponseBeforeExit(t *testing.T) {
	h, rec, c := applyContext(t)
	h.SetAutoCheckInfo(freshInfo())
	upd := &fakeUpdater{}
	h.Updater = upd

	hookCalls := 0
	h.ExitAfterUpdate = func() {
		hookCalls++
		if rec.Body.Len() == 0 {
			t.Error("exit hook ran before the response was written")
		}
	}

	h.ApplyUpdate(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if hookCalls != 1 {
		t.Errorf("exit hook calls = %d, want 1", hookCalls)
	}
	if upd.applied == nil || upd.applied.AssetURL != freshInfo().AssetURL {
		t.Errorf("Apply received %+v, want the cached check's asset url", upd.applied)
	}
	if upd.applied != nil && !upd.applied.UpdateAvailable {
		t.Error("Apply must receive the info marked as available")
	}
}

// TestApplyUpdateIgnoresRequestBody is the security guard: the management
// API is unauthenticated on localhost, so a client-supplied asset URL must
// NEVER reach Apply — only the server-side check result may.
func TestApplyUpdateIgnoresRequestBody(t *testing.T) {
	h, rec, c := applyContext(t)
	c.Request = httptest.NewRequest("POST", "/api/updates/apply", strings.NewReader(`{"latest_version":"9.9.9","asset_url":"http://127.0.0.1:1337/malware.exe","update_available":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.SetAutoCheckInfo(freshInfo())
	upd := &fakeUpdater{}
	h.Updater = upd
	h.ExitAfterUpdate = func() {}

	h.ApplyUpdate(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if upd.applied == nil || upd.applied.AssetURL != freshInfo().AssetURL {
		t.Errorf("Apply received %+v, want the SERVER-side asset url (body must be ignored)", upd.applied)
	}
}

// TestApplyUpdateCancelDoesNotExit: a declined UAC prompt must surface as
// 409 with a clear message and must NOT trigger the exit hook (there is no
// update to apply).
func TestApplyUpdateCancelDoesNotExit(t *testing.T) {
	h, rec, c := applyContext(t)
	h.SetAutoCheckInfo(freshInfo())
	h.Updater = &fakeUpdater{applyErr: update.ErrUpdateCancelled}
	exited := false
	h.ExitAfterUpdate = func() { exited = true }

	h.ApplyUpdate(c)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	if exited {
		t.Error("exit hook ran after a cancelled update")
	}
	if !strings.Contains(rec.Body.String(), "cancelled") {
		t.Errorf("body should mention the cancellation: %s", rec.Body.String())
	}
}

// TestApplyUpdateCheckFailure: without a cached result the handler falls
// back to Check(); a failed check is a 502 and must not exit.
func TestApplyUpdateCheckFailure(t *testing.T) {
	h, rec, c := applyContext(t)
	h.Updater = &fakeUpdater{checkErr: errors.New("github down")}
	exited := false
	h.ExitAfterUpdate = func() { exited = true }

	h.ApplyUpdate(c)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body: %s)", rec.Code, rec.Body.String())
	}
	if exited {
		t.Error("exit hook ran after a failed check")
	}
}

// TestApplyUpdateFallsBackToCheck: without a cached result the handler
// applies the freshly checked info and still exits.
func TestApplyUpdateFallsBackToCheck(t *testing.T) {
	h, rec, c := applyContext(t)
	info := freshInfo()
	info.InstallMode = "portable"
	upd := &fakeUpdater{info: info}
	h.Updater = upd
	exited := false
	h.ExitAfterUpdate = func() { exited = true }

	h.ApplyUpdate(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !exited {
		t.Error("exit hook did not run after a successful apply")
	}
	if upd.applied == nil || upd.applied.AssetURL != info.AssetURL {
		t.Errorf("Apply received %+v, want the checked info", upd.applied)
	}
}

// TestApplyUpdateStaleCacheRefreshes: a cached result older than the
// freshness window must be re-checked before applying.
func TestApplyUpdateStaleCacheRefreshes(t *testing.T) {
	h, rec, c := applyContext(t)
	stale := freshInfo()
	stale.CheckedAt = time.Now().Add(-48 * time.Hour)
	h.SetAutoCheckInfo(stale)
	info := freshInfo()
	upd := &fakeUpdater{info: info}
	h.Updater = upd
	exited := false
	h.ExitAfterUpdate = func() { exited = true }

	h.ApplyUpdate(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !exited {
		t.Error("exit hook did not run after a successful apply")
	}
	if upd.applied == nil {
		t.Fatal("Apply was not called")
	}
	if upd.applied.CheckedAt.Equal(stale.CheckedAt) {
		t.Error("Apply received the stale cached info; the handler must re-check")
	}
}

// TestApplyUpdateNoUpdateAvailable: a check result marked not-available must
// be rejected with 409 before Apply is called.
func TestApplyUpdateNoUpdateAvailable(t *testing.T) {
	h, rec, c := applyContext(t)
	upd := &fakeUpdater{info: &update.UpdateInfo{
		CurrentVersion:  "0.1.8",
		LatestVersion:   "0.1.8",
		UpdateAvailable: false,
		InstallMode:     "portable",
		CheckedAt:       time.Now(),
	}}
	h.Updater = upd
	exited := false
	h.ExitAfterUpdate = func() { exited = true }

	h.ApplyUpdate(c)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	if exited {
		t.Error("exit hook ran although no update was available")
	}
	if upd.applied != nil {
		t.Error("Apply must not be called when no update is available")
	}
}
