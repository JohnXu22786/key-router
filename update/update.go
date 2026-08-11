package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ReleaseInfo is the subset of the GitHub Releases API response we need.
type ReleaseInfo struct {
	TagName string `json:"tag_name"`
	// Assets as returned by the GitHub API
	Assets []Asset `json:"assets"`
}

// Asset is one downloadable file of a release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// UpdateInfo is what the UI shows after a check.
type UpdateInfo struct {
	CurrentVersion  string    `json:"current_version"`
	LatestVersion   string    `json:"latest_version"`
	UpdateAvailable bool      `json:"update_available"`
	InstallMode     string    `json:"install_mode"` // "portable" or "installed"
	AssetName       string    `json:"asset_name,omitempty"`
	AssetURL        string    `json:"asset_url,omitempty"`
	AssetSize       int64     `json:"asset_size,omitempty"`
	CheckedAt       time.Time `json:"checked_at"`
}

// repo and owner of the GitHub releases to check (product repo).
const (
	repoOwner = "JohnXu22786"
	repoName  = "key-router"
)

// installedMarker is written next to the executable by the NSIS installer so
// the updater can tell an installed copy (Program Files — needs the setup
// installer to update) from a portable copy (user-writable dir — the exe can
// be replaced in place).
const installedMarker = "KeyRouter.installed"

// Client checks and applies updates.
type Client struct {
	// CurrentVersion is the running app version (injected at build time).
	CurrentVersion string
	// BaseURL overrides the GitHub API base (tests).
	baseURL string
	// HTTP client with timeout for API + downloads.
	http *http.Client
	// InstallMode cached per check.
	mode string
}

// NewClient creates an update client.
func NewClient(currentVersion string) *Client {
	return &Client{
		CurrentVersion: currentVersion,
		baseURL:        "https://api.github.com",
		http:           &http.Client{Timeout: 30 * time.Second},
	}
}

// GitHubAPIURL returns the releases/latest endpoint URL.
func (c *Client) GitHubAPIURL() string {
	return fmt.Sprintf("%s/repos/%s/%s/releases/latest", c.baseURL, repoOwner, repoName)
}

// Check queries GitHub for the latest release and resolves the right asset
// for this platform + install mode. It never downloads anything.
func (c *Client) Check() (*UpdateInfo, error) {
	req, err := http.NewRequest("GET", c.GitHubAPIURL(), nil)
	if err != nil {
		return nil, err
	}
	// GitHub API requires a User-Agent; without one it 403s.
	req.Header.Set("User-Agent", "KeyRouter-updater/"+c.CurrentVersion)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach GitHub releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// No releases yet — nothing to update to.
		return &UpdateInfo{
			CurrentVersion:  c.CurrentVersion,
			LatestVersion:   c.CurrentVersion,
			UpdateAvailable: false,
			InstallMode:     c.installMode(),
			CheckedAt:       time.Now(),
		}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub releases responded %d", resp.StatusCode)
	}

	var release ReleaseInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to parse release info: %w", err)
	}

	info := &UpdateInfo{
		CurrentVersion:  c.CurrentVersion,
		LatestVersion:   release.TagName,
		UpdateAvailable: compareVersions(release.TagName, c.CurrentVersion) > 0,
		InstallMode:     c.installMode(),
		CheckedAt:       time.Now(),
	}

	// Resolve the asset for this OS/arch/mode.
	asset, ok := c.findAsset(release.Assets)
	if ok {
		info.AssetName = asset.Name
		info.AssetURL = asset.BrowserDownloadURL
		info.AssetSize = asset.Size
	}

	return info, nil
}

// findAsset picks the right asset for this platform and install mode.
//   - installed (Windows): the NSIS setup .exe
//   - portable: the raw binary (or setup exe as a fallback for installed mode)
func (c *Client) findAsset(assets []Asset) (Asset, bool) {
	tagSuffix := "" // the version appears in asset names; we just match patterns
	_ = tagSuffix

	want := c.platformAssetPattern()
	for _, a := range assets {
		if strings.Contains(a.Name, want) {
			return a, true
		}
	}
	return Asset{}, false
}

