# Allow PowerShell scripts to run for the current user. Without this, the
# default Restricted policy blocks npm.ps1 / Tauri's helper scripts when
# `make windows-vm-gui-dev` invokes them over SSH. Use Unrestricted (not
# RemoteSigned) so scripts on the QEMU SMB share at Z: — which Windows
# tags as the Internet zone — also run without manual unblocking.
Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy Unrestricted -Force

# Install Chocolatey
Set-ExecutionPolicy Bypass -Scope Process -Force
[System.Net.ServicePointManager]::SecurityProtocol = `
    [System.Net.ServicePointManager]::SecurityProtocol -bor 3072
Invoke-Expression ((New-Object System.Net.WebClient).DownloadString(
    'https://community.chocolatey.org/install.ps1'))

# Install Go, Git, and MinGW (GCC needed for CGo on Windows)
choco install -y golang git mingw

# Tauri/Vite GUI dev prerequisites: Node.js (LTS) for the dev server and
# Tauri CLI; WebView2 runtime for the Tauri webview (built into Windows 11
# but installing it explicitly is cheap insurance).
choco install -y nodejs-lts microsoft-edge-webview2-runtime

# SPICE guest tools — installs the vdagent service plus QXL/virtio-serial
# drivers, enabling host<->guest clipboard sharing and dynamic resolution
# when the VM is launched with `make windows-vm-gui` (which uses -spice +
# -chardev spicevmc,name=vdagent). The installer is NSIS-based; /S runs
# silently. Done unconditionally — it costs ~5 MB and lets the agent kick
# in the first time SPICE channels appear, even if a future VM session
# falls back to plain SDL with no SPICE devices.
$spiceTools = Join-Path $env:TEMP 'spice-guest-tools.exe'
Invoke-WebRequest -UseBasicParsing `
    -Uri 'https://www.spice-space.org/download/windows/spice-guest-tools/spice-guest-tools-latest.exe' `
    -OutFile $spiceTools
Start-Process -FilePath $spiceTools -ArgumentList '/S' -Wait -NoNewWindow
Remove-Item $spiceTools

# Visual Studio Build Tools 2022 + C++/WinRT toolchain. Required by the
# Cloud Files registration shim (cloudfiles/winrt/cernbox-cf.dll) which
# calls StorageProviderSyncRootManager.Register - a WinRT API that only
# C++/WinRT wraps cleanly, and C++/WinRT support in MinGW is incomplete.
# The VCTools workload bundles cl.exe, link.exe, MSBuild, and the Win11
# SDK 22621 with cppwinrt projection headers under
# 'C:\Program Files (x86)\Windows Kits\10\Include\10.0.22621.0\cppwinrt\'.
#
# Heavy install: ~7-9 GB on disk, 15-30 min on first run. Idempotent on
# subsequent runs (choco fast-paths an existing install).
choco install -y visualstudio2022buildtools `
    --package-parameters "--add Microsoft.VisualStudio.Workload.VCTools --add Microsoft.VisualStudio.Component.VC.Tools.x86.x64 --add Microsoft.VisualStudio.Component.Windows11SDK.22621 --includeRecommended --quiet --norestart"

