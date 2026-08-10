; LocalRouter Windows installer (NSIS)
; Built by .github/workflows/release.yml:
;   makensis /DVERSION=<tag> /DAPP_EXE=<path-to-binary> installer.nsi
; The binary is installed as LocalRouter.exe; user data is stored in
; %LOCALAPPDATA%\LocalRouter (never next to the executable).

Unicode true
!include "MUI2.nsh"

Name "LocalRouter"
OutFile "..\..\dist\LocalRouter-${VERSION}-windows-amd64-setup.exe"
InstallDir "$PROGRAMFILES64\LocalRouter"
RequestExecutionLevel admin

!define MUI_ABORTWARNING
!define MUI_ICON "${NSISDIR}\Contrib\Graphics\Icons\modern-install.ico"
!define MUI_UNICON "${NSISDIR}\Contrib\Graphics\Icons\modern-uninstall.ico"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\LocalRouter.exe"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Section "Install"
  ; Per-machine install (HKLM, Program Files): shortcuts and uninstall keys
  ; go to the all-users context so every account sees them.
  SetShellVarContext all
  SetOutPath "$INSTDIR"
  File "/oname=LocalRouter.exe" "${APP_EXE}"
  WriteUninstaller "$INSTDIR\Uninstall.exe"

  ; Start menu + desktop shortcuts
  CreateShortcut "$SMPROGRAMS\LocalRouter.lnk" "$INSTDIR\LocalRouter.exe"
  CreateShortcut "$DESKTOP\LocalRouter.lnk" "$INSTDIR\LocalRouter.exe"

  ; Add/Remove Programs entry
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\LocalRouter" "DisplayName" "LocalRouter"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\LocalRouter" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\LocalRouter" "Publisher" "LocalRouter"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\LocalRouter" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\LocalRouter" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\LocalRouter" "QuietUninstallString" '"$INSTDIR\Uninstall.exe" /S'
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\LocalRouter" "DisplayIcon" "$INSTDIR\LocalRouter.exe"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\LocalRouter" "NoModify" "1"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\LocalRouter" "NoRepair" "1"
SectionEnd

Section "Uninstall"
  SetShellVarContext all
  Delete "$INSTDIR\LocalRouter.exe"
  Delete "$INSTDIR\Uninstall.exe"
  Delete "$SMPROGRAMS\LocalRouter.lnk"
  Delete "$DESKTOP\LocalRouter.lnk"
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\LocalRouter"
  RMDir "$INSTDIR"
  ; User data in %LOCALAPPDATA%\LocalRouter is intentionally left intact.
SectionEnd