// platformAssetPattern returns the asset-name fragment for this platform.
func (c *Client) platformAssetPattern() string {
	switch runtime.GOOS {
	case "windows":
		if c.installMode() == "installed" {
			return "-windows-amd64-setup.exe"
		}
		return "-windows-amd64.exe"
	case "darwin":
		arch := "arm64"
		if runtime.GOARCH == "amd64" {
			arch = "amd64"
		}
		if c.installMode() == "installed" {
			return fmt.Sprintf("-darwin-%s.dmg", arch)
		}
		return fmt.Sprintf("-darwin-%s", arch)
	case "linux":
		if c.installMode() == "installed" {
			return "-linux-amd64.deb"
		}
		return "-linux-amd64"
	default:
		return ""
	}
}

// installMode determines whether the running copy is an installed build
// (Program Files, needs the installer to update) or a portable build (the exe
// can be replaced in place). The NSIS installer writes a marker file next to
// the executable.
func (c *Client) installMode() string {
	if c.mode != "" {
		return c.mode
	}
	exe, err := os.Executable()
	if err == nil {
		if _, err := os.Stat(filepath.Join(filepath.Dir(exe), installedMarker)); err == nil {
			c.mode = "installed"
			return c.mode
		}
	}
	c.mode = "portable"
	return c.mode
}

// SetInstallMode overrides the detected install mode (used in tests).
func (c *Client) SetInstallMode(mode string) {
	c.mode = mode
}

// AutoCheck runs a check on startup and then daily. On finding a newer
// version it calls onUpdate (the app shows a notification banner — it never
// applies automatically). The check is best-effort: network failures are
// logged and skipped silently.
func (c *Client) AutoCheck(onUpdate func(info *UpdateInfo)) {
	check := func() {
		info, err := c.Check()
		if err != nil {
			log.Printf("[update] auto-check failed (will retry tomorrow): %v", err)
			return
		}
		if info.UpdateAvailable {
			log.Printf("[update] version %s available (current %s)", info.LatestVersion, info.CurrentVersion)
			if onUpdate != nil {
				onUpdate(info)
			}
		}
	}
	go func() {
		check()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			check()
		}
	}()
}

// Apply downloads the release asset and applies the update:
//   - portable: replaces the running executable (via an .old rename + a
//     small helper script that swaps and relaunches, since Windows cannot
//     overwrite a running exe).
//   - installed: downloads the installer and launches it; the installer
//     handles replacing the app (the current process exits on its own).
func (c *Client) Apply(info *UpdateInfo) error {
	if info == nil || info.AssetURL == "" {
		return fmt.Errorf("no update asset resolved — run a check first")
	}

	// Download to a temp file. Use an .exe suffix on Windows so the
	// installer (CreateProcess requires the extension) and the portable swap
	// script can execute it directly.
	pattern := "keyrouter-update-*"
	if runtime.GOOS == "windows" {
		pattern = "keyrouter-update-*.exe"
	}
	tmp, err := os.CreateTemp("", pattern)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmp.Name())

	req, err := http.NewRequest("GET", info.AssetURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "KeyRouter-updater/"+c.CurrentVersion)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download responded %d", resp.StatusCode)
	}

	// Stream to the temp file, hashing as we go (integrity check).
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body)
	if err != nil {
		return fmt.Errorf("download interrupted: %w", err)
	}
	if info.AssetSize > 0 && n != info.AssetSize {
		return fmt.Errorf("download size mismatch: got %d, want %d", n, info.AssetSize)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = hex.EncodeToString(hasher.Sum(nil)) // sha available for logging

	if c.installMode() == "installed" {
		return c.applyInstalled(tmp.Name(), info)
	}
	return c.applyPortable(tmp.Name(), info)
}

// applyInstalled launches the downloaded installer. For NSIS the /S flag
// performs a silent install; the app itself exits when the installer starts
// (the marker file is next to the exe, replaced by the installer).
func (c *Client) applyInstalled(downloadPath string, info *UpdateInfo) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("installed-mode auto-update is only supported on Windows; run the %s installer manually", info.AssetName)
	}
	cmd := exec.Command(downloadPath, "/S")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch installer: %w", err)
	}
	log.Printf("[update] launched installer %s (%s)", info.AssetName, downloadPath)
	return nil
}

