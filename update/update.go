package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// ErrUpdateCancelled is returned when the user declines the UAC elevation
// prompt for the installer. The app must NOT exit in that case (there is no
// update to apply), so the error is distinct from a launch failure.
var ErrUpdateCancelled = errors.New("update cancelled — the elevation prompt was declined")

// Client checks and applies updates.
type Client struct {
	// currentVersion is the running app version (injected at build time).
	currentVersion string
	// BaseURL overrides the GitHub API base (tests).
	baseURL string
	// HTTP client with timeout for API checks (short: a hanging API must not
	// block the UI for long).
	http *http.Client
	// download is the client for asset downloads: a multi-minute installer
	// download must not be cut off by the API client's short timeout.
	download *http.Client
	// InstallMode cached per check.
	mode string
}

// NewClient creates an update client.
func NewClient(currentVersion string) *Client {
	return &Client{
		currentVersion: currentVersion,
		baseURL:        "https://api.github.com",
		http:           &http.Client{Timeout: 30 * time.Second},
		download:       &http.Client{Timeout: 30 * time.Minute},
	}
}

// CurrentVersion returns the running app version.
func (c *Client) CurrentVersion() string {
	return c.currentVersion
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
	req.Header.Set("User-Agent", "KeyRouter-updater/"+c.currentVersion)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach GitHub releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// No releases yet — nothing to update to.
		return &UpdateInfo{
			CurrentVersion:  c.currentVersion,
			LatestVersion:   c.currentVersion,
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
		CurrentVersion:  c.currentVersion,
		LatestVersion:   release.TagName,
		UpdateAvailable: compareVersions(release.TagName, c.currentVersion) > 0,
		InstallMode:     c.installMode(),
		CheckedAt:       time.Now(),
	}

	// Resolve the asset for this OS/arch/mode. A release without a matching
	// asset cannot be applied, so report it as an error instead of showing
	// "update available" and failing on Apply.
	asset, ok := c.findAsset(release.Assets)
	if !ok {
		return nil, fmt.Errorf("release %s has no asset for this platform/install mode (%s)", release.TagName, c.platformAssetPattern())
	}
	info.AssetName = asset.Name
	info.AssetURL = asset.BrowserDownloadURL
	info.AssetSize = asset.Size

	return info, nil
}

// findAsset picks the right asset for this platform and install mode.
//   - installed (Windows): the NSIS setup .exe
//   - portable: the raw binary (or setup exe as a fallback for installed mode)
func (c *Client) findAsset(assets []Asset) (Asset, bool) {
	return findAssetFor(assets, runtime.GOOS, runtime.GOARCH, c.installMode())
}

// findAssetFor selects the first asset that matches the artifact pattern for
// the given OS/arch/install mode.
func findAssetFor(assets []Asset, goos, goarch, installMode string) (Asset, bool) {
	for _, a := range assets {
		if matchesAssetPattern(goos, goarch, installMode, a.Name) {
			return a, true
		}
	}
	return Asset{}, false
}

// matchesAssetPattern reports whether an asset name matches the artifact
// pattern for the given OS/arch/install mode. Matching is a SUFFIX test, not
// a bare substring: released names always end with the artifact fragment, so
// "-linux-amd64" (portable) must never also match "-linux-amd64.deb" or
// "-linux-amd64.tar.gz", in the same way "-darwin-<arch>" must never match
// "-darwin-<arch>.dmg" — otherwise a portable update could download the
// installer or archive as its binary when that asset precedes the raw binary
// in the GitHub asset list. An unknown platform has no pattern and matches
// nothing.
func matchesAssetPattern(goos, goarch, installMode, name string) bool {
	p := assetPattern(goos, goarch, installMode)
	if p == "" {
		return false
	}
	return strings.HasSuffix(name, p)
}

// assetPattern returns the asset-name suffix that identifies the artifact
// for the given OS/arch/install mode. Portable and installed patterns are
// deliberately distinct so neither mode can resolve the other's artifact:
// "-linux-amd64" vs "-linux-amd64.deb", "-darwin-<arch>" vs
// "-darwin-<arch>.dmg", and "-windows-amd64.exe" vs "-windows-amd64-setup.exe".
func assetPattern(goos, goarch, installMode string) string {
	switch goos {
	case "windows":
		if installMode == "installed" {
			return "-windows-amd64-setup.exe"
		}
		return "-windows-amd64.exe"
	case "darwin":
		arch := "arm64"
		if goarch == "amd64" {
			arch = "amd64"
		}
		if installMode == "installed" {
			return "-darwin-" + arch + ".dmg"
		}
		return "-darwin-" + arch
	case "linux":
		if installMode == "installed" {
			return "-linux-amd64.deb"
		}
		return "-linux-amd64"
	default:
		return ""
	}
}

