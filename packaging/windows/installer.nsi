; KeyRouter Windows installer (NSIS)
; Built by .github/workflows/release.yml:
;   makensis /DTAG=<tag> /DVERSION=<numeric-version> /DAPP_EXE=<path-to-binary> installer.nsi
; The binary is installed as KeyRouter.exe; user data is stored in
; %LOCALAPPDATA%\KeyRouter (never next to the executable).

Unicode true
!include "MUI2.nsh"
!include "LogicLib.nsh"

Name "KeyRouter"
OutFile "..\..\dist\KeyRouter-${TAG}-windows-amd64-setup.exe"
InstallDir "$PROGRAMFILES64\KeyRouter"
RequestExecutionLevel admin

!define MUI_ABORTWARNING
!define MUI_ICON "..\icon\app-icon.ico"
!define MUI_UNICON "..\icon\app-icon.ico"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
; The Finish page starts the updated copy — through the (non-elevated)
; explorer shell rather than directly: a child of this elevated installer
; would run the app ELEVATED, and the app (tray, webview, localhost API)
; must run at the user's normal integrity.
!define MUI_FINISHPAGE_RUN_FUNCTION "LaunchUpdatedApp"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Section "Install"
  ; The in-app updater launches this installer elevated (setup wizard shown,
  ; not /S silent — the UI button promises "Launch Installer") and then
  ; exits the running copy so the exe can be replaced. The process may take
  ; a moment to shut down (in-flight streams finish first), so wait for it —
  ; up to ~3 minutes — before writing; otherwise the overwrite fails and the
  ; update looks like it "never happened". Manual installs with the app open
  ; get the same grace period to close it instead of an immediate error.
  ; tasklist's exit code is 0 whether or not a process matched, so the match
  ; is detected via find on the output.
  DetailPrint "Waiting for KeyRouter to exit (close the app to continue)..."
  StrCpy $0 0
  ${Do}
    ${If} $0 >= 300
      ${ExitDo} ; give up after ~2-3 min — the File below reports the failure
    ${EndIf}
    nsExec::ExecToStack 'cmd /c "tasklist /FI "IMAGENAME eq KeyRouter.exe" | find /I "KeyRouter.exe" >nul"'
    Pop $1 ; find's exit code (0 = KeyRouter.exe still running)
    Pop $2 ; output (unused)
    Sleep 300
    IntOp $0 $0 + 1
  ${LoopWhile} $1 = 0

  ; Per-machine install (HKLM, Program Files): shortcuts and uninstall keys
  ; go to the all-users context so every account sees them.
  SetShellVarContext all
  SetOutPath "$INSTDIR"
  File "/oname=KeyRouter.exe" "${APP_EXE}"
  WriteUninstaller "$INSTDIR\Uninstall.exe"

  ; Installed-build marker + version file: the auto-updater uses
  ; KeyRouter.installed to tell an installed copy (update via the setup
  ; installer) from a portable copy (replace the exe in place), and
  ; version.txt for the UI. This also keeps the install dir multi-file.
  FileOpen $0 "$INSTDIR\KeyRouter.installed" w
  FileClose $0
  FileOpen $0 "$INSTDIR\version.txt" w
  FileWrite $0 "${VERSION}"
  FileClose $0

  ; Start menu + desktop shortcuts
  CreateShortcut "$SMPROGRAMS\KeyRouter.lnk" "$INSTDIR\KeyRouter.exe"
  CreateShortcut "$DESKTOP\KeyRouter.lnk" "$INSTDIR\KeyRouter.exe"

  ; Add/Remove Programs entry
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\KeyRouter" "DisplayName" "KeyRouter"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\KeyRouter" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\KeyRouter" "Publisher" "KeyRouter"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\KeyRouter" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\KeyRouter" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\KeyRouter" "QuietUninstallString" '"$INSTDIR\Uninstall.exe" /S'
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\KeyRouter" "DisplayIcon" "$INSTDIR\KeyRouter.exe"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\KeyRouter" "NoModify" "1"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\KeyRouter" "NoRepair" "1"
SectionEnd

Section "Uninstall"
  SetShellVarContext all
  Delete "$INSTDIR\KeyRouter.exe"
  Delete "$INSTDIR\Uninstall.exe"
  Delete "$INSTDIR\KeyRouter.installed"
  Delete "$INSTDIR\version.txt"
  Delete "$SMPROGRAMS\KeyRouter.lnk"
  Delete "$DESKTOP\KeyRouter.lnk"
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\KeyRouter"
  RMDir "$INSTDIR"
  ; User data in %LOCALAPPDATA%\KeyRouter is intentionally left intact.
SectionEnd

; LaunchUpdatedApp starts the freshly installed copy on the Finish page.
; explorer.exe is the user's (non-elevated) shell, so the app inherits the
; user's integrity level instead of this elevated installer's; it returns
; immediately while the app starts in the background. The degenerate case
; (explorer not running) silently skips the launch.
Function LaunchUpdatedApp
  Exec '"$WINDIR\explorer.exe" "$INSTDIR\KeyRouter.exe"'
FunctionEnd
