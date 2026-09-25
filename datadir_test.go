package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveDefaultDataDir(t *testing.T) {
	home := func() (string, error) { return "/home/user", nil }
	env := func(values map[string]string) func(string) string {
		return func(k string) string { return values[k] }
	}

	tests := []struct {
		name string
		goos string
		env  map[string]string
		home func() (string, error)
		want string // empty means resolveDefaultDataDir must return ""
	}{
		{"windows uses LOCALAPPDATA", "windows", map[string]string{"LOCALAPPDATA": `C:\Users\user\AppData\Local`}, home, filepath.Join(`C:\Users\user\AppData\Local`, "KeyRouter")},
		{"windows falls back to user profile Local (not Roaming)", "windows", nil, home, filepath.Join(`/home/user`, "AppData", "Local", "KeyRouter")},
		{"windows with no env and no home returns empty", "windows", nil, func() (string, error) { return "", os.ErrNotExist }, ""},
		{"darwin uses Application Support", "darwin", nil, home, filepath.Join("/home/user", "Library", "Application Support", "KeyRouter")},
		{"darwin without home returns empty", "darwin", nil, func() (string, error) { return "", os.ErrNotExist }, ""},
		// XDG_DATA_HOME must be absolute on every platform (Windows filepath
		// treats "/xdg" as relative, so use os.TempDir()).
		{"linux prefers XDG_DATA_HOME", "linux", map[string]string{"XDG_DATA_HOME": os.TempDir()}, home, filepath.Join(os.TempDir(), "keyrouter")},
		{"linux ignores relative XDG_DATA_HOME", "linux", map[string]string{"XDG_DATA_HOME": "relative/path"}, home, filepath.Join("/home/user", ".local", "share", "keyrouter")},
		{"linux falls back to ~/.local/share", "linux", nil, home, filepath.Join("/home/user", ".local", "share", "keyrouter")},
		{"unknown platform uses the unix rule", "freebsd", nil, home, filepath.Join("/home/user", ".local", "share", "keyrouter")},
		{"no env and no home returns empty", "linux", nil, func() (string, error) { return "", os.ErrNotExist }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveDefaultDataDir(tt.goos, env(tt.env), tt.home)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveDataDir(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(k string) string { return values[k] }
	}

	t.Run("KEYROUTER_DATA override wins", func(t *testing.T) {
		got, err := resolveDataDir(env(map[string]string{"KEYROUTER_DATA": "/custom"}), func() string { return "/default" })
		if err != nil {
			t.Fatal(err)
		}
		if got != "/custom" {
			t.Errorf("got %q, want /custom", got)
		}
	})

	t.Run("platform default used when override unset", func(t *testing.T) {
		got, err := resolveDataDir(env(nil), func() string { return "/default" })
		if err != nil {
			t.Fatal(err)
		}
		if got != "/default" {
			t.Errorf("got %q, want /default", got)
		}
	})

	t.Run("private temp dir fallback when nothing resolves", func(t *testing.T) {
		user := "resolve-test-" + filepath.Base(t.TempDir())
		getenv := env(map[string]string{"USER": user})
		got, err := resolveDataDir(getenv, func() string { return "" })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(got) })
		tempDir, err := filepath.EvalSymlinks(os.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(got) != tempDir {
			t.Errorf("fallback %q not inside %q", got, tempDir)
		}
		if filepath.Base(got) != "keyrouter-"+user {
			t.Errorf("fallback %q should retain the stable per-user name", got)
		}
		info, err := os.Stat(got)
		if err != nil {
			t.Fatalf("stat fallback directory: %v", err)
		}
		if !info.IsDir() {
			t.Fatalf("fallback %q is not a directory", got)
		}
		if runtime.GOOS != "windows" && (info.Mode().Perm()&0077 != 0 || info.Mode().Perm()&0700 != 0700) {
			t.Errorf("fallback permissions = %04o, want owner access only", info.Mode().Perm())
		}
		again, err := resolveDataDir(getenv, func() string { return "" })
		if err != nil {
			t.Fatal(err)
		}
		if again != got {
			t.Errorf("fallback changed across starts: first %q, then %q", got, again)
		}
		if err := os.WriteFile(filepath.Join(got, "persisted.db"), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
		if data, err := os.ReadFile(filepath.Join(again, "persisted.db")); err != nil || string(data) != "data" {
			t.Errorf("fallback data did not persist across resolution: data=%q err=%v", data, err)
		}
	})
}

func TestFallbackUserComponentCannotContainPathSeparators(t *testing.T) {
	got := fallbackUserComponent(`name/../../other\user`)
	if strings.ContainsAny(got, `/\`) {
		t.Fatalf("fallback user component %q contains a path separator", got)
	}
}

func TestTempFallbackRejectsPreexistingDirectoryWithLogSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cannot simulate a directory owned by a different Windows account")
	}
	user := "fallback-test-" + filepath.Base(t.TempDir())
	attackerDir := filepath.Join(os.TempDir(), "keyrouter-"+user)
	if err := os.Mkdir(attackerDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(attackerDir) })

	victimPath := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victimPath, []byte("victim data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victimPath, filepath.Join(attackerDir, "key-router.log")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	getenv := func(key string) string {
		if key == "USER" {
			return user
		}
		return ""
	}
	if dataDir, err := resolveDataDir(getenv, func() string { return "" }); err == nil {
		t.Fatalf("fallback accepted attacker-controlled directory %q", dataDir)
	}
	if got, err := os.ReadFile(victimPath); err != nil || string(got) != "victim data" {
		t.Errorf("victim file changed through log symlink: data=%q err=%v", got, err)
	}
}

func TestTempFallbackRejectsSymlinkedDirectory(t *testing.T) {
	user := "fallback-link-test-" + filepath.Base(t.TempDir())
	attackerDir := filepath.Join(os.TempDir(), "keyrouter-"+user)
	victimDir := t.TempDir()
	victimPath := filepath.Join(victimDir, "key-router.log")
	if err := os.WriteFile(victimPath, []byte("victim data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victimDir, attackerDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(attackerDir) })

	getenv := func(key string) string {
		if key == "USER" {
			return user
		}
		return ""
	}
	if dataDir, err := resolveDataDir(getenv, func() string { return "" }); err == nil {
		t.Fatalf("fallback accepted symlinked directory %q", dataDir)
	}
	if got, err := os.ReadFile(victimPath); err != nil || string(got) != "victim data" {
		t.Errorf("victim file changed through directory symlink: data=%q err=%v", got, err)
	}
}

func TestTempFallbackRejectsWritableNonStickyTempParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows temporary-parent ACLs are covered by platform tests")
	}
	user := "unsafe-parent-test-" + filepath.Base(t.TempDir())
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(shared, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0777); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", shared)
	getenv := func(key string) string {
		if key == "USER" {
			return user
		}
		return ""
	}
	if dataDir, err := resolveDataDir(getenv, func() string { return "" }); err == nil {
		_ = os.RemoveAll(dataDir)
		t.Fatalf("fallback accepted replaceable temporary parent %q", shared)
	}
}

// The core requirement: the data directory must never be inside the
// executable's directory, regardless of how the app was built or launched.
func TestDefaultDataDirIsNotNextToExecutable(t *testing.T) {
	dir := defaultDataDir()
	if dir == "" {
		t.Fatal("no default data dir on this platform")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exeDir := filepath.Clean(filepath.Dir(exe))
	cleanDir := filepath.Clean(dir)
	if cleanDir == exeDir || strings.HasPrefix(cleanDir, exeDir+string(filepath.Separator)) {
		t.Errorf("data dir %q must not be inside the executable dir %q", dir, exeDir)
	}
}

func TestLegacyDataDirs(t *testing.T) {
	home := func() (string, error) { return "/home/user", nil }
	env := func(values map[string]string) func(string) string {
		return func(k string) string { return values[k] }
	}

	t.Run("includes exe-adjacent data dir", func(t *testing.T) {
		dirs := legacyDataDirs("linux", env(nil), home)
		exe, _ := os.Executable()
		want := filepath.Join(filepath.Dir(exe), "data")
		if !contains(dirs, want) {
			t.Errorf("legacy dirs %v missing %q", dirs, want)
		}
	})

	t.Run("includes old LocalRouter app-data dirs", func(t *testing.T) {
		// XDG_DATA_HOME must be absolute on every platform (see
		// TestResolveDefaultDataDir) — os.TempDir() qualifies.
		linux := legacyDataDirs("linux", env(map[string]string{"XDG_DATA_HOME": os.TempDir()}), home)
		if !contains(linux, filepath.Join(os.TempDir(), "localrouter")) {
			t.Errorf("linux legacy dirs missing LocalRouter dir: %v", linux)
		}
		windows := legacyDataDirs("windows", env(map[string]string{"LOCALAPPDATA": `C:\Users\user\AppData\Local`}), home)
		if !contains(windows, filepath.Join(`C:\Users\user\AppData\Local`, "LocalRouter")) {
			t.Errorf("windows legacy dirs missing LocalRouter dir: %v", windows)
		}
		darwin := legacyDataDirs("darwin", env(nil), home)
		if !contains(darwin, filepath.Join("/home/user", "Library", "Application Support", "LocalRouter")) {
			t.Errorf("darwin legacy dirs missing LocalRouter dir: %v", darwin)
		}
	})
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestMigrateLegacyData(t *testing.T) {
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("copies legacy data into the new dir", func(t *testing.T) {
		legacy := t.TempDir()
		newDir := filepath.Join(t.TempDir(), "keyrouter")
		write(filepath.Join(legacy, "local-router.db"), "db")
		write(filepath.Join(legacy, "windows.json"), "{}")

		migrateLegacyData(newDir, []string{legacy})

		if got, err := os.ReadFile(filepath.Join(newDir, "key-router.db")); err != nil || string(got) != "db" {
			t.Errorf("db not migrated: %v %q", err, got)
		}
		if got, err := os.ReadFile(filepath.Join(newDir, "windows.json")); err != nil || string(got) != "{}" {
			t.Errorf("windows.json not migrated: %v %q", err, got)
		}
		// Copy, not move — the source stays intact.
		if _, err := os.Stat(filepath.Join(legacy, "local-router.db")); err != nil {
			t.Errorf("legacy source was removed: %v", err)
		}
	})

	t.Run("retries when a later file copy fails", func(t *testing.T) {
		legacy := t.TempDir()
		newDir := filepath.Join(t.TempDir(), "keyrouter")
		write(filepath.Join(legacy, "local-router.db"), "db")
		write(filepath.Join(legacy, "windows.json"), "{}")

		// A directory at the destination makes the windows.json rename fail.
		windowsPath := filepath.Join(newDir, "windows.json")
		if err := os.MkdirAll(windowsPath, 0700); err != nil {
			t.Fatal(err)
		}
		migrateLegacyData(newDir, []string{legacy})

		if _, err := os.Stat(filepath.Join(newDir, "key-router.db")); !os.IsNotExist(err) {
			t.Fatalf("database copied despite a later-file failure: %v", err)
		}
		if err := os.Remove(windowsPath); err != nil {
			t.Fatal(err)
		}

		migrateLegacyData(newDir, []string{legacy})

		if got, err := os.ReadFile(filepath.Join(newDir, "key-router.db")); err != nil || string(got) != "db" {
			t.Errorf("db not migrated after retry: %v %q", err, got)
		}
		if got, err := os.ReadFile(windowsPath); err != nil || string(got) != "{}" {
			t.Errorf("windows.json not migrated after retry: %v %q", err, got)
		}
	})

	t.Run("is a no-op when the new dir already has a database", func(t *testing.T) {
		legacy := t.TempDir()
		newDir := t.TempDir()
		write(filepath.Join(legacy, "local-router.db"), "old")
		write(filepath.Join(newDir, "key-router.db"), "new")

		migrateLegacyData(newDir, []string{legacy})

		if got, _ := os.ReadFile(filepath.Join(newDir, "key-router.db")); string(got) != "new" {
			t.Errorf("existing new-dir db overwritten: %q", got)
		}
	})

	t.Run("is a no-op without a legacy database", func(t *testing.T) {
		legacy := t.TempDir() // junk files, but no local-router.db
		write(filepath.Join(legacy, "junk.txt"), "x")
		newDir := filepath.Join(t.TempDir(), "keyrouter")

		migrateLegacyData(newDir, []string{legacy})

		if _, err := os.Stat(newDir); !os.IsNotExist(err) {
			t.Errorf("new dir created without a legacy db: %v", err)
		}
	})

	t.Run("same dir is a no-op", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, "key-router.db"), "db")
		migrateLegacyData(dir, []string{dir})
		if got, _ := os.ReadFile(filepath.Join(dir, "key-router.db")); string(got) != "db" {
			t.Errorf("db changed: %q", got)
		}
	})

	t.Run("a crashed previous run (leftover .tmp files) does not block retry", func(t *testing.T) {
		legacy := t.TempDir()
		newDir := t.TempDir() // simulated crash leftovers: partial tmp, no final db
		write(filepath.Join(legacy, "local-router.db"), "db")
		write(filepath.Join(newDir, "key-router.db.tmp"), "truncated garbage")

		migrateLegacyData(newDir, []string{legacy})

		if got, err := os.ReadFile(filepath.Join(newDir, "key-router.db")); err != nil || string(got) != "db" {
			t.Errorf("db not migrated after crash leftovers: %v %q", err, got)
		}
		// The stale tmp must have been consumed by the retry.
		if _, err := os.Stat(filepath.Join(newDir, "key-router.db.tmp")); !os.IsNotExist(err) {
			t.Errorf("stale .tmp file not replaced: %v", err)
		}
	})

	t.Run("log files are not copied", func(t *testing.T) {
		legacy := t.TempDir()
		newDir := filepath.Join(t.TempDir(), "keyrouter")
		write(filepath.Join(legacy, "local-router.db"), "db")
		write(filepath.Join(legacy, "local-router.log"), "old log content")

		migrateLegacyData(newDir, []string{legacy})

		if got, err := os.ReadFile(filepath.Join(newDir, "key-router.db")); err != nil || string(got) != "db" {
			t.Errorf("db not migrated: %v %q", err, got)
		}
		if _, err := os.Stat(filepath.Join(newDir, "key-router.log")); !os.IsNotExist(err) {
			t.Errorf("log file was copied (should not be): %v", err)
		}
	})

	t.Run("prefers newer app-data when both legacy databases exist", func(t *testing.T) {
		root := t.TempDir()
		xdgDataHome := filepath.Join(root, "xdg")
		execPath := filepath.Join(root, "bin", "key-router")
		env := func(key string) string {
			if key == "XDG_DATA_HOME" {
				return xdgDataHome
			}
			return ""
		}
		home := func() (string, error) { return "", os.ErrNotExist }
		legacyDirs := legacyDataDirsForExecutable("linux", env, home, execPath)
		appDataDir := filepath.Join(xdgDataHome, "localrouter")
		exeDataDir := filepath.Join(filepath.Dir(execPath), "data")
		if len(legacyDirs) != 2 || legacyDirs[0] != appDataDir || legacyDirs[1] != exeDataDir {
			t.Fatalf("legacy dirs = %v, want app-data %q before exe-adjacent %q", legacyDirs, appDataDir, exeDataDir)
		}
		write(filepath.Join(appDataDir, "local-router.db"), "newer app-data database")
		write(filepath.Join(exeDataDir, "local-router.db"), "older exe-adjacent database")
		newDir := filepath.Join(root, "keyrouter")

		migrateLegacyData(newDir, legacyDirs)

		got, err := os.ReadFile(filepath.Join(newDir, "key-router.db"))
		if err != nil {
			t.Fatalf("read migrated database: %v", err)
		}
		if string(got) != "newer app-data database" {
			t.Errorf("migrated database = %q, want newer app-data contents", got)
		}
	})
}
