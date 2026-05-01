# make-msix.ps1 - package the cernbox-sync Tauri build into a signed MSIX.
#
# Runs on Windows (host or VM). Expects Tauri to have produced its standard
# bundle output under gui/src-tauri/target/release/. Produces a signed .msix
# at dev/windows/out/CERNBox-Sync_<version>_x64.msix that can be installed
# with Add-AppxPackage.
#
# Why a separate script instead of Tauri's built-in MSIX target:
#   Tauri 2's MSIX bundler is experimental and doesn't yet expose enough of
#   the manifest surface to declare a Cloud Files extension class. We hand-
#   roll the manifest from Package.appxmanifest.tpl, lay out the package
#   directory ourselves, and call MakeAppx/signtool from the Windows SDK.
#
# Pipeline:
#   1.   Resolve makeappx.exe + signtool.exe from the latest Windows 10 SDK
#        installed on this machine.
#   2.   Render Package.appxmanifest from the template.
#   3.   Stage everything that goes into the package under a temp directory
#        (manifest, exes, web assets, icons -> Assets\).
#   4.   MakeAppx pack /d <stage> /p <out.msix>
#   5.   signtool sign /fd SHA256 /a /f <pfx> /p <password> <out.msix>
#
# Inputs (parameters with defaults):
#   -ReleaseDir   gui/src-tauri/target/release   produced by `npm run tauri build`
#   -PfxPath      dev/windows/cert/cernbox-sync-dev.pfx   produced by new-dev-cert.ps1
#   -PfxPassword  cernbox-dev   matches new-dev-cert.ps1 default
#   -Version      0.1.0.0
#
# Manifest substitution variables match those in Package.appxmanifest.tpl.

[CmdletBinding()]
param(
    [string]$RepoRoot,
    [string]$ReleaseDir,
    [string]$ManifestTpl,
    [string]$IconsDir,
    [string]$OutDir,
    [string]$PfxPath,
    [string]$PfxPassword          = 'cernbox-dev',
    [string]$Version              = '0.1.0.0',
    [string]$IdentityName         = 'ch.cern.cernbox-sync',
    [string]$Publisher            = 'CN=CERN Box Sync Dev',
    [string]$DisplayName          = 'CERNBox Sync',
    [string]$PublisherDisplayName = 'CERN'
)

$ErrorActionPreference = 'Stop'

# Resolve the script's own directory robustly. $PSScriptRoot is empty when
# the script is invoked as `powershell.exe -File <relative-path>` (the case
# for our SSH-driven Makefile target on Windows PowerShell 5.1), so fall
# back to MyInvocation if needed.
$scriptRoot = $PSScriptRoot
if (-not $scriptRoot) {
    $scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
}

# Apply param defaults that depend on $scriptRoot, now that it's known.
if (-not $RepoRoot)    { $RepoRoot    = (Resolve-Path (Join-Path $scriptRoot '..\..')).Path }
if (-not $ManifestTpl) { $ManifestTpl = Join-Path $RepoRoot 'gui\src-tauri\windows\Package.appxmanifest.tpl' }
if (-not $IconsDir)    { $IconsDir    = Join-Path $RepoRoot 'gui\src-tauri\icons' }
if (-not $OutDir)      { $OutDir      = Join-Path $scriptRoot 'out' }
if (-not $PfxPath)     { $PfxPath     = Join-Path $scriptRoot 'cert\cernbox-sync-dev.pfx' }

# -- Resolve Windows SDK tools ------------------------------------------------

# Find the newest installed SDK that has both makeappx.exe and signtool.exe.
# Layout: C:\Program Files (x86)\Windows Kits\10\bin\<sdkver>\x64\<tool>.exe
$sdkRoot = 'C:\Program Files (x86)\Windows Kits\10\bin'
if (-not (Test-Path $sdkRoot)) {
    throw "Windows 10 SDK not found at $sdkRoot. Install via 'choco install windows-sdk-10-version-2104-windbg' or the SDK installer."
}

