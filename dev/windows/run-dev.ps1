# Build the cernbox-syncd sidecar and start `npm run tauri dev`.
#
# Run this from inside the Windows VM after starting it on the host with
# `make windows-vm-gui`. Source lives on the SMB share mounted at Z:; the
# cargo target dir and node_modules are placed locally for speed (target
# via $env:CARGO_TARGET_DIR set during `make windows-vm-setup`; node_modules
# is small enough on the share, so we keep it next to package.json).
$ErrorActionPreference = 'Stop'

$env:Path = (Join-Path $env:USERPROFILE '.cargo\bin') + ';' + $env:Path

if (-not (Get-Command rustc -ErrorAction SilentlyContinue)) {
    Write-Error 'rustc not found — run `make windows-vm-setup` on the host first'
    exit 1
}

$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
Set-Location $repo

# Kill any stale daemon from a previous run — it would hold an exclusive
# lock on the sidecar .exe and cause `go build` / tauri-build to fail
# with "Access is denied" (PermissionDenied / OS error 5).
Get-Process -Name cernbox-syncd -ErrorAction SilentlyContinue | Stop-Process -Force

$triple = ((rustc -vV | Select-String '^host:') -replace 'host:\s*','').Trim()
New-Item -Force -ItemType Directory gui\src-tauri\binaries | Out-Null

Write-Host "Building cernbox-syncd-$triple.exe..."
go build -buildvcs=false -ldflags='-s -w' -o "gui\src-tauri\binaries\cernbox-syncd-$triple.exe" ./cmd/cernbox-syncd
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Set-Location (Join-Path $repo 'gui')

if (-not (Test-Path node_modules)) {
    Write-Host "Installing npm dependencies (first run)..."
    npm.cmd install
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

Write-Host "Starting npm run tauri dev..."
npm.cmd run tauri dev
