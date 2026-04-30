# unregister-sync-roots.ps1 - tear down cernbox-sync OS-level sync-root
# registrations on Windows.
#
# When a folder is removed via the GUI/CLI on a current build, the daemon
# now calls CfUnregisterSyncRoot itself. This script exists for two cases
# the daemon can't handle:
#
#   1. Stale registrations from older builds (before remove-folder did the
#      unregister) that the daemon doesn't know about and won't touch on
#      its own.
#   2. The folder is gone from the config DB (manual cleanup, corrupted
#      state) but still shows up under HKCU\...\SyncRootManager and as a
#      cloud-aware folder in Explorer.
#
# Discovery
# ---------
# CfRegisterSyncRoot does NOT use the sync-root id we generate in Go; it
# auto-builds the registry key name from the ProviderName + user SID, so
# the actual keys look like 'cernbox-sync!<SID>!<token>', not
# 'cernbox-sync::<folder>'. We therefore identify "our" registrations by
# the ProviderName REG_SZ value under each key, not by the key name.
#
# The local path can live in any of:
#   - HKCU\...\<Id>\UserSyncRoots\<SID>             (REG_SZ value, common)
#   - HKCU\...\<Id>\<SID>\Path                      (REG_SZ subkey value)
#   - HKCU\...\<Id>\Path                            (occasional variant)
#
# Get-SyncRootRegistrations checks all three.
#
# Usage:
#   # See what's registered (no changes):
#   .\unregister-sync-roots.ps1 -List
#
#   # Dump the full registry contents under SyncRootManager for diagnostics:
#   .\unregister-sync-roots.ps1 -Dump
#
#   # Unregister a specific local root:
#   .\unregister-sync-roots.ps1 -LocalRoot 'C:\Users\me\CERNBox'
#
#   # Unregister every cernbox-sync sync root in one shot:
#   .\unregister-sync-roots.ps1 -All
#
#   # Override the provider-name filter (default: cernbox-sync):
#   .\unregister-sync-roots.ps1 -All -ProviderName 'cernbox-sync'
#
#   # If the API call fails for some entries, retry with -Force to scrub
#   # the registry and reparse point manually:
#   .\unregister-sync-roots.ps1 -All -Force

[CmdletBinding(DefaultParameterSetName = 'List')]
param(
    [Parameter(ParameterSetName = 'List')]
    [switch]$List,

    [Parameter(ParameterSetName = 'Dump', Mandatory = $true)]
    [switch]$Dump,

    [Parameter(ParameterSetName = 'One', Mandatory = $true)]
    [string]$LocalRoot,

    [Parameter(ParameterSetName = 'All', Mandatory = $true)]
    [switch]$All,

    # ProviderName filter applied to every key under SyncRootManager. Only
    # entries whose ProviderName REG_SZ value matches this string are
    # considered "ours". Case-insensitive.
    [string]$ProviderName = 'cernbox-sync',

    # When set, fall back to deleting the SyncRootManager registry entry
    # and stripping the IO_REPARSE_TAG_CLOUD reparse point with fsutil if
    # CfUnregisterSyncRoot returns an error. Use this to clean up after a
    # folder was deleted out from under the registration.
    [switch]$Force
)

$ErrorActionPreference = 'Stop'

if (-not ('Cf.Api' -as [type])) {
    Add-Type -Namespace Cf -Name Api -MemberDefinition @'
      [System.Runtime.InteropServices.DllImport("cldapi.dll",
        CharSet = System.Runtime.InteropServices.CharSet.Unicode)]
      public static extern int CfUnregisterSyncRoot(string syncRootPath);
'@
}

$syncRootKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Explorer\SyncRootManager'

# Look at every property of $regKey (Microsoft.Win32.RegistryKey) and
# return values that look like local file-system paths. Used as a last-
# resort path discovery when we can't find a structured Path/UserSyncRoots
# entry. A "path-like" value is a non-empty string starting with a drive
# letter or a UNC prefix.
function Get-PathLikeValues {
    param([Parameter(Mandatory)] $RegistryKey)
    foreach ($name in $RegistryKey.GetValueNames()) {
        $val = $RegistryKey.GetValue($name)
        if ($val -is [string] -and $val -match '^(?:[A-Za-z]:\\|\\\\)') {
            $val
        }
    }
}

