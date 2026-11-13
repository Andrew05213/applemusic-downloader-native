param([string]$TemporaryRoot = '', [switch]$CloudRelease)
$ErrorActionPreference = 'Stop'

# The hosted runner is elevated: its token's default file owner is normally
# Administrators, and TEMP can contain RUNNER~1. Neither models an ordinary
# desktop user's private directories. Adjust only this disposable CI process's
# token default owner (not privileges, existing ACLs, accounts or machine policy).
Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Security.Principal;
public sealed class NativeCIOwnerScope : IDisposable {
    [DllImport("kernel32.dll")] static extern IntPtr GetCurrentProcess();
    [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool OpenProcessToken(IntPtr process, uint access, out IntPtr token);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool GetTokenInformation(IntPtr token, int kind, IntPtr data, int length, out int needed);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool SetTokenInformation(IntPtr token, int kind, IntPtr data, int length);
    IntPtr token, original;
    public NativeCIOwnerScope() {
        try {
            if (!OpenProcessToken(GetCurrentProcess(), 0x88, out token)) throw new Win32Exception();
            int needed;
            GetTokenInformation(token, 4, IntPtr.Zero, 0, out needed);
            if (needed <= 0) throw new Win32Exception();
            var saved = Marshal.AllocHGlobal(needed);
            if (!GetTokenInformation(token, 4, saved, needed, out needed)) { Marshal.FreeHGlobal(saved); throw new Win32Exception(); }
            original = saved;
            var sid = WindowsIdentity.GetCurrent().User;
            var bytes = new byte[sid.BinaryLength]; sid.GetBinaryForm(bytes, 0);
            var user = Marshal.AllocHGlobal(bytes.Length);
            var owner = Marshal.AllocHGlobal(IntPtr.Size);
            try {
                Marshal.Copy(bytes, 0, user, bytes.Length); Marshal.WriteIntPtr(owner, user);
                if (!SetTokenInformation(token, 4, owner, IntPtr.Size)) throw new Win32Exception();
            } finally { Marshal.FreeHGlobal(owner); Marshal.FreeHGlobal(user); }
        } catch { Dispose(); throw; }
    }
    public void Dispose() {
        if (original != IntPtr.Zero) {
            bool restored = SetTokenInformation(token, 4, original, IntPtr.Size);
            Marshal.FreeHGlobal(original); original = IntPtr.Zero;
            if (token != IntPtr.Zero) { CloseHandle(token); token = IntPtr.Zero; }
            if (!restored) throw new Win32Exception();
        } else if (token != IntPtr.Zero) { CloseHandle(token); token = IntPtr.Zero; }
    }
}
'@

$scope = [NativeCIOwnerScope]::new()
$previous = @{}
try {
    $root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
    if (!$TemporaryRoot) { $TemporaryRoot = Join-Path $root '.artifacts/ci' }
    $temporary = [IO.Path]::GetFullPath((Join-Path $TemporaryRoot ('windows-temp-' + [guid]::NewGuid())))
    New-Item -ItemType Directory -Path $temporary | Out-Null
    $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $owner = (Get-Acl -LiteralPath $temporary).GetOwner([Security.Principal.SecurityIdentifier]).Value
    if ($owner -ne $sid -or $temporary -match '~\d') { throw 'Native CI temporary directory identity mismatch' }
    foreach ($name in @('TEMP', 'TMP', 'GOTMPDIR')) {
        $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
        [Environment]::SetEnvironmentVariable($name, $temporary, 'Process')
    }
    Push-Location $(if ($CloudRelease) { $root } else { Join-Path $root 'apple-music-downloader' })
    try {
        if ($CloudRelease) {
            # The packaged app keeps its real owner/ACL checks. Scope creation
            # only in this disposable runner process and inherited children.
            & node electron/native-cloud-release.js windows
        } else {
            & go test -race -skip '^TestWrapperSession' ./...
        }
        $code = $LASTEXITCODE
    } finally { Pop-Location }
} finally {
    foreach ($name in $previous.Keys) { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') }
    $scope.Dispose()
}
exit $code
