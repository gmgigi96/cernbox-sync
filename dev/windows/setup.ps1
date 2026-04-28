# Allow PowerShell scripts to run for the current user. Without this, the
# default Restricted policy blocks npm.ps1 / Tauri's helper scripts when
# `make windows-vm-gui-dev` invokes them over SSH.
Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned -Force

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

# Workspace directory for source code
New-Item -Force -ItemType Directory -Path 'C:\workspace\cernbox-sync' | Out-Null

Write-Host ""
Write-Host "Setup complete."
Write-Host "  Go:    $(go version)"
Write-Host "  Git:   $(git --version)"
Write-Host "  GCC:   $(gcc --version | Select-Object -First 1)"
Write-Host "  Node:  $(node --version)"
Write-Host "  npm:   $(npm --version)"
Write-Host "  Rust:  $(rustc --version)"
Write-Host "  Cargo: $(cargo --version)"
