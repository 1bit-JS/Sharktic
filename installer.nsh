; Sharktic's part of the installer that mygo build makes (mygo.json,
; windows.installerInclude). mygo includes it after MUI2.nsh and its own
; languages, and runs the macros below.
;
; The installer asks for the app's language, English unless the user chose
; Korean before, and keeps it where the app reads it (language.go). It also
; quits Sharktic running from another folder, and points the start at login, if
; on, at the installed copy. The uninstaller also turns off advanced fan
; control, whose service would otherwise keep running from Program Files, and
; the start at login.

!include "LogicLib.nsh"
!include "StrFunc.nsh"
${UnStrLoc}

!insertmacro MUI_LANGUAGE "Korean"

!define SHARKTIC_SETTINGS "$APPDATA\Sharktic"
!define SHARKTIC_LANGUAGE "$APPDATA\Sharktic\language"
!define SHARKTIC_SERVICE_KEY "SYSTEM\CurrentControlSet\Services\SharkticService"
!define SHARKTIC_RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define SHARKTIC_APPROVED_KEY "Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run"
; mygo.json's identifier, by which the app starts at login.
!define SHARKTIC_LOGIN_ID "com.local.sharktic"

LangString MYGO_DESKTOP_SHORTCUT ${LANG_KOREAN} "바탕 화면에 바로 가기 만들기"
LangString SHARKTIC_SERVICE_LEFT ${LANG_ENGLISH} "Advanced fan control could not be turned off, so the Sharktic service is still installed. Run Sharktic again and turn it off from Advanced fan control…, then uninstall."
LangString SHARKTIC_SERVICE_LEFT ${LANG_KOREAN} "고급 팬 제어를 끄지 못해 Sharktic 서비스가 남아 있습니다. Sharktic을 다시 실행해 고급 팬 제어…에서 끈 뒤 제거해 주세요."
LangString SHARKTIC_RUNNING ${LANG_ENGLISH} "Sharktic is running from another folder. Close it to continue?"
LangString SHARKTIC_RUNNING ${LANG_KOREAN} "다른 폴더에서 실행 중인 Sharktic이 있습니다. 닫고 계속할까요?"

; Both languages say it, as it comes before either is chosen.
!define MUI_LANGDLL_WINDOWTITLE "Sharktic"
!define MUI_LANGDLL_INFO "Choose the language of Sharktic.$\r$\nSharktic의 언어를 고르세요."
; Only the languages the app has, whatever the system's.
!define MUI_LANGDLL_ALLLANGUAGES

!macro customInit
  ; The language chosen before, else English. A silent install, as an
  ; update, asks nothing and keeps the file as it is.
  StrCpy $LANGUAGE ${LANG_ENGLISH}
  ClearErrors
  FileOpen $0 "${SHARKTIC_LANGUAGE}" r
  ${IfNot} ${Errors}
    FileRead $0 $1 2
    FileClose $0
    ${If} $1 == "ko"
      StrCpy $LANGUAGE ${LANG_KOREAN}
    ${EndIf}
  ${EndIf}
  !insertmacro MUI_LANGDLL_DISPLAY
  Call CloseOtherSharktic
!macroend

; The copies of Sharktic of this session that run from elsewhere, as of a zip,
; in $p of PowerShell: mygo's CloseApp quits only those of $INSTDIR, and one
; left running keeps the installed app from starting, as it runs once. Not
; the service, of session 0. "$$" is PowerShell's "$".
!define SHARKTIC_FIND "$$s = (Get-Process -Id $$PID).SessionId; $$p = @(Get-CimInstance Win32_Process | Where-Object { $$_.Name -eq 'Sharktic.exe' -and $$_.SessionId -eq $$s })"

; Quits them, after asking unless silent, or quits the installer.
Function CloseOtherSharktic
  Push $0
  Push $1
  StrCpy $1 "$WINDIR\Sysnative\WindowsPowerShell\v1.0\powershell.exe"
  IfFileExists $1 +2
  StrCpy $1 "$SYSDIR\WindowsPowerShell\v1.0\powershell.exe"
  IfFileExists $1 0 done
  nsExec::Exec `"$1" -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command "${SHARKTIC_FIND}; if ($$p.Count) { exit 2 }"`
  Pop $0
  StrCmp $0 "2" 0 done
  IfSilent close
  MessageBox MB_OKCANCEL|MB_ICONEXCLAMATION "$(SHARKTIC_RUNNING)" IDOK close
  Abort
  close:
  ; A tray app has no window to close, so it is stopped, as CloseApp does
  ; after waiting; the app clears the dead icons when it starts again.
  nsExec::Exec `"$1" -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command "${SHARKTIC_FIND}; $$p | ForEach-Object { Stop-Process -Id $$_.ProcessId -Force }; Start-Sleep -Milliseconds 500"`
  Pop $0
  done:
  Pop $1
  Pop $0
FunctionEnd

!macro customInstall
  ${IfNot} ${Silent}
    CreateDirectory "${SHARKTIC_SETTINGS}"
    FileOpen $0 "${SHARKTIC_LANGUAGE}" w
    ${If} $LANGUAGE == ${LANG_KOREAN}
      FileWrite $0 "ko$\n"
    ${Else}
      FileWrite $0 "en$\n"
    ${EndIf}
    FileClose $0
  ${EndIf}

  ; Start at login, if on, starts the installed copy from now on: one started
  ; from elsewhere, as of a zip or an older build, would run instead of it, as
  ; the app runs once. Task Manager's off, in StartupApproved, stays.
  ReadRegStr $0 HKCU "${SHARKTIC_RUN_KEY}" "${SHARKTIC_LOGIN_ID}"
  ${If} $0 != ""
    WriteRegStr HKCU "${SHARKTIC_RUN_KEY}" "${SHARKTIC_LOGIN_ID}" '"$INSTDIR\Sharktic.exe" --mygo-opened-at-login'
  ${EndIf}
!macroend

!macro customUnInstall
  ; The installed app turns advanced fan control off, as administrator.
  ; PawnIO stays, as other programs may use it.
  ClearErrors
  ReadRegStr $0 HKLM "${SHARKTIC_SERVICE_KEY}" "ImagePath"
  ${IfNot} ${Errors}
    ExecShellWait "runas" "$INSTDIR\Sharktic.exe" "--disable-advanced" SW_HIDE
    ClearErrors
    ReadRegStr $0 HKLM "${SHARKTIC_SERVICE_KEY}" "ImagePath"
    ${IfNot} ${Errors}
      MessageBox MB_OK|MB_ICONEXCLAMATION "$(SHARKTIC_SERVICE_LEFT)" /SD IDOK
    ${EndIf}
  ${EndIf}

  ; Start at login, if it starts this copy: another copy, as of a zip, may
  ; start at login too.
  ReadRegStr $0 HKCU "${SHARKTIC_RUN_KEY}" "${SHARKTIC_LOGIN_ID}"
  ${If} $0 != ""
    ${UnStrLoc} $1 $0 "$INSTDIR\" ">"
    ${If} $1 != ""
      DeleteRegValue HKCU "${SHARKTIC_RUN_KEY}" "${SHARKTIC_LOGIN_ID}"
      DeleteRegValue HKCU "${SHARKTIC_APPROVED_KEY}" "${SHARKTIC_LOGIN_ID}"
    ${EndIf}
  ${EndIf}
!macroend
