<?xml version="1.0" encoding="utf-8"?>
<!--
  tests-AppxManifest.tpl - manifest for the cloudfiles test runner package.
  Rendered into AppxManifest.xml by run-tests-msix.ps1.

  Substitutions:
    __VERSION__   four-part version, bumped per run so stale registrations
                  don't block re-registration without -ForceUpdateFromAnyVersion.

  This package exists solely so the cloudfiles test binary runs with MSIX
  package identity (a hard precondition of the WinRT registration code
  paths under test). Different Identity Name from the GUI package
  (ch.cern.cernbox-sync vs ch.cern.cernbox-sync-tests) so a registered
  dev install of the GUI doesn't conflict with a test run, and vice
  versa.

  The uap5:AppExecutionAlias is the trick that makes this whole approach
  ergonomic: registering the alias creates a stub on PATH (under
  %LOCALAPPDATA%\Microsoft\WindowsApps\) that, when invoked, activates
  the packaged app and forwards the command line. Stdout/stderr/exitcode
  pipe through normally - the test binary behaves exactly like any other
  console exe except it has package identity.
-->
<Package
  xmlns="http://schemas.microsoft.com/appx/manifest/foundation/windows10"
  xmlns:uap="http://schemas.microsoft.com/appx/manifest/uap/windows10"
  xmlns:uap5="http://schemas.microsoft.com/appx/manifest/uap/windows10/5"
  xmlns:rescap="http://schemas.microsoft.com/appx/manifest/foundation/windows10/restrictedcapabilities"
  xmlns:desktop="http://schemas.microsoft.com/appx/manifest/desktop/windows10"
  xmlns:desktop3="http://schemas.microsoft.com/appx/manifest/desktop/windows10/3"
  IgnorableNamespaces="uap uap5 rescap desktop desktop3">

  <Identity Name="ch.cern.cernbox-sync-tests"
            Publisher="CN=CERN Box Sync Dev"
            Version="__VERSION__"
            ProcessorArchitecture="x64" />

  <Properties>
    <DisplayName>cernbox-sync tests</DisplayName>
    <PublisherDisplayName>CERN</PublisherDisplayName>
    <Logo>Assets\StoreLogo.png</Logo>
  </Properties>

  <Dependencies>
    <TargetDeviceFamily Name="Windows.Desktop"
                        MinVersion="10.0.17763.0"
                        MaxVersionTested="10.0.22621.0" />
  </Dependencies>

  <Resources>
    <Resource Language="en-us" />
  </Resources>

  <Applications>
    <!--
      One Application per Go-test binary that needs package identity. Each
      gets its own uap5:AppExecutionAlias so it's invocable from the host
      shell as a normal command. Package identity is granted at activation
      time, so each alias spawn becomes a new packaged process.
    -->
    <Application Id="CloudfilesTests"
                 Executable="cloudfiles.test.exe"
                 EntryPoint="Windows.FullTrustApplication">
      <uap:VisualElements
        DisplayName="cernbox-sync cloudfiles tests"
        Description="Test runner for the cloudfiles package"
        BackgroundColor="transparent"
        Square150x150Logo="Assets\Square150x150Logo.png"
        Square44x44Logo="Assets\Square44x44Logo.png" />
      <Extensions>
        <!-- windows.cloudFiles declares that this Application can act as
             a cloud-files sync provider. Without it,
             StorageProviderSyncRootManager.Register fails with E_FAIL
             (0x80004005). The IconResource here is the package-default
             icon shown on the sync root in Explorer; Phase 4 polish
             work can replace it with a per-folder cernbox-branded icon. -->
        <!-- Microsoft's CloudMirror sample uses the desktop3 namespace
             for windows.cloudFiles (the alternative cloudfiles/windows10
             namespace is outdated/unsupported on Win11 24H2+). The empty
             <CloudFiles/> element is the minimum the schema accepts; the
             CustomStateHandler / ThumbnailProvider / ContextMenu COM
             classes are all optional. -->
        <desktop3:Extension Category="windows.cloudFiles">
          <desktop3:CloudFiles />
        </desktop3:Extension>
        <uap5:Extension Category="windows.appExecutionAlias"
                        Executable="cloudfiles.test.exe"
                        EntryPoint="Windows.FullTrustApplication">
          <uap5:AppExecutionAlias>
            <uap5:ExecutionAlias Alias="cernbox-cloudfiles-test.exe" />
          </uap5:AppExecutionAlias>
        </uap5:Extension>
      </Extensions>
    </Application>
    <Application Id="DaemonTests"
                 Executable="daemon.test.exe"
                 EntryPoint="Windows.FullTrustApplication">
      <uap:VisualElements
        DisplayName="cernbox-sync daemon tests"
        Description="Test runner for the daemon package"
        BackgroundColor="transparent"
        Square150x150Logo="Assets\Square150x150Logo.png"
        Square44x44Logo="Assets\Square44x44Logo.png" />
      <Extensions>
        <!-- Microsoft's CloudMirror sample uses the desktop3 namespace
             for windows.cloudFiles (the alternative cloudfiles/windows10
             namespace is outdated/unsupported on Win11 24H2+). The empty
             <CloudFiles/> element is the minimum the schema accepts; the
             CustomStateHandler / ThumbnailProvider / ContextMenu COM
             classes are all optional. -->
        <desktop3:Extension Category="windows.cloudFiles">
          <desktop3:CloudFiles />
        </desktop3:Extension>
        <uap5:Extension Category="windows.appExecutionAlias"
                        Executable="daemon.test.exe"
                        EntryPoint="Windows.FullTrustApplication">
          <uap5:AppExecutionAlias>
            <uap5:ExecutionAlias Alias="cernbox-daemon-test.exe" />
          </uap5:AppExecutionAlias>
        </uap5:Extension>
      </Extensions>
    </Application>
  </Applications>

  <Capabilities>
    <rescap:Capability Name="runFullTrust" />
  </Capabilities>
</Package>
