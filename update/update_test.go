package update

import (
	"runtime"
	"testing"
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