$makeappx = Get-ChildItem -Path $sdkRoot -Recurse -Filter 'makeappx.exe' -ErrorAction SilentlyContinue |
    Where-Object { $_.FullName -match '\\x64\\' } |
    Sort-Object FullName -Descending |
    Select-Object -First 1
if (-not $makeappx) { throw "makeappx.exe not found under $sdkRoot - install the Windows 10 SDK." }

$signtool = Get-ChildItem -Path $sdkRoot -Recurse -Filter 'signtool.exe' -ErrorAction SilentlyContinue |
    Where-Object { $_.FullName -match '\\x64\\' } |
    Sort-Object FullName -Descending |
    Select-Object -First 1
if (-not $signtool) { throw "signtool.exe not found under $sdkRoot - install the Windows 10 SDK." }

Write-Host "Using makeappx: $($makeappx.FullName)"
Write-Host "Using signtool: $($signtool.FullName)"

# -- Locate Tauri build output ------------------------------------------------
#
# Tauri respects $env:CARGO_TARGET_DIR (set by setup.ps1 to C:\dev-cache\
# cargo-target). Honor it if present, otherwise fall back to the repo-local
# target dir.
if (-not $ReleaseDir) {
    $cargoTarget = $env:CARGO_TARGET_DIR
    if (-not $cargoTarget) {
        $cargoTarget = Join-Path $RepoRoot 'gui\src-tauri\target'
    }
    $ReleaseDir = Join-Path $cargoTarget 'release'
}
if (-not (Test-Path $ReleaseDir)) {
    throw "Release dir not found: $ReleaseDir. Run 'npm run tauri build' or 'make gui' first."
}

# Identify the main Tauri exe. Tauri names it after productName ("CERNBox Sync"
# in tauri.conf.json), so the file should be "CERNBox Sync.exe". Be permissive
# in case productName is renamed - pick the first .exe that isn't the bundled
# sidecar daemon.
$mainExe = Get-ChildItem -Path $ReleaseDir -Filter '*.exe' -File |
    Where-Object { $_.Name -notmatch '^cernbox-syncd' } |
    Select-Object -First 1
if (-not $mainExe) { throw "No Tauri main exe found in $ReleaseDir." }

# The daemon sidecar lives next to the main exe in the release tree (Tauri
# stages externalBin entries there) - match the Rust target triple suffix
# Tauri appends.
$daemonExe = Get-ChildItem -Path $ReleaseDir -Filter 'cernbox-syncd*.exe' -File |
    Select-Object -First 1
if (-not $daemonExe) {
    # Fallback: gui/src-tauri/binaries/cernbox-syncd-<triple>.exe (where
    # run-dev.ps1 writes it). MSIX needs a stable name inside the package,
    # so we'll rename to cernbox-syncd.exe at stage time.
    $daemonExe = Get-ChildItem -Path (Join-Path $RepoRoot 'gui\src-tauri\binaries') -Filter 'cernbox-syncd*.exe' -File -ErrorAction SilentlyContinue |
        Select-Object -First 1
}
if (-not $daemonExe) { throw "cernbox-syncd.exe not found. Run 'go build ./cmd/cernbox-syncd' or 'make gui' first." }

# cernbox-cf.dll is the C++/WinRT registration shim built by
# cloudfiles/winrt/build.ps1 (invoked from run-build.ps1). It must live
# alongside the daemon inside the package so LoadLibrary("cernbox-cf.dll")
# resolves via the directory-of-the-exe rule.
$cfDll = Get-ChildItem -Path (Join-Path $RepoRoot 'gui\src-tauri\binaries') -Filter 'cernbox-cf.dll' -File -ErrorAction SilentlyContinue |
    Select-Object -First 1
if (-not $cfDll) {
    throw "cernbox-cf.dll not found in gui\src-tauri\binaries\. Run cloudfiles\winrt\build.ps1 (or run-build.ps1) first."
}

Write-Host "Main exe:    $($mainExe.FullName)"
Write-Host "Daemon exe:  $($daemonExe.FullName)"
Write-Host "CF shim:     $($cfDll.FullName)"