# Walk the SyncRootManager registry hive and yield one record per
# (SyncRootId, LocalPath) combination for entries whose ProviderName
# matches $Filter. A single sync root key can have multiple local paths
# (rare, multi-user scenarios).
function Get-SyncRootRegistrations {
    param([string]$Filter)

    if (-not (Test-Path $syncRootKey)) { return }

    Get-ChildItem $syncRootKey | ForEach-Object {
        $idKey = $_
        $props = Get-ItemProperty -LiteralPath $idKey.PSPath -ErrorAction SilentlyContinue
        if (-not $props) { return }
        if ($Filter -and $props.ProviderName -ne $Filter) { return }

        # -- Find every local path associated with this SyncRootId ------------
        $paths = New-Object System.Collections.Generic.List[string]

        # 1. UserSyncRoots\<SID> values
        $userRoots = Join-Path $idKey.PSPath 'UserSyncRoots'
        if (Test-Path $userRoots) {
            $u = Get-ItemProperty -LiteralPath $userRoots
            foreach ($p in $u.PSObject.Properties) {
                if ($p.Name -notmatch '^PS' -and $p.Value -is [string] -and $p.Value) {
                    [void]$paths.Add($p.Value)
                }
            }
        }

        # 2. Path value at the SyncRootId key itself
        if ($props.Path) { [void]$paths.Add($props.Path) }

        # 3. Per-SID subkeys with a Path value
        foreach ($subName in $idKey.GetSubKeyNames()) {
            if ($subName -eq 'UserSyncRoots') { continue }
            $sub = Get-Item -LiteralPath (Join-Path $idKey.PSPath $subName) -ErrorAction SilentlyContinue
            if (-not $sub) { continue }
            $subProps = Get-ItemProperty -LiteralPath $sub.PSPath -ErrorAction SilentlyContinue
            if ($subProps -and $subProps.Path) {
                [void]$paths.Add($subProps.Path)
            }
        }

        # 4. Last-resort fallback: any path-shaped REG_SZ value anywhere on
        #    the key. Catches one-off layouts that don't match the patterns
        #    above; the user can still see what's registered.
        if ($paths.Count -eq 0) {
            $rawKey = $idKey.OpenSubKey('')
            if ($rawKey) {
                Get-PathLikeValues -RegistryKey $rawKey | ForEach-Object { [void]$paths.Add($_) }
                $rawKey.Close()
            }
        }

        $unique = $paths | Select-Object -Unique
        if ($unique.Count -eq 0) {
            # No path found at all - still emit the record so the user sees
            # the registration exists and can decide what to do.
            [pscustomobject]@{
                SyncRootId   = $idKey.PSChildName
                ProviderName = $props.ProviderName
                LocalPath    = $null
            }
        } else {
            foreach ($p in $unique) {
                [pscustomobject]@{
                    SyncRootId   = $idKey.PSChildName
                    ProviderName = $props.ProviderName
                    LocalPath    = $p
                }
            }
        }
    }
}

