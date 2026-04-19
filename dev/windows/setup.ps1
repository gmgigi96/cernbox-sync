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

# Workspace directory for source code
New-Item -Force -ItemType Directory -Path 'C:\workspace\cernbox-sync' | Out-Null

Write-Host ""
Write-Host "Setup complete."
Write-Host "  Go:  $(go version)"
Write-Host "  Git: $(git --version)"
Write-Host "  GCC: $(gcc --version | Select-Object -First 1)"
