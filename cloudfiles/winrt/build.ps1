# build.ps1 - compile cernbox-cf.dll (the C++/WinRT registration shim).
#
# Resolves the latest installed Visual Studio Build Tools install via
# vswhere, sources the x64 dev environment via Microsoft.VisualStudio.DevShell,
# and invokes cl.exe directly. Output goes to gui/src-tauri/binaries/
# alongside the daemon so make-msix.ps1 / register-msix-dev.ps1 pick it up
# without further plumbing.
#
# Run from the Windows VM. Prereq: dev/windows/setup.ps1 must have run
# (installs visualstudio2022buildtools with the VCTools workload + Win11
# SDK 22621). Re-running this script is cheap once the toolchain is warm
# - cl.exe rebuilds incrementally.

[CmdletBinding()]
param(
    [string]$RepoRoot,
    [string]$OutDir,
    [string]$Configuration = 'Release',
    [switch]$Verbose
)

$ErrorActionPreference = 'Stop'

$scriptRoot = $PSScriptRoot
if (-not $scriptRoot) {
    $scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
}

if (-not $RepoRoot) { $RepoRoot = (Resolve-Path (Join-Path $scriptRoot '..\..')).Path }
if (-not $OutDir)   { $OutDir   = Join-Path $RepoRoot 'gui\src-tauri\binaries' }
New-Item -Force -ItemType Directory $OutDir | Out-Null

# -- Locate Visual Studio Build Tools via vswhere -----------------------------
$vswhere = "${env:ProgramFiles(x86)}\Microsoft Visual Studio\Installer\vswhere.exe"
if (-not (Test-Path $vswhere)) {
    throw "vswhere.exe not found at $vswhere - run dev\windows\setup.ps1 first."
}

$vsInstallPath = & $vswhere -latest -products * `
    -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 `
    -property installationPath
if (-not $vsInstallPath) {
    throw "No VS install with C++ tools found - re-run dev\windows\setup.ps1 to install visualstudio2022buildtools."
}

# -- Enter x64 dev shell ------------------------------------------------------
# Microsoft.VisualStudio.DevShell.dll provides Enter-VsDevShell, which sets
# up PATH, INCLUDE, LIB, etc. for cl/link to find the SDK and CRT.
$devShellDll = Join-Path $vsInstallPath 'Common7\Tools\Microsoft.VisualStudio.DevShell.dll'
if (-not (Test-Path $devShellDll)) {
    throw "Microsoft.VisualStudio.DevShell.dll not found at $devShellDll - VS install seems broken."
}
Import-Module $devShellDll
# StartInPath keeps cwd intact (Enter-VsDevShell otherwise cd's to the VS
# install root). SkipAutomaticLocation has the same effect and is the
# documented incantation.
Enter-VsDevShell -VsInstallPath $vsInstallPath `
    -SkipAutomaticLocation `
    -DevCmdArguments '-arch=x64 -host_arch=x64' | Out-Null

# Sanity-check cl.exe is now on PATH.
$cl = (Get-Command cl.exe -ErrorAction SilentlyContinue).Source
if (-not $cl) {
    throw 'cl.exe not on PATH after Enter-VsDevShell - dev shell setup failed.'
}
Write-Host "Using cl.exe: $cl"

# -- Compile + link -----------------------------------------------------------
$src = Join-Path $scriptRoot 'cernbox-cf.cpp'
$dll = Join-Path $OutDir     'cernbox-cf.dll'
$obj = Join-Path $env:TEMP   "cernbox-cf-$([guid]::NewGuid().ToString('N')).obj"

# /std:c++17       C++/WinRT requires C++17 minimum.
# /EHsc            Standard C++ exception handling.
# /MD              Link the DLL CRT (required for inter-DLL ABI compatibility).
# /LD              Build a DLL.
# /O2              Optimised build (no need to debug the shim itself).
# /permissive-     Strict standards conformance - C++/WinRT depends on it.
# /Zc:__cplusplus  Make __cplusplus actually report C++17, not 199711L.
# /await           Required for co_await syntax (we don't use it directly,
#                  but C++/WinRT's projection types are coroutine-aware).
$clFlags = @(
    '/std:c++17'
    '/EHsc'
    '/MD'
    '/LD'
    if ($Configuration -eq 'Debug') { '/Od', '/Zi' } else { '/O2' }
    '/permissive-'
    '/Zc:__cplusplus'
    '/nologo'
    "/Fo$obj"
    "/Fe:$dll"
    $src
)
$linkFlags = @(
    '/link'
    '/DLL'
    'ole32.lib'
    'runtimeobject.lib'      # RoActivateInstance, WinRT type activation
    'WindowsApp.lib'         # Universal Windows API umbrella library
)

Write-Host "Compiling $src -> $dll ..."
$null = & cl.exe @clFlags @linkFlags
if ($LASTEXITCODE -ne 0) {
    throw "cl.exe failed with exit code $LASTEXITCODE"
}

# Clean up the object file; keep only the DLL.
if (Test-Path $obj) { Remove-Item $obj }
$expFile = [System.IO.Path]::ChangeExtension($dll, '.exp')
$libFile = [System.IO.Path]::ChangeExtension($dll, '.lib')
if (Test-Path $expFile) { Remove-Item $expFile }
if (Test-Path $libFile) { Remove-Item $libFile }

Write-Host ''
Write-Host "Built: $dll"
Get-Item $dll | Select-Object Name, Length, LastWriteTime
