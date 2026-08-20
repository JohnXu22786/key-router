package update

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int // -1, 0, 1
	}{
		{"v0.1.9", "v0.1.8", 1},
		{"v0.1.8", "v0.1.9", -1},
		{"v0.1.9", "v0.1.9", 0},
		{"0.1.9", "v0.1.9", 0}, // missing v prefix tolerated
		{"v0.2.0", "v0.1.9", 1},
		{"v1.0.0", "v0.9.9", 1},
		{"v0.1.10", "v0.1.9", 1}, // multi-digit patch
		{"v0.1.9-beta", "v0.1.9", -1},
		{"v0.1.9-beta", "v0.1.8", 1},
		{"v0.1.9", "v0.1", 1}, // missing part = 0
	}
	for _, tt := range tests {
		got := compareVersions(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		// Antisymmetry: swap must give the negated result (or equal for 0).
		rev := compareVersions(tt.b, tt.a)
		if tt.want == 0 {
			if rev != 0 {
				t.Errorf("compareVersions(%q, %q) = %d, want 0", tt.b, tt.a, rev)
			}
		} else if rev != -tt.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.b, tt.a, rev, -tt.want)
		}
	}
}

func TestFindAsset(t *testing.T) {
	assets := []Asset{
		{Name: "KeyRouter-v0.1.9-windows-amd64.exe"},
		{Name: "KeyRouter-v0.1.9-windows-amd64-setup.exe"},
		{Name: "KeyRouter-v0.1.9-darwin-arm64"},
		{Name: "KeyRouter-v0.1.9-darwin-arm64.dmg"},
		{Name: "KeyRouter-v0.1.9-linux-amd64"},
	}

	t.Run("portable mode picks raw binary", func(t *testing.T) {
		// Platform-dependent pattern: just verify the matching logic on the
		// current platform returns a non-empty name for the platform prefix.
		// The install-mode distinction is covered below via SetInstallMode.
		if runtime.GOOS == "windows" {
			// portable pattern on windows = raw exe
			c := NewClient("v0.1.8")
			a, ok := c.findAsset(assets)
			if !ok || a.Name != "KeyRouter-v0.1.9-windows-amd64.exe" {
				t.Errorf("portable mode got %q, want raw exe", a.Name)
			}
		}
	})

	t.Run("installed mode picks setup exe", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("installed-mode asset selection is Windows-specific")
		}
		c := NewClient("v0.1.8")
		c.SetInstallMode("installed")
		a, ok := c.findAsset(assets)
		if !ok || a.Name != "KeyRouter-v0.1.9-windows-amd64-setup.exe" {
			t.Errorf("installed mode got %q, want setup exe", a.Name)
		}
	})
}

// TestMakeExecutable reproduces the portable-update exec-bit bug: the
// download lands in a file with no execute permissions (os.CreateTemp uses
// 0600; copyFile's os.Create yields 0644) and rename preserves that mode, so
// a POSIX portable update would install a non-executable binary and the
// relaunch's `exec` would fail with EACCES. makeExecutable must give the
// staged file the running binary's permissions (guaranteed executable) for
// both staging paths.
func TestMakeExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("execute permissions are POSIX-only; Windows binaries run by extension")
	}
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref")
	staged := filepath.Join(dir, "staged")
	if err := os.WriteFile(ref, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// umask may narrow the write above, so stat the file for the exact mode.
	fi, err := os.Stat(ref)
	if err != nil {
		t.Fatal(err)
	}
	refPerm := fi.Mode().Perm()

	// The two staging paths that produce a non-executable file: the rename
	// path inherits os.CreateTemp's 0600, the cross-device fallback inherits
	// copyFile's os.Create 0644 (0666 & umask). Remove the file each
	// iteration so WriteFile re-creates it with the given mode (the mode is
	// only applied on creation).
	for _, srcMode := range []os.FileMode{0o600, 0o644} {
		if err := os.Remove(staged); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.WriteFile(staged, []byte("bin"), srcMode); err != nil {
			t.Fatalf("create staged at %o: %v", srcMode, err)
		}
		if err := makeExecutable(ref, staged); err != nil {
			t.Fatalf("makeExecutable from %o: %v", srcMode, err)
		}
		fi, err := os.Stat(staged)
		if err != nil {
			t.Fatal(err)
		}
		perm := fi.Mode().Perm()
		want := refPerm | 0o111
		if perm != want {
			t.Errorf("staged file mode = %o, want %o (executable, keeping reference perms)", perm, want)
		}
	}
}

