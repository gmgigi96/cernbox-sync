<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend">

  <!-- ── Windows PE phase: runs during setup boot ──────────────────────────── -->
  <settings pass="windowsPE">

    <component name="Microsoft-Windows-International-Core-WinPE"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral" versionScope="nonSxS"
               xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State"
               xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
      <SetupUILanguage>
        <UILanguage>en-US</UILanguage>
      </SetupUILanguage>
      <InputLocale>0409:00000409</InputLocale>
      <SystemLocale>en-US</SystemLocale>
      <UILanguage>en-US</UILanguage>
      <UserLocale>en-US</UserLocale>
    </component>

    <component name="Microsoft-Windows-Setup"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral" versionScope="nonSxS"
               xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State"
               xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">

      <!-- Windows 11 hardware compatibility bypasses (TPM 2.0, Secure Boot,
           RAM, Storage, CPU). Harmless on Windows 10. -->
      <RunSynchronous>
        <RunSynchronousCommand wcm:action="add">
          <Order>1</Order>
          <Path>cmd /c reg add HKLM\SYSTEM\Setup\LabConfig /v BypassTPMCheck /t REG_DWORD /d 1 /f</Path>
        </RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add">
          <Order>2</Order>
          <Path>cmd /c reg add HKLM\SYSTEM\Setup\LabConfig /v BypassSecureBootCheck /t REG_DWORD /d 1 /f</Path>
        </RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add">
          <Order>3</Order>
          <Path>cmd /c reg add HKLM\SYSTEM\Setup\LabConfig /v BypassRAMCheck /t REG_DWORD /d 1 /f</Path>
        </RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add">
          <Order>4</Order>
          <Path>cmd /c reg add HKLM\SYSTEM\Setup\LabConfig /v BypassStorageCheck /t REG_DWORD /d 1 /f</Path>
        </RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add">
          <Order>5</Order>
          <Path>cmd /c reg add HKLM\SYSTEM\Setup\LabConfig /v BypassCPUCheck /t REG_DWORD /d 1 /f</Path>
        </RunSynchronousCommand>
      </RunSynchronous>

      <!-- GPT disk layout: EFI (100 MB) + MSR (16 MB) + Windows (rest) -->
      <DiskConfiguration>
        <Disk wcm:action="add">
          <DiskID>0</DiskID>
          <WillWipeDisk>true</WillWipeDisk>
          <CreatePartitions>
            <CreatePartition wcm:action="add">
              <Order>1</Order>
              <Type>EFI</Type>
              <Size>100</Size>
            </CreatePartition>
            <CreatePartition wcm:action="add">
              <Order>2</Order>
              <Type>MSR</Type>
              <Size>16</Size>
            </CreatePartition>
            <CreatePartition wcm:action="add">
              <Order>3</Order>
              <Type>Primary</Type>
              <Extend>true</Extend>
            </CreatePartition>
          </CreatePartitions>
          <ModifyPartitions>
            <ModifyPartition wcm:action="add">
              <Order>1</Order>
              <PartitionID>1</PartitionID>
              <Format>FAT32</Format>
              <Label>System</Label>
            </ModifyPartition>
            <ModifyPartition wcm:action="add">
              <Order>2</Order>
              <PartitionID>2</PartitionID>
            </ModifyPartition>
            <ModifyPartition wcm:action="add">
              <Order>3</Order>
              <PartitionID>3</PartitionID>
              <Format>NTFS</Format>
              <Label>Windows</Label>
              <Letter>C</Letter>
            </ModifyPartition>
          </ModifyPartitions>
        </Disk>
      </DiskConfiguration>

      <ImageInstall>
        <OSImage>
          <InstallFrom>
            <MetaData wcm:action="add">
              <Key>/IMAGE/NAME</Key>
              <Value>__IMAGE_NAME__</Value>
            </MetaData>
          </InstallFrom>
          <InstallTo>
            <DiskID>0</DiskID>
            <PartitionID>3</PartitionID>
          </InstallTo>
          <WillShowUI>OnError</WillShowUI>
        </OSImage>
      </ImageInstall>

      <UserData>
        <AcceptEula>true</AcceptEula>
        <ProductKey>
          <Key>__PRODUCT_KEY__</Key>
          <WillShowUI>OnError</WillShowUI>
        </ProductKey>
      </UserData>

    </component>
  </settings>

  <!-- ── Specialize phase: after install, before first login ───────────────── -->
  <settings pass="specialize">
    <component name="Microsoft-Windows-Shell-Setup"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral" versionScope="nonSxS"
               xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State"
               xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
      <ComputerName>WIN-TEST</ComputerName>
      <TimeZone>UTC</TimeZone>
    </component>
    <component name="Microsoft-Windows-Security-SPP-UX"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral" versionScope="nonSxS"
               xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State"
               xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
      <SkipAutoActivation>true</SkipAutoActivation>
    </component>
  </settings>

  <!-- ── OOBE phase: first login ───────────────────────────────────────────── -->
  <settings pass="oobeSystem">
    <component name="Microsoft-Windows-Shell-Setup"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral" versionScope="nonSxS"
               xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State"
               xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">

      <AutoLogon>
        <Enabled>true</Enabled>
        <Username>testuser</Username>
        <LogonCount>3</LogonCount>
        <Password>
          <Value>TestPass123!</Value>
          <PlainText>true</PlainText>
        </Password>
      </AutoLogon>

      <OOBE>
        <HideEULAPage>true</HideEULAPage>
        <HideLocalAccountScreen>true</HideLocalAccountScreen>
        <HideOEMRegistrationScreen>true</HideOEMRegistrationScreen>
        <HideOnlineAccountScreens>true</HideOnlineAccountScreens>
        <HideWirelessSetupInOOBE>true</HideWirelessSetupInOOBE>
        <NetworkLocation>Work</NetworkLocation>
        <ProtectYourPC>3</ProtectYourPC>
        <SkipUserOOBE>true</SkipUserOOBE>
        <SkipMachineOOBE>true</SkipMachineOOBE>
      </OOBE>

      <UserAccounts>
        <LocalAccounts>
          <LocalAccount wcm:action="add">
            <Name>testuser</Name>
            <DisplayName>testuser</DisplayName>
            <Group>Administrators</Group>
            <Password>
              <Value>TestPass123!</Value>
              <PlainText>true</PlainText>
            </Password>
          </LocalAccount>
        </LocalAccounts>
      </UserAccounts>

      <!-- Commands run on first login as testuser (Administrators group) -->
      <FirstLogonCommands>

        <!-- Give network time to initialize before hitting Windows Update -->
        <SynchronousCommand wcm:action="add">
          <Order>1</Order>
          <Description>Wait for network</Description>
          <CommandLine>powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "Start-Sleep 20"</CommandLine>
        </SynchronousCommand>

        <!-- Install OpenSSH Server from Windows optional features -->
        <SynchronousCommand wcm:action="add">
          <Order>2</Order>
          <Description>Install OpenSSH Server</Description>
          <CommandLine>powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0"</CommandLine>
        </SynchronousCommand>

        <!-- Start sshd and set it to start automatically -->
        <SynchronousCommand wcm:action="add">
          <Order>3</Order>
          <Description>Enable sshd service</Description>
          <CommandLine>powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "Start-Service sshd; Set-Service sshd -StartupType Automatic"</CommandLine>
        </SynchronousCommand>

        <!-- Allow TCP/22 through Windows Firewall -->
        <SynchronousCommand wcm:action="add">
          <Order>4</Order>
          <Description>Open firewall port 22</Description>
          <CommandLine>powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "New-NetFirewallRule -Name OpenSSH-Server -DisplayName 'OpenSSH Server' -Enabled True -Direction Inbound -Protocol TCP -Action Allow -LocalPort 22"</CommandLine>
        </SynchronousCommand>

        <!-- Use PowerShell as the default shell for SSH sessions -->
        <SynchronousCommand wcm:action="add">
          <Order>5</Order>
          <Description>Set PowerShell as default SSH shell</Description>
          <CommandLine>powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "New-ItemProperty -Force -Path 'HKLM:\SOFTWARE\OpenSSH' -Name DefaultShell -Value 'C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe' -PropertyType String"</CommandLine>
        </SynchronousCommand>

        <!--
          By default Windows OpenSSH routes Administrators through
          C:\ProgramData\ssh\administrators_authorized_keys.
          Remove that override so the per-user ~/.ssh/authorized_keys is used.
        -->
        <SynchronousCommand wcm:action="add">
          <Order>6</Order>
          <Description>Patch sshd_config for user-level authorized_keys</Description>
          <CommandLine>powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "$f='C:\ProgramData\ssh\sshd_config'; (Get-Content $f) | Where-Object { $_ -notmatch 'Match Group administrators' -and $_ -notmatch '__PROGRAMDATA__' } | Set-Content $f"</CommandLine>
        </SynchronousCommand>

        <!-- Install the host's SSH public key so passwordless login works -->
        <SynchronousCommand wcm:action="add">
          <Order>7</Order>
          <Description>Install SSH public key</Description>
          <CommandLine>powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "New-Item -Force -ItemType Directory 'C:\Users\testuser\.ssh'; Set-Content -Path 'C:\Users\testuser\.ssh\authorized_keys' -Value '__SSH_PUBKEY__'"</CommandLine>
        </SynchronousCommand>

        <!-- Restart sshd to pick up the sshd_config change -->
        <SynchronousCommand wcm:action="add">
          <Order>8</Order>
          <Description>Restart sshd</Description>
          <CommandLine>powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "Restart-Service sshd"</CommandLine>
        </SynchronousCommand>

      </FirstLogonCommands>
    </component>
  </settings>

</unattend>
