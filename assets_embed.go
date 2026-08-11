package main

import (
	"embed"
)

// Assets embeds the application icon so the tray and any runtime UI can use
// it without relying on files next to the executable (portable/installed
// builds may have the data elsewhere).
//
//go:embed assets/app-icon.ico assets/app-icon-512.png
var assetsFS embed.FS

// trayIconData returns the Windows .ico bytes for the tray icon.
func trayIconData() []byte {
	data, err := assetsFS.ReadFile("assets/app-icon.ico")
	if err != nil {
		return nil
	}
	return data
}