# Reload PATH so go/gcc are usable in this session
$env:Path = [System.Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' +
            [System.Environment]::GetEnvironmentVariable('Path', 'User')

# Copy cfapi.h from the Windows SDK into MinGW's system include directory.
# MinGW does not ship cfapi.h and the Cloud Files wrappers need it.
$sdkCfapi = Get-ChildItem 'C:\Program Files (x86)\Windows Kits\10\Include\*\um\cfapi.h' |
    Sort-Object { $_.FullName } -Descending | Select-Object -First 1
if ($sdkCfapi) {
    $mingwInclude = (gcc -print-file-name=include 2>$null)
    if (-not $mingwInclude) {
        $mingwInclude = 'C:\ProgramData\mingw64\mingw64\x86_64-w64-mingw32\include'
    }
    Copy-Item $sdkCfapi.FullName (Join-Path $mingwInclude 'cfapi.h') -Force
    Write-Host "  cfapi.h copied from $($sdkCfapi.FullName)"
} else {
    Write-Warning "Windows SDK cfapi.h not found - install Windows 10 SDK"
}

# Install Rust via rustup, defaulting to the GNU host so it links with the
# MinGW toolchain installed above (avoids needing Visual Studio Build Tools).
# Required by Tauri's Rust backend for `make windows-vm-gui-dev`.
$rustup = Join-Path $env:TEMP 'rustup-init.exe'
Invoke-WebRequest -UseBasicParsing -Uri 'https://win.rustup.rs/x86_64' -OutFile $rustup
& $rustup -y --default-host x86_64-pc-windows-gnu --profile minimal

# Reload PATH again so node/cargo are visible to the verification block below
# and to subsequent SSH sessions (rustup writes %USERPROFILE%\.cargo\bin to
# the User PATH).
$env:Path = [System.Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' +
            [System.Environment]::GetEnvironmentVariable('Path', 'User') + ';' +
            (Join-Path $env:USERPROFILE '.cargo\bin')

# Make Windows compatible with QEMU's built-in SMB share (\\10.0.2.4\qemu):
#  1. Allow unauthenticated guest auth — QEMU's smbd only offers guest, but
#     Windows 10/11 block insecure guest by default.
#  2. Don't require SMB packet signing — Windows 11 24H2 enforces signing
#     for outbound connections by default; QEMU's smbd doesn't support it,
#     so the tree-connect fails with error 0xC04A2003.
reg add 'HKLM\SYSTEM\CurrentControlSet\Services\LanmanWorkstation\Parameters' /v AllowInsecureGuestAuth /t REG_DWORD /d 1 /f | Out-Null
Set-SmbClientConfiguration -RequireSecuritySignature $false -Confirm:$false
Set-SmbClientConfiguration -EnableSecuritySignature $false -Confirm:$false

# Map the host repo (exposed via QEMU's built-in SMB at \\10.0.2.4\qemu)
# as Z: on every desktop logon. We can't rely on /persistent:yes here
# because setup.ps1 runs over SSH (a non-interactive session) while the
# SMB share is only attached when the VM is launched with `make
# windows-vm-gui`. A startup .cmd in the Startup folder runs at each
# interactive logon, when the share is guaranteed to be reachable.
$startup = [Environment]::GetFolderPath('Startup')
New-Item -Force -ItemType Directory $startup | Out-Null
@'
@echo off
net use Z: \\10.0.2.4\qemu >nul 2>&1
'@ | Set-Content -Encoding ASCII (Join-Path $startup 'mount-qemu-share.cmd')

# Also try to mount it now (will silently fail if SMB share not yet attached;
# the startup script will pick it up the next time the user logs in).
cmd /c 'net use Z: /delete /yes' 2>$null | Out-Null
cmd /c 'net use Z: \\10.0.2.4\qemu' 2>$null | Out-Null

# Forward localhost:80 inside the VM to the host's 10.0.2.2:80 (QEMU's
# slirp gateway). The cernbox dev environment listens on the host's
# localhost:80, so this lets the same `http://localhost/` URL work
# unchanged in the VM — no need to remember 10.0.2.2.
netsh interface portproxy delete v4tov4 listenport=80 listenaddress=127.0.0.1 2>$null | Out-Null
netsh interface portproxy add v4tov4 listenport=80 listenaddress=127.0.0.1 connectport=80 connectaddress=10.0.2.2

# Cargo's target/ has tens of thousands of small object files; over the
# slirp+SMB share that would be unbearable. Pin it to local NTFS instead
# (machine-wide so every shell sees it). Source still lives on Z:.
$cargoTargetDir = 'C:\dev-cache\cargo-target'
New-Item -Force -ItemType Directory $cargoTargetDir | Out-Null
[System.Environment]::SetEnvironmentVariable('CARGO_TARGET_DIR', $cargoTargetDir, 'Machine')
$env:CARGO_TARGET_DIR = $cargoTargetDir

Write-Host ""
Write-Host "Setup complete."
Write-Host "  Go:    $(go version)"
Write-Host "  Git:   $(git --version)"
Write-Host "  GCC:   $(gcc --version | Select-Object -First 1)"
Write-Host "  Node:  $(node --version)"
Write-Host "  npm:   $(npm --version)"
Write-Host "  Rust:  $(rustc --version)"
Write-Host "  Cargo: $(cargo --version)"
