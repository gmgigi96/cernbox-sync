# run-build.ps1 - release-mode counterpart to run-dev.ps1.
#
# Builds the cernbox-syncd sidecar with -ldflags='-s -w' and produces the
# release Tauri bundle (npm run tauri build). Used by the gui-msix Makefile
# target as the prerequisite step before make-msix.ps1 packages the MSIX.
#
# Run inside the Windows VM after `make windows-vm-setup`. The repo is
# expected to be at the script's parent-of-parent (i.e., dev/windows/run-build.ps1
# resolves to <repo>).
#
# -ServerUrl overrides the VITE_SERVER_URL baked into the GUI bundle.
# By default vite reads .env.production at build time (which points at
# https://cernbox.cern.ch) - that's wrong for dev MSIX builds where we
# want the GUI to talk to the local revad. The Makefile passes
# 'http://localhost' here for `make gui-msix`; override via
# `make gui-msix MSIX_SERVER_URL=...` for a production bundle.

[CmdletBinding()]
param(
    [string]$ServerUrl = ''
)

$ErrorActionPreference = 'Stop'

$env:Path = (Join-Path $env:USERPROFILE '.cargo\bin') + ';' + $env:Path

if (-not (Get-Command rustc -ErrorAction SilentlyContinue)) {
    Write-Error 'rustc not found - run `make windows-vm-setup` on the host first'
    exit 1
}

$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
Set-Location $repo

# Stop any stale daemon - exclusive file lock would break the rebuild.
Get-Process -Name cernbox-syncd -ErrorAction SilentlyContinue | Stop-Process -Force

$triple = ((rustc -vV | Select-String '^host:') -replace 'host:\s*','').Trim()
New-Item -Force -ItemType Directory gui\src-tauri\binaries | Out-Null

# Build the C++/WinRT registration shim first. Output lands in
# gui\src-tauri\binaries\cernbox-cf.dll, alongside the daemon, so the MSIX
# staging step picks it up via its standard binaries lookup. Failure here
# is fatal: without this DLL the daemon can't register sync roots and
# on-demand sync silently falls back to plain download.
Write-Host 'Building cernbox-cf.dll (Cloud Files WinRT shim)...'
& powershell.exe -ExecutionPolicy Bypass -File 'cloudfiles\winrt\build.ps1' `
    -OutDir 'gui\src-tauri\binaries'
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "Building cernbox-syncd-$triple.exe (release)..."
go build -buildvcs=false -ldflags='-s -w' -o "gui\src-tauri\binaries\cernbox-syncd-$triple.exe" ./cmd/cernbox-syncd
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

# Tauri's bundler (the prebuilt npm-distributed CLI) hardcodes the host
# triple it was compiled against and uses that to look up externalBin
# entries. The npm package ships an msvc-built CLI, so even when our
# rustc/cargo are GNU (setup.ps1 installs rust with --default-host
# x86_64-pc-windows-gnu) the bundler asks for cernbox-syncd-x86_64-pc-windows-msvc.exe.
# Copy the binary under every plausible triple so tauri dev (rustc's host)
# and tauri build (CLI's host) both find it.
foreach ($alt in 'x86_64-pc-windows-msvc', 'x86_64-pc-windows-gnu') {
    if ($alt -ne $triple) {
        Copy-Item -Force `
            "gui\src-tauri\binaries\cernbox-syncd-$triple.exe" `
            "gui\src-tauri\binaries\cernbox-syncd-$alt.exe"
    }
}

Set-Location (Join-Path $repo 'gui')

if (-not (Test-Path node_modules)) {
    Write-Host 'Installing npm dependencies (first run)...'
    npm.cmd install
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

Write-Host 'Running tauri build (no-bundle)...'
# NO_STRIP=1 mirrors the host-side `make gui` recipe - Tauri's strip step
# breaks on the cgo-linked daemon sidecar (mingw stripping leaves a binary
# that won't load cldapi.dll callbacks correctly).
$env:NO_STRIP = '1'
# Vite reads VITE_SERVER_URL from the env at build time, taking precedence
# over .env.production. Without this override the GUI is hardcoded to
# https://cernbox.cern.ch, which is wrong for any local dev setup.
if ($ServerUrl) {
    Write-Host "Setting VITE_SERVER_URL=$ServerUrl for the GUI build"
    $env:VITE_SERVER_URL = $ServerUrl
}
# --no-bundle skips Tauri's MSI/NSIS bundling step. We don't want either:
#  * make-msix.ps1 packages the binary tree itself with MakeAppx, and
#  * the MSI step downloads WiX and adds ~30s of latency for an artefact
#    we throw away.
# It's passed through `--` so npm doesn't swallow the flag.
npm.cmd run tauri build -- --no-bundle
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host ''
Write-Host 'Tauri build complete.'
