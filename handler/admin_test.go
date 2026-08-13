package handler_test

import (
	"encoding/json"
	"net/http/httptest"
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
	h.Updater.SetInstallMode("installed")

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
	if body.CurrentVersion != h.Updater.CurrentVersion {
		t.Errorf("current_version = %q, want %q", body.CurrentVersion, h.Updater.CurrentVersion)
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
