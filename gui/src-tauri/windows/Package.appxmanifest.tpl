<?xml version="1.0" encoding="utf-8"?>
<!--
  Package.appxmanifest.tpl — MSIX manifest template for cernbox-sync.

  Substitutions performed by dev/windows/make-msix.ps1:
    __IDENTITY_NAME__           Package identity (reverse-DNS, e.g. ch.cern.cernbox-sync)
    __PUBLISHER__               Publisher subject (must match signing cert's Subject)
    __VERSION__                 Four-part version, e.g. 0.1.0.0
    __DISPLAY_NAME__            User-facing display name
    __PUBLISHER_DISPLAY_NAME__  User-facing publisher name
    __EXECUTABLE__              Tauri main exe filename (relative to package root)

  Why we need this manifest:
    The new Cloud Files registration API — StorageProviderSyncRootManager.Register
    — is a WinRT call gated on package identity. An unpackaged Win32 process
    can't call it (returns E_NO_PACKAGE_IDENTITY). Wrapping the cernbox-sync
    bundle in an MSIX gives the daemon a package identity and unlocks the
    modern Explorer integration (status badges, "Always keep on this device"
    verb, namespace icon under "This PC").

  This is a Desktop Bridge (full-trust packaged Win32) manifest, not a UWP
  app: the Application uses EntryPoint="Windows.FullTrustApplication" plus
  the runFullTrust capability so cernbox-syncd can do everything an
  unpackaged daemon does (TCP/IPC sockets, cgo, fs walks, etc).

  The Cloud Files shell extension classes (status icon handler, command
  verbs) will be added in Phase 3 under a uap:Extension Category="windows.cloudFiles"
  block — left out of v1 so we can land identity first and verify the WinRT
  registration call succeeds end-to-end before layering shell COM servers
  on top.
-->
<Package
  xmlns="http://schemas.microsoft.com/appx/manifest/foundation/windows10"
  xmlns:uap="http://schemas.microsoft.com/appx/manifest/uap/windows10"
  xmlns:uap10="http://schemas.microsoft.com/appx/manifest/uap/windows10/10"
  xmlns:rescap="http://schemas.microsoft.com/appx/manifest/foundation/windows10/restrictedcapabilities"
  IgnorableNamespaces="uap uap10 rescap">

  <Identity Name="__IDENTITY_NAME__"
            Publisher="__PUBLISHER__"
            Version="__VERSION__"
            ProcessorArchitecture="x64" />

  <Properties>
    <DisplayName>__DISPLAY_NAME__</DisplayName>
    <PublisherDisplayName>__PUBLISHER_DISPLAY_NAME__</PublisherDisplayName>
    <Logo>Assets\StoreLogo.png</Logo>
  </Properties>

  <Dependencies>
    <!--
      MinVersion 10.0.17763.0 = Windows 10 1809.
      That's the first build where Cloud Files API + StorageProviderSyncRootManager
      are both stable; older builds either lack the API entirely or have known
      sync-root reparse-point bugs.
      MaxVersionTested follows the latest LTSC we've validated against.
    -->
    <TargetDeviceFamily Name="Windows.Desktop"
                        MinVersion="10.0.17763.0"
                        MaxVersionTested="10.0.22621.0" />
  </Dependencies>

  <Resources>
    <Resource Language="en-us" />
  </Resources>

  <Applications>
    <Application Id="App"
                 Executable="__EXECUTABLE__"
                 EntryPoint="Windows.FullTrustApplication">
      <uap:VisualElements
        DisplayName="__DISPLAY_NAME__"
        Description="CERNBox cloud-aware sync client"
        BackgroundColor="transparent"
        Square150x150Logo="Assets\Square150x150Logo.png"
        Square44x44Logo="Assets\Square44x44Logo.png">
        <uap:DefaultTile Square71x71Logo="Assets\Square71x71Logo.png"
                         Square310x310Logo="Assets\Square310x310Logo.png"
                         Wide310x150Logo="Assets\Square150x150Logo.png" />
      </uap:VisualElements>
    </Application>
    <!--
      The daemon is declared as a second Application so MSIX grants it
      execute ACLs and the GUI's sidecar spawn (CreateProcess from
      Tauri's shell plugin) succeeds. Without this entry the OS treats
      cernbox-syncd.exe as a payload-only file and CreateProcess fails
      with ERROR_ACCESS_DENIED, even when invoked from inside the same
      package. AppListEntry="none" hides the daemon from the Start menu
      and AppsFolder so it doesn't show up alongside the GUI.

      Both Applications share the package's Identity, so when the GUI
      starts the daemon they're inside the same package boundary - the
      socket path each side computes via Windows.Storage.ApplicationData
      / FOLDERID_LocalAppData lands on the same redirected location.
    -->
    <Application Id="Daemon"
                 Executable="cernbox-syncd.exe"
                 EntryPoint="Windows.FullTrustApplication">
      <uap:VisualElements
        DisplayName="CERNBox Sync Daemon"
        Description="Background sync daemon for CERNBox Sync"
        BackgroundColor="transparent"
        Square150x150Logo="Assets\Square150x150Logo.png"
        Square44x44Logo="Assets\Square44x44Logo.png"
        AppListEntry="none">
      </uap:VisualElements>
    </Application>
  </Applications>

  <Capabilities>
    <!--
      runFullTrust — required for Desktop Bridge / packaged Win32 apps.
      Without it the daemon can't open arbitrary files on disk, run cgo,
      or speak unrestricted TCP/IPC. This capability is restricted (rescap)
      and ignored by the Store unless the publisher is allow-listed; for
      sideloaded / sparse-signed dev builds it works without restriction.
    -->
    <rescap:Capability Name="runFullTrust" />
  </Capabilities>
</Package>
