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

// TestFindAssetPortableVsInstalled is the regression test for the portable
// asset-resolution bug: the portable patterns ("-linux-amd64", "-darwin-<arch>")
// are also substrings of the installed artifacts ("-linux-amd64.deb",
// "-linux-amd64.tar.gz", "-darwin-<arch>.dmg"), so a bare substring match
// could resolve an installer/archive as the portable binary when it happens to
// precede the raw binary in the GitHub asset list. Portable matching must skip
// those artifacts, and installed matching must still find them, in ANY order.
// findAssetFor is exercised directly so every OS/arch is covered on any host.
func TestFindAssetPortableVsInstalled(t *testing.T) {
	tests := []struct {
		name         string
		goos, goarch string
		mode         string
		assets       []Asset
		want         string // "" = no match expected
	}{
		{
			name: "linux portable skips .deb/.tar.gz that precede the raw binary",
			goos: "linux", goarch: "amd64", mode: "portable",
			assets: []Asset{
				{Name: "KeyRouter-v0.3.2-linux-amd64.tar.gz"},
				{Name: "KeyRouter-v0.3.2-linux-amd64.deb"},
				{Name: "KeyRouter-v0.3.2-linux-amd64"},
			},
			want: "KeyRouter-v0.3.2-linux-amd64",
		},
		{
			name: "linux portable still finds the raw binary when it is listed first",
			goos: "linux", goarch: "amd64", mode: "portable",
			assets: []Asset{
				{Name: "KeyRouter-v0.3.2-linux-amd64"},
				{Name: "KeyRouter-v0.3.2-linux-amd64.deb"},
			},
			want: "KeyRouter-v0.3.2-linux-amd64",
		},
		{
			name: "linux installed still finds the .deb when the raw binary precedes it",
			goos: "linux", goarch: "amd64", mode: "installed",
			assets: []Asset{
				{Name: "KeyRouter-v0.3.2-linux-amd64"},
				{Name: "KeyRouter-v0.3.2-linux-amd64.tar.gz"},
				{Name: "KeyRouter-v0.3.2-linux-amd64.deb"},
			},
			want: "KeyRouter-v0.3.2-linux-amd64.deb",
		},
		{
			name: "darwin arm64 portable skips the .dmg that precedes the raw binary",
			goos: "darwin", goarch: "arm64", mode: "portable",
			assets: []Asset{
				{Name: "KeyRouter-v0.3.2-darwin-arm64.dmg"},
				{Name: "KeyRouter-v0.3.2-darwin-arm64"},
			},
			want: "KeyRouter-v0.3.2-darwin-arm64",
		},
		{
			name: "darwin amd64 portable skips the .dmg that precedes the raw binary",
			goos: "darwin", goarch: "amd64", mode: "portable",
			assets: []Asset{
				{Name: "KeyRouter-v0.3.2-darwin-amd64.dmg"},
				{Name: "KeyRouter-v0.3.2-darwin-amd64"},
			},
			want: "KeyRouter-v0.3.2-darwin-amd64",
		},
		{
			name: "darwin installed still finds the .dmg when the raw binary precedes it",
			goos: "darwin", goarch: "arm64", mode: "installed",
			assets: []Asset{
				{Name: "KeyRouter-v0.3.2-darwin-arm64"},
				{Name: "KeyRouter-v0.3.2-darwin-arm64.dmg"},
			},
			want: "KeyRouter-v0.3.2-darwin-arm64.dmg",
		},
		{
			name: "windows portable does not pick the setup exe that precedes the raw exe",
			goos: "windows", goarch: "amd64", mode: "portable",
			assets: []Asset{
				{Name: "KeyRouter-v0.3.2-windows-amd64-setup.exe"},
				{Name: "KeyRouter-v0.3.2-windows-amd64.exe"},
			},
			want: "KeyRouter-v0.3.2-windows-amd64.exe",
		},
		{
			name: "windows installed still finds the setup exe when the raw exe precedes it",
			goos: "windows", goarch: "amd64", mode: "installed",
			assets: []Asset{
				{Name: "KeyRouter-v0.3.2-windows-amd64.exe"},
				{Name: "KeyRouter-v0.3.2-windows-amd64-setup.exe"},
			},
			want: "KeyRouter-v0.3.2-windows-amd64-setup.exe",
		},
		{
			name: "unknown platform matches nothing",
			goos: "plan9", goarch: "amd64", mode: "portable",
			assets: []Asset{{Name: "KeyRouter-v0.3.2-linux-amd64"}},
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, ok := findAssetFor(tt.assets, tt.goos, tt.goarch, tt.mode)
			if tt.want == "" {
				if ok {
					t.Fatalf("findAssetFor matched %q, want no match", a.Name)
				}
				return
			}
			if !ok || a.Name != tt.want {
				t.Errorf("findAssetFor = (%q, %v), want (%q, true)", a.Name, ok, tt.want)
			}
		})
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
