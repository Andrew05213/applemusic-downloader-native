!include "x64.nsh"

!ifdef BUILD_UNINSTALLER
  Var AmdlUninstallExit
  Var AmdlUninstallResult

  ; The helper owns the trusted WSL command boundary and keeps stdout/stderr
  ; separate. NSIS receives only its fixed ASCII result token.
  Function un.AmdlRunUninstallAction
    StrCpy $AmdlUninstallExit 25
    StrCpy $AmdlUninstallResult ""
    nsExec::ExecToStack /TIMEOUT=600000 '"$INSTDIR\resources\bin\amdl-windows-probe.exe" $0'
    Pop $AmdlUninstallExit
    Pop $AmdlUninstallResult
  FunctionEnd
!endif

!macro customInstall
  DetailPrint "Apple Music Downloader desktop shell installed."
  DetailPrint "The dedicated WSL2 appliance will be prepared by the first-run wizard."
!macroend

!macro customUnInstall
  ${If} ${isUpdated}
    Goto amdl_uninstall_done
  ${EndIf}

  ${If} ${Silent}
    Goto amdl_preserve_wsl
  ${EndIf}

  MessageBox MB_ICONQUESTION|MB_YESNOCANCEL "是否保留专用 WSL 发行版中的任务数据库和 Wrapper 登录状态？选择‘是’会停止服务并保留任务数据；选择‘否’会注销 AppleMusicDownloader 发行版。Windows 音乐目录中的正式下载始终保留。" IDYES amdl_preserve_wsl IDNO amdl_remove_wsl
  Abort

  amdl_preserve_wsl:
    StrCpy $0 "uninstall-preserve-owned"
    Call un.AmdlRunUninstallAction
    ${If} $AmdlUninstallExit == 0
      ${If} $AmdlUninstallResult == "AMDL_UNINSTALL_PRESERVED"
        DetailPrint "Verified AppleMusicDownloader Appliance was stopped and terminated with task data preserved."
        Goto amdl_preserve_cleanup
      ${EndIf}
      ${If} $AmdlUninstallResult == "AMDL_UNINSTALL_ABSENT"
        DetailPrint "AppleMusicDownloader WSL distribution is not registered; continuing desktop uninstall."
        Goto amdl_preserve_cleanup
      ${EndIf}
    ${EndIf}
    ${If} $AmdlUninstallExit == 20
      DetailPrint "The same-name WSL distribution did not pass the owned Appliance identity proof; leaving it untouched."
      ${IfNot} ${Silent}
        MessageBox MB_ICONEXCLAMATION|MB_OK "无法确认 AppleMusicDownloader 发行版属于本应用，或检测到可能保留的运行时数据。为避免影响其他发行版或删除数据，卸载程序不会停止或注销它；桌面凭据仍会清除。"
      ${EndIf}
      Goto amdl_preserve_cleanup
    ${EndIf}
    ${If} $AmdlUninstallExit == 22
      ${If} ${Silent}
        DetailPrint "Unable to stop the verified WSL service; aborting silent uninstall with data preserved."
        SetErrorLevel 1
        Abort
      ${EndIf}
      MessageBox MB_ICONEXCLAMATION|MB_YESNO "无法停止 AppleMusicDownloader 后台服务。发行版可能已损坏，或仍有下载任务运行。是否继续卸载桌面应用并保留发行版及任务数据？选择‘否’会取消卸载。" IDYES amdl_preserve_cleanup
      Abort
    ${EndIf}
    ${If} $AmdlUninstallExit == 23
      DetailPrint "Unable to terminate the verified AppleMusicDownloader WSL distribution; preserving all task data."
      ${IfNot} ${Silent}
        MessageBox MB_ICONEXCLAMATION|MB_OK "已停止 AppleMusicDownloader 后台服务，但无法结束专用 WSL 发行版。桌面应用会继续卸载；任务数据库、Wrapper 状态和正式下载均会保留，发行版可能暂时继续运行。"
      ${EndIf}
      Goto amdl_preserve_cleanup
    ${EndIf}
    DetailPrint "The WSL uninstall helper could not acquire a safe lifecycle boundary; preserving Appliance data."
    SetErrorLevel 1
    Abort

    amdl_preserve_cleanup:
    Delete "$LOCALAPPDATA\AppleMusicDownloader\credentials.bin"
    Delete "$LOCALAPPDATA\AppleMusicDownloader\credentials.bin.tmp"
    DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "AppleMusicDownloader"
    DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run" "AppleMusicDownloader"
    Goto amdl_uninstall_done

  amdl_remove_wsl:
    StrCpy $0 "uninstall-remove-owned"
    Call un.AmdlRunUninstallAction
    ${If} $AmdlUninstallExit == 0
      ${If} $AmdlUninstallResult == "AMDL_UNINSTALL_REMOVED"
        DetailPrint "Verified AppleMusicDownloader Appliance was terminated and unregistered."
        Goto amdl_remove_state_cleanup
      ${EndIf}
      ${If} $AmdlUninstallResult == "AMDL_UNINSTALL_ABSENT"
        DetailPrint "AppleMusicDownloader WSL distribution is not registered and no owned install directory remains."
        Goto amdl_remove_state_cleanup
      ${EndIf}
    ${EndIf}
    ${If} $AmdlUninstallExit == 24
      DetailPrint "The verified Appliance could not be unregistered; preserving Appliance state for recovery."
      ${IfNot} ${Silent}
        MessageBox MB_ICONEXCLAMATION|MB_OK "无法注销 AppleMusicDownloader 专用 WSL 发行版。桌面应用仍会卸载，但 WSL 任务数据会保留；重新安装后可使用修复功能。Windows 音乐目录中的正式下载未受影响。"
      ${EndIf}
      Goto amdl_remove_credentials_only
    ${EndIf}
    DetailPrint "Refusing to unregister because the Appliance ownership or lifecycle proof did not complete."
    MessageBox MB_ICONSTOP|MB_OK "无法安全验证或停止 AppleMusicDownloader 专用 WSL 发行版。为避免删除同名的其他发行版或未完成更新的数据，任务数据未被删除，卸载已取消。"
    SetErrorLevel 1
    Abort

    amdl_remove_state_cleanup:
    ; The Windows helper removes these files while holding the lifecycle mutex.
    ; Do not delete them here: a new install could acquire the mutex after the
    ; helper returns and publish fresh recovery state before this NSIS process
    ; reaches the old cleanup lines.

    amdl_remove_credentials_only:
    Delete "$LOCALAPPDATA\AppleMusicDownloader\credentials.bin"
    Delete "$LOCALAPPDATA\AppleMusicDownloader\credentials.bin.tmp"
    DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "AppleMusicDownloader"
    DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run" "AppleMusicDownloader"
    Goto amdl_uninstall_done

  amdl_uninstall_done:
!macroend
