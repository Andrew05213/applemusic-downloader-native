# Windows desktop compatibility

> 历史记录：以下内容保留当时的实现、命令与验收语境，不代表当前发布状态。当前指南见[文档导航](README.md)，不要直接用本页旧命令安装或发布 2.0。
> 本页兼容矩阵属于 1.x WSL 桌面，不能当作 2.0 实机验收结论；旧源码入口见[维护分支说明](legacy/README.md#维护-1x)。

## Runtime targets

- Windows 10 21H2 / 22H2 x64, build 19044 or newer; prefer fully updated 22H2.
- Windows 11 x64 remains supported by the same desktop/Appliance path.
- Earlier Windows versions, 32-bit Windows and ARM64 are not supported.
- This is application compatibility, not an extension of Microsoft's OS
  servicing or security-update lifecycle. Windows Server is not a tested target.

The shared policy in `electron/windows-support.js` is used by both the Windows
capability probe and the WSL manager. Unknown/malformed release values and
non-integer helper build values fail closed. The new module must be included in
both online and offline desktop packages.

## WSL and hardware prerequisites

Firmware virtualization must be enabled and Windows must finish any pending
reboot. A current independently serviced WSL2 with systemd support is required;
legacy inbox WSL and WSL1 are not sufficient. A successful Windows version
check alone does not mean the Appliance is ready.

The existing wizard retains its explicit UAC-confirmed fixed commands:
`wsl.exe --install --no-distribution` and `wsl.exe --update`. The app does not
silently upgrade Windows, install a default Ubuntu distribution, or modify
another distribution. After prerequisites succeed, it imports only its own
verified Appliance. An old WSL that rejects `--version` remains a visible
`wsl_update_required` failure, not a successful detection.

The Windows stdio network bridge does not require Windows 11 mirrored
networking. Do not enable mirrored networking, alter DNS/routes or write a
global `.wslconfig` as a Windows 10 workaround. Helper identity, directory
locking, ownership, credential-FD and local proxy protections are unchanged.
Docker is not required on end-user machines; the documented maintainer build
hosts are a separate policy.

References:

- [Microsoft WSL installation prerequisites](https://learn.microsoft.com/en-us/windows/wsl/install)
- [Independently serviced WSL on Windows build 19044+](https://learn.microsoft.com/en-us/windows/wsl/compare-versions)
- [WSL systemd support](https://learn.microsoft.com/en-us/windows/wsl/systemd)

## Verification status and remaining acceptance

Automated fixtures cover Windows builds 19044, 19045, 22000, 22631 and 26100;
with/without the Windows helper, installed/absent owned Appliance, malformed
versions, disabled virtualization, reboot requirements, unsupported architecture
and an outdated WSL command. These tests do not run a Windows 10 kernel.

**Windows 10 physical/VM acceptance and freshly packaged installers remain
unverified.** Before describing a build as Win10-tested, record the exact OS,
WSL version and package SHA-256, and complete these checks on a dedicated host:

- Fresh online/offline desktop install; no WSL initially; accept UAC, finish
  the official install and reboot if requested. Do not treat unavailable WSL
  downloads as success.
- Import the dedicated Appliance; verify ownership, final manifest and helper
  identity. Verify the installed app no longer reports Windows 11-only support.
- Run only the credential-free network preflight for this acceptance; never
  infer real Apple login or media-download success from it.
- Use the Settings tab, protected dialogs, Chinese/spaced download directories,
  a non-C NTFS drive, and common display scales (100%, 125%, 150%).
- Verify denied UAC, firmware virtualization off, legacy WSL and pending reboot
  show the right recovery action without changing other distributions.

Existing installers are not changed by a source edit. Rebuild the desktop
packages before distributing a compatibility candidate. NOTICE redistribution
restrictions and `publicReleaseAllowed=false` remain unchanged.
