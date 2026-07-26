; ============================================================================
; ken.nsi — NSIS installer TEMPLATE for the Ken knowledge-base server (Windows).
;
; Ken has no native Windows service manager the way Linux has systemd, so this
; installer registers ken.exe as a Windows service using WinSW (a tiny, MIT-
; licensed service wrapper). The wrapper reads ken-service.xml (below) and runs
; `ken.exe serve` under the Local Service account, restarting it on failure.
;
; ----------------------------------------------------------------------------
; This is a clean, commented TEMPLATE — not a turnkey build. Before running
; makensis you must place these companion files next to this .nsi:
;
;   ken.exe            Windows build of ken:
;                        set GOOS=windows GOARCH=amd64
;                        set CGO_ENABLED=0
;                        go build -trimpath -ldflags "-s -w \
;                          -X github.com/Quest-ICT/ken/internal/version.Version=%VERSION%" \
;                          -o ken.exe ./cmd/ken
;
;   ken-service.exe    WinSW renamed to match its XML. Download WinSW.exe (the
;                      .NET-runtime-embedded build) from
;                      https://github.com/winsw/winsw/releases and rename it to
;                      ken-service.exe. WinSW auto-loads the same-basename
;                      ken-service.xml sitting beside it.
;
;   ken-service.xml    WinSW service definition (a ready-to-use sample is
;                      written to $INSTDIR at install time by this script — see
;                      the WriteServiceXml section; you may instead ship your own
;                      and remove that section).
;
; Build (cross-builds a Windows .exe from Linux, so it can run in CI/Wine):
;   makensis -DVERSION=0.1.0 deploy/ken.nsi
;
; Requires: makensis (NSIS 3.x). Everything else is stock.
; ============================================================================

Unicode true
!ifndef VERSION
  !define VERSION "0.1.0-dev"
!endif

!define APP        "ken"
!define APP_NAME   "Ken knowledge base"
!define PUBLISHER  "quest-ict"
!define SERVICE    "ken"                 ; Windows service id
!define URL        "http://localhost:8080/"
!define REGKEY     "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP}"
; Machine-wide state dir (a service account can write here, unlike Program Files).
; $PROGRAMDATA expands to C:\ProgramData at (un)install time.
!define APPDATA_KEN "$PROGRAMDATA\${APP}"

Name "${APP_NAME} ${VERSION}"
OutFile "ken-${VERSION}-windows-x64-setup.exe"
InstallDir "$PROGRAMFILES64\${APP}"
InstallDirRegKey HKLM "Software\${PUBLISHER}\${APP}" "InstallDir"
RequestExecutionLevel admin          ; service registration needs elevation
ShowInstDetails show
ShowUnInstDetails show

; --- Modern UI 2 ------------------------------------------------------------
!include "MUI2.nsh"
!include "LogicLib.nsh"
!define MUI_ABORTWARNING
; !define MUI_ICON   "ken.ico"        ; supply an icon if you have one
; !define MUI_UNICON "ken.ico"

!insertmacro MUI_PAGE_WELCOME
; !insertmacro MUI_PAGE_LICENSE "LICENSE.txt"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN_TEXT "Open the Ken web UI"
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_FUNCTION OpenWebUI
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

