package main

import "testing"

func TestAutostartRunValueQuotesExecutablePath(t *testing.T) {
	path := `C:\Program Files\Key Router\KeyRouter.exe`
	want := `"C:\Program Files\Key Router\KeyRouter.exe"`
	if got := autostartRunValue(path); got != want {
		t.Fatalf("autostartRunValue(%q) = %q, want %q", path, got, want)
	}
}