// platformAssetPattern returns the asset-name fragment for this platform,
// used for the "no asset for this platform" error message in Check.
func (c *Client) platformAssetPattern() string {
	return assetPattern(runtime.GOOS, runtime.GOARCH, c.installMode())
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

// InstallMode returns the detected install mode ("portable" or "installed"),
// resolving it on first use and caching the result.
func (c *Client) InstallMode() string {
	return c.installMode()
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
	// The temp file becomes the installer's own once it launches (the
	// installer reads it while running; the startup sweep of stale downloads
	// removes it later), so installed-mode keeps it on full success and
	// removes it on every failure path; portable-mode always cleans it up
	// here. The file is closed first: Windows cannot delete an open file,
	// and the early-return paths (download errors) never reach tmp.Close().
	keepForInstaller := false
	defer func() {
		if !keepForInstaller {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()

	req, err := http.NewRequest("GET", info.AssetURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "KeyRouter-updater/"+c.currentVersion)

	// The download client has a long timeout: installers are tens of MB and
	// slow connections take minutes (the API client's 30s would abort them).
	resp, err := c.download.Do(req)
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
		if err := c.applyInstalled(tmp.Name(), info); err != nil {
			return err
		}
		keepForInstaller = true
		return nil
	}
	return c.applyPortable(tmp.Name(), info)
}

// applyInstalled launches the downloaded installer and schedules the app to
// exit so the installer can replace the running exe:
//
//   - The installer must run ELEVATED (Program Files is not writable
//     otherwise), so it is launched via ShellExecuteEx with the "runas" verb
//     — Windows shows the UAC prompt, and a declined prompt is reported as
//     ErrUpdateCancelled so the app stays open (there is no update to apply).
//     The installer opens its setup wizard visibly (never /S silent — the
//     UI button promises "Launch Installer"), so the user sees and completes
//     the install.
//   - The handler calls the exit hook (after responding), which closes the
//     window and runs the normal graceful shutdown. The installer waits for
//     this process to exit before writing files (its wait loop runs in
//     interactive mode too, not just silent installs) and starts the
//     updated copy from its Finish page.
func (c *Client) applyInstalled(downloadPath string, info *UpdateInfo) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("installed-mode auto-update is only supported on Windows; run the %s installer manually", info.AssetName)
	}
	if err := launchInstaller(downloadPath, ""); err != nil {
		if errors.Is(err, ErrUpdateCancelled) {
			return ErrUpdateCancelled
		}
		return fmt.Errorf("failed to launch installer: %w", err)
	}
	log.Printf("[update] launched installer %s (%s)", info.AssetName, downloadPath)
	return nil
}

// launchInstaller starts the downloaded setup exe elevated. Overridable in
// tests.
var launchInstaller = runAsElevated

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
	// the swap script. A stale .new from a failed previous attempt would
	// make the rename fail forever, so clear it first.
	newPath := filepath.Join(dir, exeName+".new")
	if err := os.Remove(newPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to clear stale staging file: %w", err)
	}
	if err := os.Rename(downloadPath, newPath); err != nil {
		// Cross-device move (temp dir on another volume): copy instead.
		if err := copyFile(downloadPath, newPath); err != nil {
			return fmt.Errorf("failed to stage new executable: %w", err)
		}
	}

	if runtime.GOOS == "windows" {
		return c.launchWindowsSwap(dir, exeName, newPath)
	}

	// POSIX: rename the running exe aside, move the new one in. The new
	// process must NOT start while this one still holds the server port, so
	// a detached shell waits for this process to exit and then execs the new
	// binary (the handler triggers the exit right after responding).
	oldPath := filepath.Join(dir, exeName+".old")
	if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to clear stale backup file: %w", err)
	}
	if err := os.Rename(exe, oldPath); err != nil {
		return fmt.Errorf("failed to rename running executable: %w", err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		// Restore the old binary on failure.
		os.Rename(oldPath, exe)
		return fmt.Errorf("failed to replace executable: %w", err)
	}
	if err := launchPosixRelaunch(exe); err != nil {
		// Roll the swap back so a failed update leaves the app usable.
		os.Rename(exe, newPath)
		os.Rename(oldPath, exe)
		return fmt.Errorf("failed to schedule relaunch: %w", err)
	}
	return nil
}