# -- Stage package contents ---------------------------------------------------

$stage = Join-Path $env:TEMP "cernbox-sync-msix-$([guid]::NewGuid().ToString('N'))"
New-Item -Force -ItemType Directory $stage | Out-Null
try {
    Copy-Item $mainExe.FullName   (Join-Path $stage $mainExe.Name)
    # Daemon goes in by a stable name regardless of triple suffix on disk -
    # this matches what providers_windows.go (and the IPC client) expect to
    # spawn from the package directory once Phase 2/3 wire it up.
    Copy-Item $daemonExe.FullName (Join-Path $stage 'cernbox-syncd.exe')
    Copy-Item $cfDll.FullName     (Join-Path $stage 'cernbox-cf.dll')

    # Copy WebView2/web assets if Tauri staged them next to the exe.
    foreach ($extra in 'WebView2Loader.dll', 'resources') {
        $src = Join-Path $ReleaseDir $extra
        if (Test-Path $src) {
            Copy-Item $src (Join-Path $stage $extra) -Recurse -Force
        }
    }

    # Assets\ - logos referenced by the manifest. Use the Square* PNGs the
    # Tauri icon generator already produced; MSIX is happy with them.
    $assetsDir = Join-Path $stage 'Assets'
    New-Item -Force -ItemType Directory $assetsDir | Out-Null
    foreach ($logo in 'StoreLogo.png',
                      'Square44x44Logo.png',
                      'Square71x71Logo.png',
                      'Square150x150Logo.png',
                      'Square310x310Logo.png') {
        $src = Join-Path $IconsDir $logo
        if (-not (Test-Path $src)) { throw "Missing logo asset: $src" }
        Copy-Item $src (Join-Path $assetsDir $logo)
    }

    # Render the manifest from the template.
    $manifest = Get-Content -Raw -Path $ManifestTpl
    $manifest = $manifest.Replace('__IDENTITY_NAME__',           $IdentityName)
    $manifest = $manifest.Replace('__PUBLISHER__',               $Publisher)
    $manifest = $manifest.Replace('__VERSION__',                 $Version)
    $manifest = $manifest.Replace('__DISPLAY_NAME__',            $DisplayName)
    $manifest = $manifest.Replace('__PUBLISHER_DISPLAY_NAME__',  $PublisherDisplayName)
    $manifest = $manifest.Replace('__EXECUTABLE__',              $mainExe.Name)
    Set-Content -Path (Join-Path $stage 'AppxManifest.xml') -Value $manifest -Encoding UTF8

    # -- Pack -----------------------------------------------------------------
    New-Item -Force -ItemType Directory $OutDir | Out-Null
    $msixPath = Join-Path $OutDir ("CERNBox-Sync_${Version}_x64.msix")
    if (Test-Path $msixPath) { Remove-Item $msixPath -Force }

    Write-Host ''
    Write-Host "Packing $msixPath ..."
    & $makeappx.FullName pack /d $stage /p $msixPath /o /nv
    if ($LASTEXITCODE -ne 0) { throw "makeappx failed (exit $LASTEXITCODE)" }

    # -- Sign -----------------------------------------------------------------
    if (-not (Test-Path $PfxPath)) {
        throw "PFX not found at $PfxPath - run dev/windows/new-dev-cert.ps1 first."
    }
    Write-Host "Signing $msixPath ..."
    & $signtool.FullName sign /fd SHA256 /a /f $PfxPath /p $PfxPassword $msixPath
    if ($LASTEXITCODE -ne 0) { throw "signtool failed (exit $LASTEXITCODE)" }

    Write-Host ''
    Write-Host "Built: $msixPath"
    Write-Host ''
    Write-Host 'Install on this machine with:'
    Write-Host "  Add-AppxPackage -Path '$msixPath'"
    Write-Host ''
    Write-Host 'Verify package identity from a packaged process:'
    Write-Host "  Get-AppxPackage $IdentityName"
}
finally {
    if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
}