// applyPortable replaces the running executable. Because the process is
// running, Windows locks the exe; the helper script renames the current exe
// to .old, moves the new one in, and relaunches. On non-Windows the running
// binary can usually be replaced directly after rename (POSIX allows
// unlink/rename of a running file).
func (c *Client) applyPortable(downloadPath string, info *UpdateInfo) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate running executable: %w", err)
	}
	dir := filepath.Dir(exe)
	exeName := filepath.Base(exe)

	// Move the downloaded file into the app dir under a temp name, then run
	// the swap script.
	newPath := filepath.Join(dir, exeName+".new")
	if err := os.Rename(downloadPath, newPath); err != nil {
		// Cross-device move (temp dir on another volume): copy instead.
		if err := copyFile(downloadPath, newPath); err != nil {
			return fmt.Errorf("failed to stage new executable: %w", err)
		}
	}

	if runtime.GOOS == "windows" {
		return c.launchWindowsSwap(dir, exeName, newPath)
	}

	// POSIX: rename the running exe aside, move the new one in, relaunch.
	oldPath := filepath.Join(dir, exeName+".old")
	if err := os.Rename(exe, oldPath); err != nil {
		return fmt.Errorf("failed to rename running executable: %w", err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		// Restore the old binary on failure.
		os.Rename(oldPath, exe)
		return fmt.Errorf("failed to replace executable: %w", err)
	}
	return c.relaunch(exe)
}

// launchWindowsSwap writes a small batch script that waits for this process
// to exit, swaps exe.new into place, and relaunches. Returns the command to
// start it; the app should then exit promptly.
func (c *Client) launchWindowsSwap(dir, exeName, newPath string) error {
	script := filepath.Join(dir, exeName+".update.bat")
	content := fmt.Sprintf(`@echo off
:wait
tasklist /FI "IMAGENAME eq %[1]s" 2>nul | find /I "%[1]s" >nul
if not errorlevel 1 (
  timeout /t 1 /nobreak >nul
  goto wait
)
move /Y "%[2]s" "%[3]s" >nul
start "" "%[3]s"
del "%%~f0"
`, exeName, newPath, filepath.Join(dir, exeName))

	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write update script: %w", err)
	}
	cmd := exec.Command("cmd", "/c", script)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch update script: %w", err)
	}
	return nil
}

// relaunch starts the (new) executable detached and returns.
func (c *Client) relaunch(exe string) error {
	cmd := exec.Command(exe)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to relaunch app: %w", err)
	}
	return nil
}

// compareVersions compares two version strings of the form vX.Y.Z (or
// X.Y.Z). Returns >0 if a > b, <0 if a < b, 0 if equal. Non-numeric
// suffixes are tolerated (v1.2.3-beta < v1.2.3).
func compareVersions(a, b string) int {
	pa := parseVersion(a)
	pb := parseVersion(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	// Same numeric part: a prerelease suffix sorts lower than a release.
	sa := strings.TrimPrefix(a, "v")
	sb := strings.TrimPrefix(b, "v")
	ra := strings.Count(sa, ".") >= 2 && strings.Contains(strings.SplitN(sa, ".", 4)[2], "-")
	rb := strings.Count(sb, ".") >= 2 && strings.Contains(strings.SplitN(sb, ".", 4)[2], "-")
	if ra != rb {
		if ra {
			return -1
		}
		return 1
	}
	return 0
}

// parseVersion splits "v1.2.3-beta" into [1,2,3], missing parts are 0.
func parseVersion(v string) [3]int {
	s := strings.TrimPrefix(v, "v")
	parts := strings.SplitN(s, ".", 4)
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		num := strings.SplitN(parts[i], "-", 2)[0] // strip prerelease suffix
		fmt.Sscanf(num, "%d", &out[i])
	}
	return out
}

// copyFile copies src to dst (used when rename fails across volumes).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