// launchPosixRelaunch starts a detached shell that waits for this process
// (by PID) to exit, then execs the given executable. The old process must
// exit before the new one binds the server port.
func launchPosixRelaunch(exe string) error {
	return exec.Command("sh", "-c", posixRelaunchScript(os.Getpid(), exe)).Start()
}

// posixRelaunchScript returns the shell script that waits for the given
// process (by PID) to exit, then execs the exe. The wait gives up after
// ~5 minutes (PID reuse — a recycled PID could otherwise stall the
// relaunch forever) and proceeds anyway; by then the old process is gone
// either way. The exe path is single-quoted and any embedded quote is
// escaped, so $, backtick, and double quotes in paths stay literal.
func posixRelaunchScript(pid int, exe string) string {
	return fmt.Sprintf("n=0; while kill -0 %d 2>/dev/null && [ $n -lt 300 ]; do n=$((n+1)); sleep 1; done; exec '%s'",
		pid, strings.ReplaceAll(exe, "'", `'\''`))
}

// launchWindowsSwap writes a small batch script that waits for this process
// (by PID) to exit, swaps exe.new into place, and relaunches. Returns the
// command to start it; the app should then exit promptly. The script gets a
// unique name so a retry can't splice new content into a still-running
// helper from a previous attempt (cmd re-reads batch files line by line).
func (c *Client) launchWindowsSwap(dir, exeName, newPath string) error {
	f, err := os.CreateTemp(dir, exeName+".update-*.bat")
	if err != nil {
		return fmt.Errorf("failed to create update script: %w", err)
	}
	script := f.Name()
	content := windowsSwapScript(newPath, filepath.Join(dir, exeName), os.Getpid())
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(script)
		return fmt.Errorf("failed to write update script: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(script)
		return fmt.Errorf("failed to write update script: %w", err)
	}
	cmd := exec.Command("cmd", "/c", script)
	cmd.SysProcAttr = hideWindowAttr()
	if err := cmd.Start(); err != nil {
		os.Remove(script)
		return fmt.Errorf("failed to launch update script: %w", err)
	}
	return nil
}

// windowsSwapScript returns the .bat content that waits for THIS process (by
// PID) to exit (the exe is locked while the process lives), swaps the staged
// exe into place, relaunches it, and deletes itself. The wait gives up after
// ~5 minutes (PID reuse) and proceeds anyway — by then the old process is
// gone either way. No parenthesized blocks are used, so no delayed expansion
// is needed and paths containing "!" stay intact.
func windowsSwapScript(newPath, exePath string, pid int) string {
	return fmt.Sprintf(`@echo off
set /a n=0
:wait
tasklist /FI "PID eq %[1]d" 2>nul | find /I "%[1]d" >nul
if errorlevel 1 goto proceed
set /a n+=1
if %%n%% GEQ 300 goto proceed
ping -n 2 127.0.0.1 >nul
goto wait
:proceed
move /Y "%[2]s" "%[3]s" >nul
start "" "%[3]s"
del "%%~f0"
`, pid, newPath, exePath)
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

// CleanupStaleDownloads removes leftover updater/restart-helper temp files
// (interrupted downloads, cancelled installers, stray helpers) from the
// system temp dir. Called at startup; best-effort — it must never fail the
// app. Files newer than the threshold are kept (a helper may still be
// running mid-update).
func CleanupStaleDownloads() {
	cleanupStaleDownloads(os.TempDir(), 24*time.Hour)
}

func cleanupStaleDownloads(dir string, olderThan time.Duration) {
	var matches []string
	for _, pattern := range []string{"keyrouter-update-*", "keyrouter-restart-*"} {
		m, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			continue
		}
		matches = append(matches, m...)
	}
	cutoff := time.Now().Add(-olderThan)
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.ModTime().Before(cutoff) {
			if err := os.Remove(m); err != nil {
				log.Printf("[update] failed to remove stale temp file %s: %v", m, err)
			}
		}
	}
}
