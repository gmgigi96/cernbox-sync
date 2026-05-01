# new-dev-cert.ps1 - generate a self-signed code-signing cert for MSIX dev builds.
#
# Run once on the Windows VM (or any Windows host that does the packaging).
# Produces two files in dev/windows/cert/:
#
#   cernbox-sync-dev.pfx   private key + cert, password-protected, used by signtool
#   cernbox-sync-dev.cer   public cert, imported on the test machine into
#                          Cert:\LocalMachine\Root + TrustedPeople so the
#                          MSIX install passes signature validation
#
# The cert subject MUST match the Publisher attribute in Package.appxmanifest
# (CN=CERN Box Sync Dev) - MakeAppx/Add-AppxPackage rejects mismatched
# publisher/subject pairs. Edit -PublisherSubject below if you change the
# manifest publisher.
#
# Production cert is provisioned outside this script (HSM/PKCS#11/EV cert via
# CERN's signing process). This script is dev-only.

[CmdletBinding()]
param(
    [string]$PublisherSubject = 'CN=CERN Box Sync Dev',
    [string]$Password         = 'cernbox-dev',
    [int]   $YearsValid       = 5,
    [string]$OutDir
)

$ErrorActionPreference = 'Stop'

# Resolve script root robustly: $PSScriptRoot is empty inside param()
# defaults when invoked as `powershell.exe -File <relative-path>` on PS 5.1.
$scriptRoot = $PSScriptRoot
if (-not $scriptRoot) {
    $scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
}
if (-not $OutDir) { $OutDir = Join-Path $scriptRoot 'cert' }

New-Item -Force -ItemType Directory $OutDir | Out-Null

$pfxPath = Join-Path $OutDir 'cernbox-sync-dev.pfx'
$cerPath = Join-Path $OutDir 'cernbox-sync-dev.cer'

if (Test-Path $pfxPath) {
    Write-Host "Cert already exists at $pfxPath. Delete it manually if you want to regenerate."
    exit 0
}

# Code-signing EKU (1.3.6.1.5.5.7.3.3) - without it, signtool refuses the cert
# even though New-SelfSignedCertificate would happily produce one usable for
# encryption.
$cert = New-SelfSignedCertificate `
    -Type CodeSigningCert `
    -Subject $PublisherSubject `
    -KeyUsage DigitalSignature `
    -FriendlyName 'CERNBox Sync dev signing key' `
    -CertStoreLocation 'Cert:\CurrentUser\My' `
    -NotAfter (Get-Date).AddYears($YearsValid) `
    -TextExtension @('2.5.29.37={text}1.3.6.1.5.5.7.3.3', '2.5.29.19={text}')

$securePassword = ConvertTo-SecureString -String $Password -Force -AsPlainText

Export-PfxCertificate -Cert $cert -FilePath $pfxPath -Password $securePassword | Out-Null
Export-Certificate    -Cert $cert -FilePath $cerPath                            | Out-Null

# Trust the cert on this machine so locally-built MSIX packages install
# without "untrusted publisher" prompts. On a clean test machine import
# cernbox-sync-dev.cer into LocalMachine\Root + LocalMachine\TrustedPeople
# manually (requires admin):
#   Import-Certificate -FilePath cernbox-sync-dev.cer -CertStoreLocation Cert:\LocalMachine\Root
#   Import-Certificate -FilePath cernbox-sync-dev.cer -CertStoreLocation Cert:\LocalMachine\TrustedPeople
try {
    Import-Certificate -FilePath $cerPath -CertStoreLocation 'Cert:\LocalMachine\Root'         -ErrorAction SilentlyContinue | Out-Null
    Import-Certificate -FilePath $cerPath -CertStoreLocation 'Cert:\LocalMachine\TrustedPeople' -ErrorAction SilentlyContinue | Out-Null
} catch {
    Write-Warning 'Could not import dev cert into LocalMachine stores - re-run elevated, or import manually before installing the MSIX.'
}

Write-Host ''
Write-Host 'Dev signing cert generated:'
Write-Host "  PFX:  $pfxPath"
Write-Host "  CER:  $cerPath"
Write-Host "  Subject:  $PublisherSubject"
Write-Host "  PFX password:  $Password"
Write-Host ''
Write-Host 'Keep the .pfx out of source control. The repo .gitignore should already exclude dev/windows/cert/.'
