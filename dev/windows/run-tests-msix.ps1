# run-tests-msix.ps1 - drive Windows test suites that need MSIX package
# identity. Replaces `go test -tags windows ./...` for any package whose
# tests touch StorageProviderSyncRootManager (cernbox-cf.dll) - plain
# `go test` from a normal shell can't provide identity, so these
# packages need to run from inside a registered MSIX.
#
# Session 0 / Local System workaround
# -----------------------------------
# When the script is launched from an OpenSSH session (the default for
# `make test-windows`), the PowerShell process runs in Session 0 (the
# services session). Windows MSIX deployment refuses Session 0 - PLM
# (Process/Package Lifetime Manager) fails to initialise with
# 0x80070005 / "Failed to initialize PLM". See:
#   https://learn.microsoft.com/en-us/answers/questions/4172437/cant-install-apps-on-microsoft-store
#   https://github.com/PowerShell/Win32-OpenSSH/issues/1632
#
# Workaround: the deployment + test-execution block is wrapped in a
# self-dispatching helper (Invoke-InActiveUserSession) that, when called
# from Session 0, schedules itself as a one-shot task running in the
# active console user's session (Session 1, where testuser is auto-
# logged-in by autounattend's FirstLogonCommands). Output is captured
# to a log file in the staging dir; we tail it as the task runs and
# return the exit code. When the script is launched directly from an
# interactive desktop PowerShell (e.g. inside `make windows-vm-gui`),
# the dispatcher is a no-op and we run inline.
#
# Currently covers:
#   - ./cloudfiles  (alias: cernbox-cloudfiles-test.exe)
#   - ./daemon      (alias: cernbox-daemon-test.exe)
#
# Pipeline:
#   1. Build cernbox-cf.dll via cloudfiles\winrt\build.ps1.
#   2. `go test -c -tags windows ./<pkg> -o <pkg>.test.exe` for each package.
#   3. Stage all test binaries + DLL + assets + a tests-only AppxManifest
#      that declares one Application + AppExecutionAlias per binary.
#   4. Add-AppxPackage -Register the staged dir as ch.cern.cernbox-sync-tests.
#      Each alias stub becomes a normal command on PATH that carries
#      package identity into the test process.
#   5. Run each alias in turn, accumulating exit codes; first failure
#      sticks in the final exit code.
#   6. Remove-AppxPackage in a finally{} block so a failed run never leaks
#      a stale registration.
#
# Run from the host with `make test-windows`, or directly inside the VM:
#   Z:\dev\windows\run-tests-msix.ps1                   # run all packages
#   Z:\dev\windows\run-tests-msix.ps1 -Package cloudfiles
#   Z:\dev\windows\run-tests-msix.ps1 -test.run TestSyncRoot_RegisterConnect
#
# Anything not matching a known parameter is forwarded verbatim to every
# test binary (handy for -test.run, -test.v=false, etc.).

[CmdletBinding()]
param(
    [string]$RepoRoot,
    [string]$ManifestTpl,
    [string]$IconsDir,

    # Persistent staging dir kept outside the workspace so re-cloning /
    # wiping C:/workspace/cernbox-sync doesn't yank the install location
    # of a registered package. Under %LOCALAPPDATA% rather than C:\ root
    # so the AppX deployment service (running as SYSTEM) can read the
    # folder - root-level dirs on C:\ can have restrictive ACLs that
    # cause Add-AppxPackage -Register to fail with HRESULT 0x80070005.
    [string]$StageDir = (Join-Path $env:LOCALAPPDATA 'cernbox-sync-tests-stage'),

    # Identity values - kept distinct from the GUI MSIX so test runs don't
    # collide with a dev install of the GUI.
    [string]$IdentityName = 'ch.cern.cernbox-sync-tests',
    [string]$Publisher    = 'CN=CERN Box Sync Dev',

    # Bumped automatically per run via the build number so re-registration
    # doesn't trip "same version already installed" errors. Override to
    # pin a specific version when debugging.
    [string]$Version,

    # Test-binary timeout passed through to `go test`.
    [string]$Timeout = '300s',

    # Limit to a subset of packages. Default: all packages declared in
    # $packages below. Pass one or more shorthand names ("cloudfiles",
    # "daemon", ...) to run only those.
    [string[]]$Package,

    # Skip the cernbox-cf.dll rebuild step. Useful when iterating on Go
    # code without touching the C++ shim - shaves ~5s warm.
    [switch]$NoCfDll,

    # Skip the test-binary rebuild step. Useful when re-running the same
    # binaries under different test args.
    [switch]$NoBuild
)