// TestWindowsSwapScript guards the portable swap batch: it must wait for
// THIS process (by PID) to exit (the exe is locked while the process
// lives), swap the staged exe into place, relaunch, and delete itself.
func TestWindowsSwapScript(t *testing.T) {
	s := windowsSwapScript(
		`D:\apps\KeyRouter\KeyRouter.exe.new`,
		`D:\apps\KeyRouter\KeyRouter.exe`,
		4242,
	)
	for _, want := range []string{
		`PID eq 4242`,
		`move /Y "D:\apps\KeyRouter\KeyRouter.exe.new" "D:\apps\KeyRouter\KeyRouter.exe"`,
		`start "" "D:\apps\KeyRouter\KeyRouter.exe"`,
		`del "%~f0"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("swap script missing %q\n---\n%s", want, s)
		}
	}
}

// tempUpdateFiles snapshots the updater's temp files (keyrouter-update-* in
// the system temp dir).
func tempUpdateFiles(t *testing.T) map[string]bool {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "keyrouter-update-*"))
	m := make(map[string]bool, len(matches))
	for _, p := range matches {
		m[p] = true
	}
	return m
}

// TestApplyDownloadFailureLeavesNoTempFiles: a failed apply must clean up its
// temp file in BOTH install modes — a leaked installer exe in installed mode
// is exactly the kind of leftover this guards against.
func TestApplyDownloadFailureLeavesNoTempFiles(t *testing.T) {
	before := tempUpdateFiles(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	for _, mode := range []string{"portable", "installed"} {
		c := NewClient("v0.1.8")
		c.SetInstallMode(mode)
		info := &UpdateInfo{
			AssetName: "KeyRouter-v0.1.9-windows-amd64-setup.exe",
			AssetURL:  srv.URL + "/asset",
			AssetSize: 12345,
		}
		if err := c.Apply(info); err == nil {
			t.Fatalf("mode %s: Apply succeeded, want download failure", mode)
		}
	}
	for p := range tempUpdateFiles(t) {
		if !before[p] {
			t.Errorf("temp file left behind after failed apply: %s", p)
		}
	}
}

// TestApplyInstalledCancelCleansTemp: a declined UAC prompt must surface as
// ErrUpdateCancelled AND not leave the downloaded installer behind.
func TestApplyInstalledCancelCleansTemp(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("installed-mode apply is Windows-only")
	}
	orig := launchInstaller
	launchInstaller = func(path, args string) error { return ErrUpdateCancelled }
	defer func() { launchInstaller = orig }()

	before := tempUpdateFiles(t)
	body := []byte("MZ fake installer")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	c := NewClient("v0.1.8")
	c.SetInstallMode("installed")
	info := &UpdateInfo{
		AssetName: "KeyRouter-v0.1.9-windows-amd64-setup.exe",
		AssetURL:  srv.URL + "/a",
		AssetSize: int64(len(body)),
	}
	if err := c.Apply(info); !errors.Is(err, ErrUpdateCancelled) {
		t.Fatalf("Apply error = %v, want ErrUpdateCancelled", err)
	}
	for p := range tempUpdateFiles(t) {
		if !before[p] {
			t.Errorf("temp file left behind after cancelled update: %s", p)
		}
	}
}

// TestApplyInstalledKeepsTempForInstaller: on a successful launch the temp
// installer must survive the Apply call — the running installer reads it
// (it is cleaned up by the startup sweep of stale downloads afterwards) —
// and the installer must be launched VISIBLY (empty args, never /S silent:
// the silent launch was why users never saw the installer open).
func TestApplyInstalledKeepsTempForInstaller(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("installed-mode apply is Windows-only")
	}
	before := tempUpdateFiles(t)
	// Clean up whatever the apply leaves behind (the kept installer), even
	// when an assertion fails mid-test.
	defer func() {
		for p := range tempUpdateFiles(t) {
			if !before[p] {
				os.Remove(p)
			}
		}
	}()

	var launchArgs string
	origInstaller := launchInstaller
	launchInstaller = func(path, args string) error { launchArgs = args; return nil }
	defer func() { launchInstaller = origInstaller }()

	body := []byte("MZ fake installer")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	c := NewClient("v0.1.8")
	c.SetInstallMode("installed")
	info := &UpdateInfo{
		AssetName: "KeyRouter-v0.1.9-windows-amd64-setup.exe",
		AssetURL:  srv.URL + "/a",
		AssetSize: int64(len(body)),
	}
	if err := c.Apply(info); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if launchArgs != "" {
		t.Errorf("installer launched with args %q, want empty (the setup wizard must show)", launchArgs)
	}

	// Exactly one new temp file: the installer handed to the (fake) launch.
	kept := 0
	for p := range tempUpdateFiles(t) {
		if !before[p] {
			kept++
		}
	}
	if kept != 1 {
		t.Errorf("expected exactly one kept temp installer, found %d", kept)
	}
}

// TestCleanupStaleDownloads: the startup cleanup removes only old updater
// and restart-helper temp files — never fresh ones or unrelated files.
func TestCleanupStaleDownloads(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "keyrouter-update-1.exe")
	oldRestart := filepath.Join(dir, "keyrouter-restart-1.bat")
	fresh := filepath.Join(dir, "keyrouter-update-2.exe")
	freshRestart := filepath.Join(dir, "keyrouter-restart-2.bat")
	other := filepath.Join(dir, "unrelated.bat")
	for _, p := range []string{old, oldRestart, fresh, freshRestart, other} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	for _, p := range []string{old, oldRestart} {
		if err := os.Chtimes(p, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}

	cleanupStaleDownloads(dir, 24*time.Hour)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("stale updater temp file was not removed")
	}
	if _, err := os.Stat(oldRestart); !os.IsNotExist(err) {
		t.Error("stale restart helper script was not removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh updater temp file must be kept")
	}
	if _, err := os.Stat(freshRestart); err != nil {
		t.Error("fresh restart helper script must be kept")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("unrelated file must be kept")
	}
}
