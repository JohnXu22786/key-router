package main

import "testing"

func TestAutostartRunValueQuotesExecutablePath(t *testing.T) {
	path := `C:\Program Files\Key Router\KeyRouter.exe`
	want := `"C:\Program Files\Key Router\KeyRouter.exe"`
	if got := autostartRunValue(path); got != want {
		t.Fatalf("autostartRunValue(%q) = %q, want %q", path, got, want)
	}
}

func TestAutostartRegistryStringSize(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  uintptr
	}{
		{name: "ASCII", value: "KeyRouter.exe", want: 28},
		{name: "accented path", value: "C:\\Users\\Jos\u00e9\\KeyRouter.exe", want: 56},
		{name: "supplementary character", value: "\U0001f680", want: 6},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := autostartRegistryStringSize(test.value); got != test.want {
				t.Fatalf("autostartRegistryStringSize(%q) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}