$ErrorActionPreference = 'Stop'

$scriptRoot = $PSScriptRoot
if (-not $scriptRoot) {
    $scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
}
if (-not $RepoRoot)    { $RepoRoot    = (Resolve-Path (Join-Path $scriptRoot '..\..')).Path }
if (-not $ManifestTpl) { $ManifestTpl = Join-Path $scriptRoot 'tests-AppxManifest.tpl' }
if (-not $IconsDir)    { $IconsDir    = Join-Path $RepoRoot 'gui\src-tauri\icons' }

# Auto-version: each octet must fit in 16 bits (MSIX schema constraint),
# so naive "minutes since epoch" overflows. Encode as
# 0.<year-2020>.<dayOfYear>.<minuteOfDay> instead - monotonic within a
# year, rolls over each new year (harmless with -ForceUpdateFromAnyVersion
# on the install side).
if (-not $Version) {
    $now = (Get-Date).ToUniversalTime()
    $yearOffset  = $now.Year - 2020          # 6 in 2026, fits forever
    $dayOfYear   = $now.DayOfYear            # 1..366
    $minuteOfDay = $now.Hour * 60 + $now.Minute  # 0..1439
    $Version = "0.$yearOffset.$dayOfYear.$minuteOfDay"
}

# -- Package table ------------------------------------------------------------
#
# Each entry corresponds to one <Application> in tests-AppxManifest.tpl.
# Adding a new package here requires a matching <Application> + alias
# declaration in the manifest template - the alias name MUST match
# Alias below or the registration step will succeed but the test
# binary won't be reachable.
$packages = @(
    [pscustomobject]@{
        ShortName = 'cloudfiles'
        ImportPath = './cloudfiles'
        TestExe   = 'cloudfiles.test.exe'
        Alias     = 'cernbox-cloudfiles-test.exe'
    }
    [pscustomobject]@{
        ShortName = 'daemon'
        ImportPath = './daemon'
        TestExe   = 'daemon.test.exe'
        Alias     = 'cernbox-daemon-test.exe'
    }
)

if ($Package) {
    $selected = $Package
    $packages = $packages | Where-Object { $selected -contains $_.ShortName }
    if (-not $packages) {
        throw "No packages match -Package: $($selected -join ', '). Known: cloudfiles, daemon."
    }
}

# -- 1. Build cernbox-cf.dll --------------------------------------------------
if (-not $NoCfDll) {
    Write-Host '== Building cernbox-cf.dll =='
    & (Join-Path $RepoRoot 'cloudfiles\winrt\build.ps1') -OutDir (Join-Path $RepoRoot 'gui\src-tauri\binaries')
    if ($LASTEXITCODE -ne 0) {
        throw "cloudfiles\winrt\build.ps1 failed with exit code $LASTEXITCODE"
    }
}
$cfDll = Join-Path $RepoRoot 'gui\src-tauri\binaries\cernbox-cf.dll'
if (-not (Test-Path $cfDll)) {
    throw "cernbox-cf.dll not found at $cfDll - rebuild without -NoCfDll, or run cloudfiles\winrt\build.ps1 manually."
}

