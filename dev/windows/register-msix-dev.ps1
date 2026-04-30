# register-msix-dev.ps1 - fast dev iteration on MSIX-context features.
#
# Stages the GUI + daemon into a persistent on-disk directory and registers
# it via Add-AppxPackage -Register against the staged AppxManifest.xml.
# Skips MakeAppx pack and signtool sign entirely - those are only needed
# for distribution. -Register against an unpacked folder is the canonical
# dev workflow on Windows for MSIX iteration.
#
# Round-trip when cargo + Go caches are warm: ~15s. Compare to make-msix.ps1
# which adds ~30s of pack+sign on top.
#
# Where to run from
# -----------------
# Host:  `make gui-msix-dev`  - tars the source into the VM, builds, registers.
# VM:    `Z:\dev\windows\register-msix-dev.ps1`  - if the VM was started
#        with `make windows-vm-gui` (host repo mounted as Z:), this skips
#        the tar/upload step entirely.
#
# In both cases the registered package's install location is the staging
# directory ($StageDir), so subsequent rebuilds that overwrite files in
# place are picked up the next time the app is launched - no need to
# re-register unless the manifest itself changes.

[CmdletBinding()]
param(
    [string]$RepoRoot,
    [string]$ReleaseDir,
    [string]$ManifestTpl,
    [string]$IconsDir,

    # Persistent staging dir. Lives outside the repo so re-cloning / wiping
    # the workspace doesn't yank the install location from under the
    # registered package. Override if you want multiple staged builds side
    # by side.
    [string]$StageDir             = 'C:\cernbox-sync-msix-stage',

    [string]$ServerUrl            = '',
    [string]$Version              = '0.1.0.0',
    [string]$IdentityName         = 'ch.cern.cernbox-sync',
    [string]$Publisher            = 'CN=CERN Box Sync Dev',
    [string]$DisplayName          = 'CERNBox Sync',
    [string]$PublisherDisplayName = 'CERN',

    # Skip the build step. Useful when iterating on the manifest only - you
    # know the exes are up to date and just want to re-stage and re-register.
    [switch]$NoBuild
)

$ErrorActionPreference = 'Stop'

# -- Resolve script root (PSScriptRoot empty inside param() defaults on PS5) --
$scriptRoot = $PSScriptRoot
if (-not $scriptRoot) {
    $scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
}

if (-not $RepoRoot)    { $RepoRoot    = (Resolve-Path (Join-Path $scriptRoot '..\..')).Path }
if (-not $ManifestTpl) { $ManifestTpl = Join-Path $RepoRoot 'gui\src-tauri\windows\Package.appxmanifest.tpl' }
if (-not $IconsDir)    { $IconsDir    = Join-Path $RepoRoot 'gui\src-tauri\icons' }

