[CmdletBinding()]
param([switch]$Apply)
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
function Assert-InRoot([string]$Path) {
    $full = [IO.Path]::GetFullPath($Path)
    if (-not $full.StartsWith($root + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'archive_path_outside_repository'
    }
    return $full
}
$directories = @(Get-ChildItem -LiteralPath $root -Directory | Where-Object {
    $_.Name -like 'dist-native-*' -or $_.Name -like 'dist-v2-native-runtime-*'
})
$installers = @($directories | Where-Object {
    $_.Name -like 'dist-native-*' -and (Test-Path -LiteralPath (Join-Path $_.FullName 'native-build-evidence.json'))
} | Sort-Object LastWriteTime -Descending | Select-Object -First 2)
$runtimes = @($directories | Where-Object {
    $_.Name -like 'dist-v2-native-runtime-*' -and (Test-Path -LiteralPath (Join-Path $_.FullName 'bundle.json'))
} | Sort-Object LastWriteTime -Descending | Select-Object -First 4)
$keep = @($installers + $runtimes | ForEach-Object { $_.Name })
# Only executable paths are examined. Never collect/log process arguments or credentials.
$running = @(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath } | Select-Object -ExpandProperty ExecutablePath)
$skipped = @()
$candidates = @($directories | Where-Object {
    $item = $_
    if ($keep -contains $item.Name) { return $false }
    if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'archive_source_is_reparse_point' }
    $full = Assert-InRoot $item.FullName
    if (@($running | Where-Object { $_.StartsWith($full + '\', [StringComparison]::OrdinalIgnoreCase) }).Count) {
        $script:skipped += $item.Name
        return $false
    }
    # Refuse tracked or non-ignored content. This script is not a Git/worktree cleaner.
    $tracked = @(& git -C $root ls-files -- $item.Name)
    if ($LASTEXITCODE -ne 0 -or $tracked.Count) { throw 'archive_source_contains_tracked_files' }
    & git -C $root check-ignore --quiet -- $item.Name
    if ($LASTEXITCODE -ne 0) { throw 'archive_source_not_ignored' }
    return $true
})
$stamp = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ') + '-' + [Guid]::NewGuid().ToString('N').Substring(0,8)
$archive = Assert-InRoot (Join-Path $root ('.artifacts/history/' + $stamp))
$report = [ordered]@{ schemaVersion=1; repository=$root; archive=$archive; applied=[bool]$Apply; retained=$keep; activeSkipped=$skipped; moves=@() }
foreach ($item in $candidates) {
    $source = Assert-InRoot $item.FullName
    $destination = Assert-InRoot (Join-Path $archive $item.Name)
    $report.moves += [ordered]@{ source=$source; destination=$destination; status='planned' }
}
if ($Apply -and $report.moves.Count) {
    foreach ($relative in @('.artifacts', '.artifacts/history')) {
        $parent = Assert-InRoot (Join-Path $root $relative)
        if (Test-Path -LiteralPath $parent) {
            if ((Get-Item -LiteralPath $parent -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'archive_destination_is_reparse_point' }
        }
    }
    New-Item -ItemType Directory -Path $archive | Out-Null
    $manifest = Join-Path $archive 'manifest.json'
    $report | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $manifest -Encoding utf8
    foreach ($move in $report.moves) {
        $source = Assert-InRoot $move.source
        $destination = Assert-InRoot $move.destination
        if (Test-Path -LiteralPath $destination) { throw 'archive_destination_exists' }
        if ((Get-Item -LiteralPath $source -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'archive_source_is_reparse_point' }
        Move-Item -LiteralPath $source -Destination $destination
        $move.status = 'archived'
        $report | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $manifest -Encoding utf8
    }
}
$report | ConvertTo-Json -Depth 6