# -- 2. Compile every selected test binary ------------------------------------
foreach ($p in $packages) {
    $exePath = Join-Path $RepoRoot $p.TestExe
    if ($NoBuild) {
        if (-not (Test-Path $exePath)) {
            throw "$($p.TestExe) not found at $exePath - rebuild without -NoBuild."
        }
        continue
    }
    Write-Host "== Compiling $($p.TestExe) =="
    Push-Location $RepoRoot
    try {
        & go test -c -tags windows -o $exePath $p.ImportPath
        if ($LASTEXITCODE -ne 0) {
            throw "go test -c $($p.ImportPath) failed with exit code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }
}

# -- 3. Stage the package ----------------------------------------------------
#
# Stop any previously-registered tests package + drop its install dir before
# re-staging. Add-AppxPackage -Register chokes if the staging dir is being
# overwritten while the registration still references it.
$existing = Get-AppxPackage $IdentityName -ErrorAction SilentlyContinue
if ($existing) {
    Write-Host "Removing previous test package registration ($($existing.PackageFullName))..."
    # Kill any test process still holding handles open on stage files.
    $procNames = @()
    foreach ($p in $packages) {
        $procNames += [System.IO.Path]::GetFileNameWithoutExtension($p.TestExe)
        $procNames += [System.IO.Path]::GetFileNameWithoutExtension($p.Alias)
    }
    Get-Process -Name $procNames -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 200
    Remove-AppxPackage -Package $existing.PackageFullName -ErrorAction SilentlyContinue
}
if (Test-Path $StageDir) {
    Remove-Item -Recurse -Force $StageDir
}
New-Item -Force -ItemType Directory $StageDir | Out-Null

Write-Host "Staging at $StageDir ..."
Copy-Item $cfDll (Join-Path $StageDir 'cernbox-cf.dll')
foreach ($p in $packages) {
    Copy-Item (Join-Path $RepoRoot $p.TestExe) (Join-Path $StageDir $p.TestExe)
}

$assetsDir = Join-Path $StageDir 'Assets'
New-Item -Force -ItemType Directory $assetsDir | Out-Null
foreach ($logo in 'StoreLogo.png',
                  'Square44x44Logo.png',
                  'Square150x150Logo.png') {
    $src = Join-Path $IconsDir $logo
    if (-not (Test-Path $src)) { throw "Missing logo asset: $src" }
    Copy-Item $src (Join-Path $assetsDir $logo)
}

$manifest = Get-Content -Raw -Path $ManifestTpl
$manifest = $manifest.Replace('__VERSION__', $Version)
$manifestPath = Join-Path $StageDir 'AppxManifest.xml'
Set-Content -Path $manifestPath -Value $manifest -Encoding UTF8

# -- Generate the in-session payload ------------------------------------------
#
# The register/run/unregister phases all need to happen inside an
# interactive user session. We write them to a self-contained payload
# script so Invoke-InActiveUserSession can dispatch them to the active
# console session via Scheduled Task when we're called from SSH (Session
# 0). When we're called from an interactive PowerShell, the dispatcher
# notices and runs the payload inline.
$payloadPath = Join-Path $StageDir 'run-tests-payload.ps1'
$payloadLog  = Join-Path $StageDir 'run-tests-payload.log'

$payloadAliases = @($packages | ForEach-Object { "@{Name='$($_.ShortName)'; Alias='$($_.Alias)'}" })
$payloadAliasesLiteral = '@(' + ($payloadAliases -join ', ') + ')'
$payloadTestArgs = @('-test.v', "-test.timeout=$Timeout")
foreach ($extra in $args) {
    if ($null -ne $extra) { $payloadTestArgs += [string]$extra }
}
$quotedArgs = @($payloadTestArgs | Where-Object { $null -ne $_ } | ForEach-Object { "'" + ([string]$_).Replace("'", "''") + "'" })
$payloadTestArgsLiteral = '@(' + ($quotedArgs -join ', ') + ')'

# Note: this is a here-string in double-quoted form so $vars escaped with
# a backtick (`$) defer their evaluation to when the payload runs. We
# splice in literal values for $manifestPath, $IdentityName, the alias
# list, the test-args list, and the exit-sentinel path.
#
# Why no `exit N` at the end of the payload: in PS5, `exit` from a script
# launched via `powershell.exe -File` terminates the entire host
# process, which kills the wrapper before it can write the exit-code
# sentinel. So the payload writes the sentinel itself and lets control
# fall off the end naturally.
$exitFileForPayload = "$payloadLog.exit"
$payload = @"
`$ErrorActionPreference = 'Stop'
`$ExitFile = '$exitFileForPayload'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

try {
    "Payload running in session `$((Get-Process -Id `$PID).SessionId) as `$(whoami)"

    Write-Host 'Registering tests package...'
    Add-AppxPackage -Register '$manifestPath' -ForceUpdateFromAnyVersion
    `$pkg = Get-AppxPackage '$IdentityName'
    if (-not `$pkg) { throw 'Add-AppxPackage -Register completed but Get-AppxPackage returned nothing.' }
    Write-Host "Registered as `$(`$pkg.PackageFamilyName)"

    `$aliasInfo = $payloadAliasesLiteral
    `$testArgs  = $payloadTestArgsLiteral
    `$failed    = @()
    try {
        foreach (`$a in `$aliasInfo) {
            `$aliasPath = Join-Path `$env:LOCALAPPDATA "Microsoft\WindowsApps\`$(`$a.Alias)"
            if (-not (Test-Path `$aliasPath)) {
                throw "AppExecutionAlias stub not found at `$aliasPath - registration may have silently dropped the alias for `$(`$a.Name). Check Developer Mode."
            }
            Write-Host ''
            Write-Host '============================================================'
            Write-Host "Running `$(`$a.Name) tests: `$(`$a.Alias) `$(`$testArgs -join ' ')"
            Write-Host '============================================================'
            # 2>&1 forwards the test binary's stderr into the same
            # stream we're capturing, otherwise panics / "no test files"
            # warnings disappear into nowhere.
            & `$aliasPath @testArgs 2>&1
            `$exit = `$LASTEXITCODE
            if (`$exit -ne 0) {
                Write-Host ''
                Write-Host "`$(`$a.Name) tests FAILED (exit `$exit)"
                `$failed += `$a.Name
            } else {
                Write-Host ''
                Write-Host "`$(`$a.Name) tests passed."
            }
        }
    }
    finally {
        Write-Host ''
        Write-Host '------------------------------------------------------------'
        Write-Host 'Unregistering tests package...'
        Get-AppxPackage '$IdentityName' -ErrorAction SilentlyContinue | Remove-AppxPackage -ErrorAction SilentlyContinue
    }

    if (`$failed.Count -gt 0) {
        Write-Host "Failed packages: `$(`$failed -join ', ')"
        Set-Content -Path `$ExitFile -Value '1' -Encoding ASCII
    } else {
        Write-Host 'All MSIX-context tests passed.'
        Set-Content -Path `$ExitFile -Value '0' -Encoding ASCII
    }
}
catch {
    Write-Host "Payload raised: `$_"
    Set-Content -Path `$ExitFile -Value '1' -Encoding ASCII
}
"@
Set-Content -Path $payloadPath -Value $payload -Encoding UTF8

# -- Dispatcher: run the payload in the active user session if needed ---------
function Invoke-InActiveUserSession {
    param(
        [Parameter(Mandatory)] [string]$ScriptPath,
        [Parameter(Mandatory)] [string]$LogPath,
        [string]$User = 'testuser',
        [int]$TimeoutSeconds = 600
    )

    $exitFile = "$LogPath.exit"
    Remove-Item $LogPath, $exitFile -Force -ErrorAction SilentlyContinue

    $mySession = (Get-Process -Id $PID).SessionId
    if ($mySession -ne 0) {
        Write-Host "(Already in session $mySession; running payload inline.)"
        & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $ScriptPath
        if (Test-Path $exitFile) {
            return [int]((Get-Content $exitFile -Raw).Trim())
        }
        return $LASTEXITCODE
    }

    # Session 0 (services) - dispatch into the user's active console
    # session via a one-shot scheduled task. A wrapper around the payload
    # captures all output streams to $LogPath; the payload itself writes
    # the exit-code sentinel ($exitFile) before returning, so we just
    # poll for that file to appear.

    $taskName = 'cernbox-tests-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
    Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue |
        Unregister-ScheduledTask -Confirm:$false -ErrorAction SilentlyContinue

    # Force UTF-8 in the redirected log. Without this, PS5 defaults the
    # `*>` redirection to UTF-16 LE w/ BOM, which makes our tailing
    # reader (which uses StreamReader's default UTF-8) print garbled
    # output ("R e g i s t e r e d" with NULs between chars).
    #
    # The payload script writes the exit-code sentinel itself ($exitFile)
    # because in PS5 `exit N` from a -File script terminates the host
    # process before any wrapper code can run. So the wrapper's job is
    # just to redirect output - we don't try to capture LASTEXITCODE
    # here.
    $wrapper = "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; `$OutputEncoding = [System.Text.Encoding]::UTF8; `$PSDefaultParameterValues['Out-File:Encoding']='utf8'; & '$ScriptPath' *>&1 | Out-File -FilePath '$LogPath' -Encoding utf8 -Append"
    $action  = New-ScheduledTaskAction `
        -Execute 'powershell.exe' `
        -Argument "-NoProfile -ExecutionPolicy Bypass -Command `"$wrapper`""
    $principal = New-ScheduledTaskPrincipal `
        -UserId $User -RunLevel Highest -LogonType Interactive
    $settings  = New-ScheduledTaskSettingsSet `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit (New-TimeSpan -Seconds ($TimeoutSeconds + 60))

    Register-ScheduledTask -TaskName $taskName -Action $action `
        -Principal $principal -Settings $settings -Force | Out-Null

    Write-Host "Dispatching payload to ${User}'s active session via Scheduled Task '$taskName'"
    Write-Host '(SSH runs in session 0, but MSIX deployment requires session 1+.)'
    Start-ScheduledTask -TaskName $taskName

    # Tail the log as the task runs. Read with FileShare=ReadWrite so we
    # don't conflict with the writer; track our position so we don't
    # re-print already-shown content.
    $logPos  = 0
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ($true) {
        if (Test-Path $LogPath) {
            try {
                $stream = [System.IO.File]::Open($LogPath, 'Open', 'Read', 'ReadWrite')
                $stream.Position = $logPos
                $reader = New-Object System.IO.StreamReader($stream)
                $chunk  = $reader.ReadToEnd()
                $logPos = $stream.Position
                $reader.Close(); $stream.Close()
                if ($chunk) { Write-Host $chunk -NoNewline }
            } catch {
                # transient lock: try again next tick
            }
        }
        if (Test-Path $exitFile) { break }
        if ((Get-Date) -gt $deadline) {
            Write-Host ''
            Write-Host "(Timed out after $TimeoutSeconds s; cancelling task.)" -ForegroundColor Yellow
            Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
            break
        }
        Start-Sleep -Milliseconds 500
    }
    # Final flush.
    if (Test-Path $LogPath) {
        try {
            $stream = [System.IO.File]::Open($LogPath, 'Open', 'Read', 'ReadWrite')
            $stream.Position = $logPos
            $reader = New-Object System.IO.StreamReader($stream)
            $tail   = $reader.ReadToEnd()
            $reader.Close(); $stream.Close()
            if ($tail) { Write-Host $tail -NoNewline }
        } catch {}
    }

    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue

    if (Test-Path $exitFile) {
        $exitCode = [int]((Get-Content $exitFile -Raw).Trim())
        return $exitCode
    }
    Write-Host '(Scheduled task did not produce an exit-code sentinel; treating as failure.)' -ForegroundColor Yellow
    return 1
}

# -- 4-6. Register / Run / Unregister via dispatcher --------------------------
$payloadExit = Invoke-InActiveUserSession `
    -ScriptPath $payloadPath -LogPath $payloadLog `
    -User testuser -TimeoutSeconds 900

if ($payloadExit -ne 0) {
    Write-Host ''
    Write-Host "MSIX-context tests failed (payload exit $payloadExit)" -ForegroundColor Red
    exit $payloadExit
}
Write-Host ''
Write-Host 'All MSIX-context tests passed.' -ForegroundColor Green