# -- Build (unless told otherwise) --------------------------------------------
if (-not $NoBuild) {
    Write-Host '== Building daemon + GUI =='
    $buildArgs = @()
    if ($ServerUrl) { $buildArgs += @('-ServerUrl', $ServerUrl) }
    & (Join-Path $scriptRoot 'run-build.ps1') @buildArgs
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

# -- Locate Tauri build output (same logic as make-msix.ps1) ------------------
if (-not $ReleaseDir) {
    $cargoTarget = $env:CARGO_TARGET_DIR
    if (-not $cargoTarget) { $cargoTarget = Join-Path $RepoRoot 'gui\src-tauri\target' }
    $ReleaseDir = Join-Path $cargoTarget 'release'
}
if (-not (Test-Path $ReleaseDir)) {
    throw "Release dir not found: $ReleaseDir. Run 'npm run tauri build' or 'make gui' first, or drop -NoBuild."
}

$mainExe = Get-ChildItem -Path $ReleaseDir -Filter '*.exe' -File |
    Where-Object { $_.Name -notmatch '^cernbox-syncd' } |
    Select-Object -First 1
if (-not $mainExe) { throw "No Tauri main exe found in $ReleaseDir." }

$daemonExe = Get-ChildItem -Path $ReleaseDir -Filter 'cernbox-syncd*.exe' -File |
    Select-Object -First 1
if (-not $daemonExe) {
    $daemonExe = Get-ChildItem -Path (Join-Path $RepoRoot 'gui\src-tauri\binaries') -Filter 'cernbox-syncd*.exe' -File -ErrorAction SilentlyContinue |
        Select-Object -First 1
}
if (-not $daemonExe) { throw "cernbox-syncd.exe not found." }

Write-Host "Main exe:    $($mainExe.FullName)"
Write-Host "Daemon exe:  $($daemonExe.FullName)"

# -- Stage to a persistent directory ------------------------------------------
#
# We can't just blindly Remove-Item on $StageDir while the package is
# registered: any running app process holds locks on the exes. Easiest
# is: unregister first, wipe, re-stage, re-register. Add-AppxPackage
# -Register with -ForceUpdateFromAnyVersion *should* handle the in-place
# replace transparently, but in practice it gets unhappy if files in the
# install location are removed underneath it. Unregister-then-register
# is bulletproof.
$existing = Get-AppxPackage $IdentityName -ErrorAction SilentlyContinue
if ($existing) {
    Write-Host "Removing previous registration of $($existing.PackageFamilyName) ..."
    # Stop any running daemon/GUI process so the staging dir isn't locked.
    Get-Process -Name cernbox-syncd, cernbox-sync-gui -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 200
    Remove-AppxPackage -Package $existing.PackageFullName -ErrorAction SilentlyContinue
}

if (Test-Path $StageDir) {
    Remove-Item -Recurse -Force $StageDir
}
New-Item -Force -ItemType Directory $StageDir | Out-Null

Write-Host "Staging at $StageDir ..."
Copy-Item $mainExe.FullName   (Join-Path $StageDir $mainExe.Name)
Copy-Item $daemonExe.FullName (Join-Path $StageDir 'cernbox-syncd.exe')

foreach ($extra in 'WebView2Loader.dll', 'resources') {
    $src = Join-Path $ReleaseDir $extra
    if (Test-Path $src) {
        Copy-Item $src (Join-Path $StageDir $extra) -Recurse -Force
    }
}

$assetsDir = Join-Path $StageDir 'Assets'
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

# -- Render the manifest ------------------------------------------------------
$manifest = Get-Content -Raw -Path $ManifestTpl
$manifest = $manifest.Replace('__IDENTITY_NAME__',           $IdentityName)
$manifest = $manifest.Replace('__PUBLISHER__',               $Publisher)
$manifest = $manifest.Replace('__VERSION__',                 $Version)
$manifest = $manifest.Replace('__DISPLAY_NAME__',            $DisplayName)
$manifest = $manifest.Replace('__PUBLISHER_DISPLAY_NAME__',  $PublisherDisplayName)
$manifest = $manifest.Replace('__EXECUTABLE__',              $mainExe.Name)
$manifestPath = Join-Path $StageDir 'AppxManifest.xml'
Set-Content -Path $manifestPath -Value $manifest -Encoding UTF8

# -- Register -----------------------------------------------------------------
#
# Add-AppxPackage -Register registers a folder layout as an installed
# package without packaging or signing. Requires Developer Mode OR for the
# user to be the package owner (they always are, for self-registered loose
# packages, on Win10 1809+).
#
# -ForceUpdateFromAnyVersion: lets us re-register in place even when the
# version in the manifest is unchanged.
Write-Host "Registering $manifestPath ..."
Add-AppxPackage -Register $manifestPath -ForceUpdateFromAnyVersion

$pkg = Get-AppxPackage $IdentityName
if (-not $pkg) { throw 'Registration succeeded but Get-AppxPackage returned nothing - investigate.' }

Write-Host ''
Write-Host '=========================================================='
Write-Host "Registered: $($pkg.PackageFamilyName)"
Write-Host "Install location: $($pkg.InstallLocation)"
Write-Host ''
Write-Host 'Launch the GUI:'
Write-Host "  Start-Process 'shell:AppsFolder\$($pkg.PackageFamilyName)!App'"
Write-Host ''
Write-Host 'Run the daemon directly (debug logs to console):'
Write-Host "  & '$($pkg.InstallLocation)\cernbox-syncd.exe' -log-level debug"
Write-Host '=========================================================='
