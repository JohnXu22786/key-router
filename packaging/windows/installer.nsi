; KeyRouter Windows installer (NSIS)
; Built by .github/workflows/release.yml:
;   makensis /DVERSION=<tag> /DAPP_EXE=<path-to-binary> installer.nsi
; The binary is installed as KeyRouter.exe; user data is stored in
; %LOCALAPPDATA%\KeyRouter (never next to the executable).

Unicode true
!include "MUI2.nsh"

Name "KeyRouter"
OutFile "..\..\dist\KeyRouter-${VERSION}-windows-amd64-setup.exe"
InstallDir "$PROGRAMFILES64\KeyRouter"
RequestExecutionLevel admin

!define MUI_ABORTWARNING
!define MUI_ICON "${NSISDIR}\Contrib\Graphics\Icons\modern-install.ico"
!define MUI_UNICON "${NSISDIR}\Contrib\Graphics\Icons\modern-uninstall.ico"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\KeyRouter.exe"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Section "Install"
  ; Per-machine install (HKLM, Program Files): shortcuts and uninstall keys
  ; go to the all-users context so every account sees them.
  SetShellVarContext all
  SetOutPath "$INSTDIR"
  File "/oname=KeyRouter.exe" "${APP_EXE}"
  WriteUninstaller "$INSTDIR\Uninstall.exe"

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
  Delete "$SMPROGRAMS\KeyRouter.lnk"
  Delete "$DESKTOP\KeyRouter.lnk"
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\KeyRouter"
  RMDir "$INSTDIR"
  ; User data in %LOCALAPPDATA%\KeyRouter is intentionally left intact.
SectionEnd