; ============================================================================
; Install
; ============================================================================
Section "Ken (required)" SecCore
  SectionIn RO
  SetOutPath "$INSTDIR"

  ; --- Program files --------------------------------------------------------
  File "ken.exe"
  File "ken-service.exe"        ; WinSW wrapper (basename must match the XML)
  ; File "ken-service.xml"      ; ship your own, or let the block below write one

  ; --- Data / logs directories (version-independent, survive upgrades) ------
  ; Kept under ProgramData so an unprivileged service account can write them.
  CreateDirectory "$APPDATA_KEN"
  CreateDirectory "$APPDATA_KEN\data"
  CreateDirectory "$APPDATA_KEN\logs"
  CreateDirectory "$APPDATA_KEN\backups"

  Call WriteServiceXml

  ; --- Register + start the Windows service via WinSW -----------------------
  DetailPrint "Registering the ${SERVICE} Windows service..."
  nsExec::ExecToLog '"$INSTDIR\ken-service.exe" install'
  Pop $0
  ${If} $0 != 0
    DetailPrint "WinSW install returned $0 (already installed?) — continuing."
  ${EndIf}
  nsExec::ExecToLog '"$INSTDIR\ken-service.exe" start'
  Pop $0

  ; --- Start Menu shortcut to the local URL ---------------------------------
  CreateDirectory "$SMPROGRAMS\${APP}"
  WriteINIStr "$SMPROGRAMS\${APP}\ken web UI.url" "InternetShortcut" "URL" "${URL}"
  CreateShortCut "$SMPROGRAMS\${APP}\Uninstall ken.lnk" "$INSTDIR\uninstall.exe"

  ; --- Registry / uninstaller -----------------------------------------------
  WriteRegStr HKLM "Software\${PUBLISHER}\${APP}" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "${REGKEY}" "DisplayName"     "${APP_NAME}"
  WriteRegStr HKLM "${REGKEY}" "DisplayVersion"  "${VERSION}"
  WriteRegStr HKLM "${REGKEY}" "Publisher"       "${PUBLISHER}"
  WriteRegStr HKLM "${REGKEY}" "UninstallString" "$INSTDIR\uninstall.exe"
  WriteRegStr HKLM "${REGKEY}" "DisplayIcon"     "$INSTDIR\ken.exe"
  WriteRegDWORD HKLM "${REGKEY}" "NoModify" 1
  WriteRegDWORD HKLM "${REGKEY}" "NoRepair" 1
  WriteUninstaller "$INSTDIR\uninstall.exe"

  DetailPrint ""
  DetailPrint "Next steps:"
  DetailPrint "  1. Create the first curator login (run in an elevated prompt):"
  DetailPrint '       "$INSTDIR\ken.exe" user add --name admin'
  DetailPrint "     (set KEN_DB to $APPDATA_KEN\data\ken.db first, or run from that dir)"
  DetailPrint "  2. Open ${URL} and sign in."
  DetailPrint "  3. Issue an agent token: ken.exe token add --actor my-agent"
SectionEnd

; Sample WinSW service definition. Points KEN_DB/logs at ProgramData so the
; service account can write, and runs `ken.exe serve` in the foreground (WinSW
; supervises the process directly).
Function WriteServiceXml
  FileOpen $0 "$INSTDIR\ken-service.xml" w
  FileWrite $0 '<service>$\r$\n'
  FileWrite $0 '  <id>${SERVICE}</id>$\r$\n'
  FileWrite $0 '  <name>${APP_NAME}</name>$\r$\n'
  FileWrite $0 '  <description>AI-first knowledge base (MCP + web).</description>$\r$\n'
  FileWrite $0 '  <executable>%BASE%\ken.exe</executable>$\r$\n'
  FileWrite $0 '  <arguments>serve</arguments>$\r$\n'
  FileWrite $0 '  <env name="KEN_DB" value="$APPDATA_KEN\data\ken.db" />$\r$\n'
  FileWrite $0 '  <env name="KEN_ADDR" value=":8080" />$\r$\n'
  FileWrite $0 '  <workingdirectory>$APPDATA_KEN</workingdirectory>$\r$\n'
  FileWrite $0 '  <logpath>$APPDATA_KEN\logs</logpath>$\r$\n'
  FileWrite $0 '  <log mode="roll-by-size"><sizeThreshold>10240</sizeThreshold><keepFiles>8</keepFiles></log>$\r$\n'
  FileWrite $0 '  <onfailure action="restart" delay="5 sec" />$\r$\n'
  FileWrite $0 '  <startmode>Automatic</startmode>$\r$\n'
  FileWrite $0 '</service>$\r$\n'
  FileClose $0
FunctionEnd

Function OpenWebUI
  ExecShell "open" "${URL}"
FunctionEnd

; ============================================================================
; Uninstall
; ============================================================================
Section "Uninstall"
  DetailPrint "Stopping + removing the ${SERVICE} service..."
  nsExec::ExecToLog '"$INSTDIR\ken-service.exe" stop'
  Pop $0
  nsExec::ExecToLog '"$INSTDIR\ken-service.exe" uninstall'
  Pop $0

  Delete "$INSTDIR\ken.exe"
  Delete "$INSTDIR\ken-service.exe"
  Delete "$INSTDIR\ken-service.xml"
  Delete "$INSTDIR\uninstall.exe"
  ; NOTE: the database/logs/backups under ProgramData are DELIBERATELY left in
  ; place so an uninstall/upgrade never destroys knowledge. Remove them by hand
  ; if you really want a clean wipe:  %ProgramData%\ken
  RMDir "$INSTDIR"

  Delete "$SMPROGRAMS\${APP}\ken web UI.url"
  Delete "$SMPROGRAMS\${APP}\Uninstall ken.lnk"
  RMDir  "$SMPROGRAMS\${APP}"

  DeleteRegKey HKLM "${REGKEY}"
  DeleteRegKey HKLM "Software\${PUBLISHER}\${APP}"
SectionEnd