# Tear down a single registration. Tries the API first; if -Force is set
# and the API fails, falls back to manual registry + reparse-point scrub.
# Returns $true on success, $false otherwise.
function Remove-CernboxSyncRoot {
    param(
        [Parameter(Mandatory = $true)] [string]$LocalRoot,
        [string]$SyncRootId,
        [switch]$Force
    )

    Write-Host "Unregistering $LocalRoot ..." -NoNewline
    $hr = [Cf.Api]::CfUnregisterSyncRoot($LocalRoot)
    if ($hr -eq 0) {
        Write-Host ' OK'
        return $true
    }

    # 0x80070002 = HRESULT_FROM_WIN32(ERROR_FILE_NOT_FOUND): nothing at that
    # path. Treat as already-clean.
    if ($hr -eq -2147024894) {
        Write-Host ' already gone'
        return $true
    }

    Write-Host (' failed (HRESULT 0x{0:x8})' -f $hr) -ForegroundColor Yellow
    if (-not $Force) {
        Write-Warning 'Re-run with -Force to scrub the registry / reparse point manually.'
        return $false
    }

    if ($SyncRootId) {
        $key = Join-Path $syncRootKey $SyncRootId
        if (Test-Path $key) {
            Write-Host "  removing registry key $key"
            Remove-Item -Recurse -Force $key
        }
    }
    if (Test-Path $LocalRoot) {
        Write-Host "  stripping reparse point on $LocalRoot"
        & fsutil reparsepoint delete $LocalRoot | Out-Null
    }
    return $true
}

# -- Dispatch ----------------------------------------------------------------

switch ($PSCmdlet.ParameterSetName) {
    'List' {
        $roots = Get-SyncRootRegistrations -Filter $ProviderName
        if (-not $roots) {
            Write-Host "No sync roots with ProviderName='$ProviderName' registered."
            Write-Host "Run with -Dump to see all entries under HKCU\...\SyncRootManager."
            return
        }
        $roots | Format-Table -AutoSize SyncRootId, ProviderName, LocalPath
    }

    'Dump' {
        if (-not (Test-Path $syncRootKey)) {
            Write-Host "No SyncRootManager registry hive at $syncRootKey."
            return
        }
        Get-ChildItem $syncRootKey | ForEach-Object {
            $name = $_.PSChildName
            Write-Host ''
            Write-Host "=== $name ===" -ForegroundColor Cyan
            $props = Get-ItemProperty -LiteralPath $_.PSPath -ErrorAction SilentlyContinue
            if ($props) {
                $props.PSObject.Properties |
                    Where-Object { $_.Name -notmatch '^PS' } |
                    ForEach-Object { '  {0,-30} = {1}' -f $_.Name, $_.Value }
            }
            foreach ($subName in $_.GetSubKeyNames()) {
                Write-Host "  [subkey] $subName"
                $subPath = Join-Path $_.PSPath $subName
                $subProps = Get-ItemProperty -LiteralPath $subPath -ErrorAction SilentlyContinue
                if ($subProps) {
                    $subProps.PSObject.Properties |
                        Where-Object { $_.Name -notmatch '^PS' } |
                        ForEach-Object { '    {0,-28} = {1}' -f $_.Name, $_.Value }
                }
            }
        }
    }

    'One' {
        $match = Get-SyncRootRegistrations -Filter $ProviderName | Where-Object {
            $_.LocalPath -eq $LocalRoot -or
            $_.LocalPath -eq (Resolve-Path -LiteralPath $LocalRoot -ErrorAction SilentlyContinue).Path
        } | Select-Object -First 1
        $id = if ($match) { $match.SyncRootId } else { $null }
        $ok = Remove-CernboxSyncRoot -LocalRoot $LocalRoot -SyncRootId $id -Force:$Force
        if (-not $ok) { exit 1 }
    }

    'All' {
        $roots = Get-SyncRootRegistrations -Filter $ProviderName
        if (-not $roots) {
            Write-Host "No sync roots with ProviderName='$ProviderName' registered."
            return
        }
        $failed = 0
        foreach ($r in $roots) {
            if (-not $r.LocalPath) {
                Write-Warning "Skipping $($r.SyncRootId): no local path discovered. Re-run -Dump and clean manually."
                $failed++
                continue
            }
            if (-not (Remove-CernboxSyncRoot -LocalRoot $r.LocalPath -SyncRootId $r.SyncRootId -Force:$Force)) {
                $failed++
            }
        }
        if ($failed -gt 0) {
            Write-Warning "$failed registration(s) could not be removed cleanly. Re-run with -Force or scrub manually."
            exit 1
        }
        Write-Host 'All cernbox-sync sync roots unregistered.'
    }
}
