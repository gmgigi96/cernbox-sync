# Install Chocolatey
Set-ExecutionPolicy Bypass -Scope Process -Force
[System.Net.ServicePointManager]::SecurityProtocol = `
    [System.Net.ServicePointManager]::SecurityProtocol -bor 3072
Invoke-Expression ((New-Object System.Net.WebClient).DownloadString(
    'https://community.chocolatey.org/install.ps1'))

# Install Go, Git, and MinGW (GCC needed for CGo on Windows)
choco install -y golang git mingw

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
    Write-Warning "Windows SDK cfapi.h not found — install Windows 10 SDK"
}

# Workspace directory for source code
New-Item -Force -ItemType Directory -Path 'C:\workspace\cernbox-sync' | Out-Null

Write-Host ""
Write-Host "Setup complete."
Write-Host "  Go:  $(go version)"
Write-Host "  Git: $(git --version)"
Write-Host "  GCC: $(gcc --version | Select-Object -First 1)"
