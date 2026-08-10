package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// defaultDataDir returns the platform's per-user application-data directory
// for LocalRouter. User data (SQLite DB, rate-limit windows, logs) always
// lives here — never next to the executable — so it survives updates and
// behaves identically for every build type (portable binary, installer) and
// every platform. LOCALROUTER_DATA overrides it (see resolveDataDir).
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
			return filepath.Join(d, "LocalRouter")
		}
		// Avoid the Roaming profile: SQLite over redirected/synced profiles
		// causes locking problems. Fall back to Local under the user profile.
		if h, err := home(); err == nil && h != "" {
			return filepath.Join(h, "AppData", "Local", "LocalRouter")
		}
	case "darwin":
		if h, err := home(); err == nil && h != "" {
			return filepath.Join(h, "Library", "Application Support", "LocalRouter")
		}
	default: // linux and everything else
		if d := getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
			return filepath.Join(d, "localrouter")
		}
		if h, err := home(); err == nil && h != "" {
			return filepath.Join(h, ".local", "share", "localrouter")
		}
	}
	return ""
}

// resolveDataDir picks the data directory: the LOCALROUTER_DATA override
// wins, then the platform app-data directory, then a user-scoped last-resort
// temp dir (so the SQLite DB, which holds API keys, is never world-readable).
func resolveDataDir(getenv func(string) string, defaultDir func() string) string {
	if d := getenv("LOCALROUTER_DATA"); d != "" {
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
	return filepath.Join(os.TempDir(), "localrouter-"+user)
}

// migrateLegacyData copies user data from the old executable-adjacent "data"
// directory into the new system data directory, exactly once. It is
// idempotent — once the new directory contains a database it does nothing —
// and copies instead of moving, so a failed migration can never destroy the
// original files.
func migrateLegacyData(newDir, legacyDir string) {
	if newDir == "" || legacyDir == "" || newDir == legacyDir {
		return
	}
	if _, err := os.Stat(filepath.Join(newDir, "local-router.db")); err == nil {
		return // already migrated (or fresh install with data)
	}
	if fi, err := os.Stat(filepath.Join(legacyDir, "local-router.db")); err != nil || fi.IsDir() {
		return // no legacy data to migrate
	}
	log.Printf("[main] migrating data from %s to %s", legacyDir, newDir)
	if err := os.MkdirAll(newDir, 0700); err != nil {
		log.Printf("[main] data migration failed (continuing with fresh data): %v", err)
		return
	}
	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		log.Printf("[main] data migration failed (continuing with fresh data): %v", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue // flat files only (db, windows.json, logs)
		}
		// Skip log files: they are ephemeral, and on Windows renaming over
		// the open local-router.log handle (the app holds it with no
		// FILE_SHARE_DELETE) fails with a sharing violation, which would
		// abort the rest of the migration. A fresh log is opened anyway.
		if strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		src := filepath.Join(legacyDir, e.Name())
		data, err := os.ReadFile(src)
		if err != nil {
			log.Printf("[main] data migration failed at %s (continuing with fresh data): %v", src, err)
			return
		}
		// Write via temp file + rename so a crash mid-copy can never leave a
		// truncated local-router.db behind: the idempotency check above keys
		// on that file's existence, and a partial DB would block retries.
		dstTmp := filepath.Join(newDir, e.Name()+".tmp")
		if err := os.WriteFile(dstTmp, data, 0600); err != nil {
			log.Printf("[main] data migration failed at %s (continuing with fresh data): %v", dstTmp, err)
			return
		}
		if err := os.Rename(dstTmp, filepath.Join(newDir, e.Name())); err != nil {
			log.Printf("[main] data migration failed at %s (continuing with fresh data): %v", dstTmp, err)
			return
		}
	}
	log.Println("[main] data migration complete")
}
