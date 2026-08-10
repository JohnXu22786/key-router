package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// defaultDataDir returns the platform's per-user application-data directory
// for KeyRouter. User data (SQLite DB, rate-limit windows, logs) always lives
// here — never next to the executable — so it survives updates and behaves
// identically for every build type (portable binary, installer) and every
// platform. KEYROUTER_DATA overrides it (see resolveDataDir).
func defaultDataDir() string {
	return resolveDefaultDataDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
}

// resolveDefaultDataDir is the pure form of defaultDataDir, with the OS and
// the lookup functions injected so every platform's rule can be unit-tested
// on any host. It returns "" when no suitable directory can be determined.
func resolveDefaultDataDir(goos string, getenv func(string) string, home func() (string, error)) string {
	switch goos {
	case "windows":
		if d := getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "KeyRouter")
		}
		// Avoid the Roaming profile: SQLite over redirected/synced profiles
		// causes locking problems. Fall back to Local under the user profile.
		if h, err := home(); err == nil && h != "" {
			return filepath.Join(h, "AppData", "Local", "KeyRouter")
		}
	case "darwin":
		if h, err := home(); err == nil && h != "" {
			return filepath.Join(h, "Library", "Application Support", "KeyRouter")
		}
	default: // linux and everything else
		if d := getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
			return filepath.Join(d, "keyrouter")
		}
		if h, err := home(); err == nil && h != "" {
			return filepath.Join(h, ".local", "share", "keyrouter")
		}
	}
	return ""
}

// legacyDataDirs returns the directories that earlier versions may have used
// for user data, oldest first:
//
//  1. The executable-adjacent ./data directory (pre-0.2.0 builds).
//  2. The previous application-data directory, named after the old
//     "LocalRouter" product name (v0.1.x builds after the app-data move).
//
// These are checked at startup so an upgrade never loses the user's keys,
// routes, or billing history.
func legacyDataDirs(goos string, getenv func(string) string, home func() (string, error)) []string {
	var dirs []string
	if execPath, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(execPath), "data"))
	}
	switch goos {
	case "windows":
		if d := getenv("LOCALAPPDATA"); d != "" {
			dirs = append(dirs, filepath.Join(d, "LocalRouter"))
		}
	case "darwin":
		if h, err := home(); err == nil && h != "" {
			dirs = append(dirs, filepath.Join(h, "Library", "Application Support", "LocalRouter"))
		}
	default:
		if d := getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
			dirs = append(dirs, filepath.Join(d, "localrouter"))
		}
		if h, err := home(); err == nil && h != "" {
			dirs = append(dirs, filepath.Join(h, ".local", "share", "localrouter"))
		}
	}
	return dirs
}

// resolveDataDir picks the data directory: the KEYROUTER_DATA override wins,
// then the platform app-data directory, then a user-scoped last-resort temp
// dir (so the SQLite DB, which holds API keys, is never world-readable).
func resolveDataDir(getenv func(string) string, defaultDir func() string) string {
	if d := getenv("KEYROUTER_DATA"); d != "" {
		return d
	}
	if d := defaultDir(); d != "" {
		return d
	}
	user := getenv("USER")
	if user == "" {
		user = getenv("USERNAME")
	}
	if user == "" {
		user = "user"
	}
	return filepath.Join(os.TempDir(), "keyrouter-"+user)
}

// migrateLegacyData copies user data from the legacy data directories (see
// legacyDataDirs) into the new system data directory, exactly once. It is
// idempotent — once the new directory contains a database it does nothing —
// and copies instead of moving, so a failed migration can never destroy the
// original files. Files from the old "local-router.*" naming are renamed to
// the new "key-router.*" naming while copying.
func migrateLegacyData(newDir string, legacyDirs []string) {
	if newDir == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(newDir, "key-router.db")); err == nil {
		return // already migrated (or fresh install with data)
	}
	for _, legacyDir := range legacyDirs {
		if legacyDir == "" || legacyDir == newDir {
			continue
		}
		if fi, err := os.Stat(filepath.Join(legacyDir, "local-router.db")); err != nil || fi.IsDir() {
			continue // no legacy data in this dir
		}
		log.Printf("[main] migrating data from %s to %s", legacyDir, newDir)
		if err := copyLegacyFiles(legacyDir, newDir); err != nil {
			log.Printf("[main] data migration failed (continuing with fresh data): %v", err)
		}
		return // one source is enough — don't stack partial migrations
	}
}

// copyLegacyFiles copies flat files from legacyDir into newDir, renaming
// local-router.* files to key-router.*. Writes go through a temp file plus
// rename so a crash mid-copy can never leave a truncated key-router.db
// behind: the idempotency check keys on that file's existence, and a partial
// DB would block retries.
func copyLegacyFiles(legacyDir, newDir string) error {
	if err := os.MkdirAll(newDir, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue // flat files only (db, windows.json, logs)
		}
		// Skip log files: they are ephemeral, and on Windows renaming over
		// the open key-router.log handle (the app holds it with no
		// FILE_SHARE_DELETE) fails with a sharing violation, which would
		// abort the rest of the migration. A fresh log is opened anyway.
		if strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "local-router.") {
			name = "key-router." + strings.TrimPrefix(name, "local-router.")
		}
		src := filepath.Join(legacyDir, e.Name())
		dstTmp := filepath.Join(newDir, name+".tmp")
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstTmp, data, 0600); err != nil {
			return err
		}
		if err := os.Rename(dstTmp, filepath.Join(newDir, name)); err != nil {
			return err
		}
	}
	return nil
}
