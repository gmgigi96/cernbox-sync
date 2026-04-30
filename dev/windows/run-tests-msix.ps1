# run-tests-msix.ps1 - drive the cloudfiles test suite with MSIX package
# identity. Replaces `go test -tags windows ./...` for the cloudfiles
# package, since Phase 2's WinRT registration call
# (StorageProviderSyncRootManager.Register, via cernbox-cf.dll) requires
# package identity and `go test` from a normal shell can't provide it.
#
# Pipeline:
#   1. Build cernbox-cf.dll via cloudfiles\winrt\build.ps1.
#   2. `go test -c -tags windows ./cloudfiles -o cloudfiles.test.exe`.
#   3. Stage the test binary + DLL + assets + a tests-only AppxManifest into
#      a temp dir.
#   4. Add-AppxPackage -Register the staged dir as ch.cern.cernbox-sync-tests.
#      The manifest declares an uap5:AppExecutionAlias, so after registration
#      the test binary is callable as `cernbox-cloudfiles-test.exe` from any
#      PowerShell session, with package identity attached and stdout/stderr/
#      exit-code piped through normally.
#   5. Run `cernbox-cloudfiles-test.exe -test.v` (and any extra args), capture
#      its exit code.
#   6. Remove-AppxPackage in a finally{} block so a failed run never leaks
#      a stale registration.
#
# Run from the host with `make test-windows`, or directly inside the VM:
#   Z:\dev\windows\run-tests-msix.ps1 -- -test.run TestSyncRoot_RegisterConnect
#
# Anything after a literal `--` is forwarded verbatim to the test binary,
# matching the convention `go test` uses.

[CmdletBinding()]
param(
    [string]$RepoRoot,
    [string]$ManifestTpl,
    [string]$IconsDir,

    # Persistent staging dir kept outside the workspace so re-cloning /
    # wiping C:/workspace/cernbox-sync doesn't yank the install location
    # of a registered package.
    [string]$StageDir = 'C:\cernbox-sync-tests-stage',

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

    # Skip the cernbox-cf.dll rebuild step. Useful when iterating on Go
    # code without touching the C++ shim - shaves ~5s warm.
    [switch]$NoCfDll,

    # Skip the test-binary rebuild step. Useful when re-running the same
    # binary under different test args.
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

# Auto-version: 0.0.<minutes-since-epoch>.0. Monotonic, fits in the four-
# part MSIX version field. Avoids manual version bumps between runs.
if (-not $Version) {
    $minutes = [int]((Get-Date).ToUniversalTime() - [datetime]'1970-01-01').TotalMinutes
    $Version = "0.0.$minutes.0"
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

# -- 2. Compile the cloudfiles test binary ------------------------------------
$testExe = Join-Path $RepoRoot 'cloudfiles.test.exe'
if (-not $NoBuild) {
    Write-Host '== Compiling cloudfiles test binary =='
    Push-Location $RepoRoot
    try {
        & go test -c -tags windows -o $testExe ./cloudfiles
        if ($LASTEXITCODE -ne 0) {
            throw "go test -c failed with exit code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }
}
if (-not (Test-Path $testExe)) {
    throw "cloudfiles.test.exe not found at $testExe - rebuild without -NoBuild."
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
    Get-Process -Name cloudfiles.test, cernbox-cloudfiles-test -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 200
    Remove-AppxPackage -Package $existing.PackageFullName -ErrorAction SilentlyContinue
}
if (Test-Path $StageDir) {
    Remove-Item -Recurse -Force $StageDir
}
New-Item -Force -ItemType Directory $StageDir | Out-Null

Write-Host "Staging at $StageDir ..."
Copy-Item $testExe (Join-Path $StageDir 'cloudfiles.test.exe')
Copy-Item $cfDll   (Join-Path $StageDir 'cernbox-cf.dll')

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

# -- 4. Register --------------------------------------------------------------
Write-Host "Registering tests package ($IdentityName, $Version)..."
Add-AppxPackage -Register $manifestPath -ForceUpdateFromAnyVersion
$pkg = Get-AppxPackage $IdentityName
if (-not $pkg) {
    throw 'Add-AppxPackage -Register completed but Get-AppxPackage returned nothing.'
}
Write-Host "Registered as $($pkg.PackageFamilyName)"

# -- 5. Run --------------------------------------------------------------
#
# uap5:AppExecutionAlias creates a stub at:
#   %LOCALAPPDATA%\Microsoft\WindowsApps\cernbox-cloudfiles-test.exe
# WindowsApps is on PATH for interactive sessions but is NOT inherited by
# the SSH session by default. Resolve the stub explicitly so we don't depend
# on whatever PATH happens to be set on the runner.
$alias = Join-Path $env:LOCALAPPDATA 'Microsoft\WindowsApps\cernbox-cloudfiles-test.exe'
if (-not (Test-Path $alias)) {
    throw "AppExecutionAlias stub not found at $alias - registration may have silently dropped the alias. Check that Developer Mode (or the App Installer flow) is enabled."
}

# Forward anything passed after `--` on our command line straight to the
# test binary. Note that PowerShell strips a literal `--` token from $args
# unless we get hold of it via $MyInvocation.UnboundArguments / @args - the
# simplest portable approach is to require the caller to pass through
# extras explicitly. For the common case we hardcode -test.v -test.timeout=...
$testArgs = @('-test.v', "-test.timeout=$Timeout")
foreach ($a in $args) {
    $testArgs += $a
}

Write-Host ''
Write-Host "Running: cernbox-cloudfiles-test.exe $($testArgs -join ' ')"
Write-Host '------------------------------------------------------------'

try {
    & $alias @testArgs
    $exit = $LASTEXITCODE
}
finally {
    # -- 6. Unregister -------------------------------------------------------
    Write-Host ''
    Write-Host '------------------------------------------------------------'
    Write-Host 'Unregistering tests package...'
    Get-AppxPackage $IdentityName -ErrorAction SilentlyContinue |
        Remove-AppxPackage -ErrorAction SilentlyContinue
}

if ($exit -ne 0) {
    Write-Host "Tests failed with exit code $exit" -ForegroundColor Red
    exit $exit
}
Write-Host 'All cloudfiles tests passed.' -ForegroundColor Green
