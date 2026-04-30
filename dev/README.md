# Dev Environment

This directory contains a local development environment that simulates a minimal CERNBox stack. It is meant for testing the sync client against a real WebDAV endpoint backed by EOS storage.

## Architecture

Two containers are started via Docker Compose:

- **eos-storage** — a standalone EOS instance (MGM + FST + QuarkDB) that provides the actual file storage. It exposes gRPC on port `50051` and HTTPS on port `8443`.
- **revad** — a [reva](https://github.com/cs3org/reva) gateway that fronts EOS and exposes HTTP WebDAV on port `80`. It handles authentication, routing, and the WebDAV/OCS/ocdav HTTP surface that the sync client talks to.

`revad` depends on `eos-storage` being healthy before it starts.

## Demo users

Three users are pre-created in both EOS and the reva auth provider:

| Username   | Password        | Home path              |
|------------|-----------------|------------------------|
| `einstein` | `relativity`    | `/eos/user/e/einstein` |
| `marie`    | `radioactivity` | `/eos/user/m/marie`    |
| `richard`  | `superfluidity` | `/eos/user/r/richard`  |

WebDAV is reachable at `http://localhost/remote.php/webdav/`.

## Starting and stopping

```bash
# Start (builds images if needed, runs in the background)
make dev-up

# Stop and remove containers
make dev-down
```

The first run will take a few minutes because both images are built from scratch. `eos-storage` needs extra time on first boot to initialise the namespace and register the FST — the `revad` container waits for its health check to pass before starting.

## Connecting the sync client

Register a sync pair pointing at a demo user's WebDAV root:

```bash
./cernbox-sync add \
  --name dev \
  --local /tmp/cernbox-dev \
  --remote http://localhost/remote.php/webdav/ \
  --username einstein \
  --password relativity
```

Then start the daemon (or trigger a manual sync cycle) as usual.

## Helper scripts

Two shell scripts in `scripts/` let you interact with the running stack from the host without any extra tooling beyond `curl` (and optionally `jq` / `xmllint` for pretty output).

All scripts default to the `einstein` / `relativity` credentials and `http://localhost` as the base URL. Override with environment variables.

---

### `scripts/webdav.sh` — WebDAV client

```
WEBDAV_URL   base URL  (default: http://localhost/remote.php/webdav)
WEBDAV_USER  username  (default: einstein)
WEBDAV_PASS  password  (default: relativity)
```

| Command | Description |
|---------|-------------|
| `list <path>` | `PROPFIND Depth:1` — list a remote directory |
| `get <remote> [local]` | Download a file; prints to stdout if no local path given |
| `put <local> <remote>` | Upload a file |
| `mkdir <path>` | Create a directory tree (`MKCOL`, like `mkdir -p`) |
| `delete <path>` | Delete a file or directory |
| `move <src> <dst>` | Move / rename a remote resource |

```bash
# List the root
./dev/scripts/webdav.sh list /

# Upload a file
./dev/scripts/webdav.sh put ./README.md /README.md

# Download it back
./dev/scripts/webdav.sh get /README.md /tmp/README.md

# Create a folder tree
./dev/scripts/webdav.sh mkdir /projects/alpha

# Move the file into it
./dev/scripts/webdav.sh move /README.md /projects/alpha/README.md

# Delete the folder
./dev/scripts/webdav.sh delete /projects

# Act as a different user
WEBDAV_USER=marie WEBDAV_PASS=radioactivity ./dev/scripts/webdav.sh list /
```

---

### `scripts/graph.sh` — LibreGraph API client

Talks to the reva `ocgraph` service at `http://localhost/graph`.

```
GRAPH_URL   base URL  (default: http://localhost/graph)
GRAPH_USER  username  (default: einstein)
GRAPH_PASS  password  (default: relativity)
```

Pass `-v` before the command to print the HTTP method and URL.

**Users**

| Command | Description |
|---------|-------------|
| `me` | Current user's profile |
| `me-change-password --current <p> --new <p>` | Change own password |
| `users [--expand-groups]` | List all users |
| `user <id-or-name> [--expand-groups]` | Get a specific user |
| `user-create --display-name <n> --mail <m> --account <a> --password <p>` | Create a user |
| `user-update <id> [--display-name] [--mail] [--account]` | Update a user |
| `user-delete <id>` | Delete a user |

**Groups**

| Command | Description |
|---------|-------------|
| `groups` | List all groups |

**Spaces**

| Command | Description |
|---------|-------------|
| `spaces [--filter <expr>] [--expand <expr>]` | List all spaces |
| `my-spaces` | Spaces the authenticated user belongs to |
| `space <id>` | Get a specific space |
| `space-create --name <n> [--description <d>] [--quota <bytes>]` | Create a space |
| `space-update <id> [--name] [--description] [--alias] [--quota]` | Update a space |
| `space-disable <id>` | Soft-delete a space |
| `space-restore <id>` | Re-enable a disabled space |
| `space-purge <id>` | Permanently delete a disabled space |

**Shares**

| Command | Description |
|---------|-------------|
| `shared-with-me` | Items shared with the current user |
| `shared-by-me` | Items shared by the current user |
| `role-definitions` | List available permission roles |

**Space permissions**

| Command | Description |
|---------|-------------|
| `space-permissions <space-id>` | List permissions on a space |
| `space-invite <space-id> --recipients <json> --roles <json>` | Add members |
| `space-create-link <space-id> --type <t>` | Create a share link |
| `space-update-permission <space-id> <perm-id> --roles <json>` | Update a permission |
| `space-delete-permission <space-id> <perm-id>` | Remove a permission |
| `space-set-link-password <space-id> <perm-id> --password <p>` | Set link password |

**Drive item permissions**

| Command | Description |
|---------|-------------|
| `drive-permissions <space-id> <resource-id>` | List item permissions |
| `drive-invite <space-id> <resource-id> --recipients <json> --roles <json>` | Share an item |
| `drive-create-link <space-id> <resource-id> --type <t>` | Create an item share link |
| `update-received-share <space-id> <resource-id> --hidden <bool>` | Hide/unhide a share |
| `update-permission <space-id> <resource-id> <perm-id> --roles <json>` | Update item permission |
| `delete-permission <space-id> <resource-id> <perm-id>` | Remove item permission |
| `set-link-password <space-id> <resource-id> <perm-id> --password <p>` | Set item link password |

```bash
# Inspect the current user
./dev/scripts/graph.sh me

# List all users
./dev/scripts/graph.sh users

# List spaces visible to einstein
./dev/scripts/graph.sh my-spaces

# Filter for project spaces
./dev/scripts/graph.sh spaces --filter "driveType eq 'project'"

# Create a project space
./dev/scripts/graph.sh space-create --name "My Project" --quota 10737418240

# See what roles are available before sharing
./dev/scripts/graph.sh role-definitions
```

---

## Windows VM (for testing Windows-specific code)

The Windows-specific code paths — Cloud Files API integration for on-demand sync, the Tauri GUI on Windows, and the MSIX packaging that the modern WinRT registration API requires — are built and tested inside a local QEMU/KVM Windows VM. The Makefile provisions, runs, and tears it down.

### Why MSIX (the on-demand path requires package identity)

Phase 2 of the on-demand work migrates from the legacy `CfRegisterSyncRoot` (`cldapi.dll`) to the modern `StorageProviderSyncRootManager.Register` (WinRT). The WinRT call refuses to run unless the calling process has MSIX package identity (it throws `E_NO_PACKAGE_IDENTITY` otherwise). That has two consequences for development:

- **The daemon must be packaged** to register sync roots. We ship a Desktop Bridge MSIX containing the Tauri GUI and the daemon as two declared `<Application>` entries (so the OS grants execute ACLs to both — without that the GUI's CreateProcess on the daemon fails with `ERROR_ACCESS_DENIED`).
- **The cloudfiles tests must run packaged too**. `make test-windows` therefore stages the test binary inside its own throwaway MSIX (different identity from the GUI package) and invokes it via an `uap5:AppExecutionAlias` so stdout/stderr/exitcode pipe through normally.

The actual WinRT call lives in **`cernbox-cf.dll`** (`cloudfiles/winrt/cernbox-cf.cpp`), a tiny C++/WinRT shim that the Go side loads via `LazyDLL` — same pattern `cfapi_syscall_windows.go` uses for `cldapi.dll`. C++/WinRT requires MSVC (MinGW's projection support is incomplete), so the VM has both toolchains: MinGW for cgo and the daemon, MSVC for the shim.

### Prerequisites (Arch Linux)

```bash
sudo pacman -S qemu-full edk2-ovmf xorriso virt-viewer samba
```

`virt-viewer` provides `remote-viewer` for the SPICE display used by `make windows-vm-gui` (clipboard sharing, dynamic resolution); `samba` provides `smbd` for QEMU's built-in SMB share that mounts the host repo as `Z:` inside the VM. A Windows installation ISO is also required — the **Windows 11 Multi-Edition** or **Windows 11 Enterprise Evaluation** ISO from the Microsoft Evaluation Center.

### One-time setup

```bash
# 1. Run the unattended Windows installer (~25 min, displays an SDL window)
make windows-vm-create WINDOWS_ISO=/path/to/win11.iso

# When Windows reaches the desktop and the FirstLogonCommands finish,
# shut it down from the Start menu, then:

# 2. Boot it headless in the background
make windows-vm-start

# 3. Provision the toolchain inside the VM (~30 min on first run, idempotent on re-runs)
make windows-vm-setup
```

`setup.ps1` runs over SSH and installs:

- **Chocolatey**, then via choco: Go, Git, MinGW (for cgo), Node.js LTS, WebView2 runtime
- **Visual Studio Build Tools 2022** with the VCTools workload + Win11 SDK 22621 (for compiling `cernbox-cf.dll` — heavy, ~7–9 GB)
- **Rust** via rustup, defaulted to the GNU host so it links with MinGW (avoids a second linker dependency for the daemon side)
- **SPICE guest tools** so `make windows-vm-gui` clipboard sharing works
- A few system tweaks: SMB client compat for QEMU's built-in share (allow guest auth + disable mandatory packet signing), a `localhost:80 → 10.0.2.2:80` portproxy so the in-VM GUI can reach the host's revad as `localhost`, `CARGO_TARGET_DIR` pinned to local NTFS so cargo doesn't crawl over the SMB share, and a logon-script that mounts the host repo as `Z:` on every interactive logon

The installer is fully unattended: it picks the edition, accepts the EULA, partitions the disk, creates `testuser` (Administrators group, password `TestPass123!`), enables OpenSSH Server, opens the firewall, and installs the host's generated SSH public key. Windows 11 hardware checks (TPM, Secure Boot, RAM) are bypassed via registry keys written during the WinPE phase.

> **Developer Mode** must be enabled in the guest for `Add-AppxPackage -Register` against an unpacked folder layout, which both `gui-msix-dev` and `test-windows` rely on. `setup.ps1` does not toggle this; if you hit `HRESULT 0x80073CFF` ("a Sideload Solution is required") during registration, enable it in `Settings → Privacy & security → For developers → Developer Mode`.

### Selecting a different Windows edition

The default targets `Windows 11 Pro` with the public Pro KMS client setup key. To install another edition, override **both** variables — the key must match the edition name:

```bash
make windows-vm-create WINDOWS_ISO=... \
  WINDOWS_IMAGE_NAME="Windows 11 Home" \
  WINDOWS_PRODUCT_KEY=TX9XD-98N7V-6WMQ6-BX7FG-H8Q99
```

The Makefile lists the public KMS keys for Pro, Enterprise, Education, and Home in a comment near the variable definition.

### Day-to-day VM commands

| Command | Description |
|---------|-------------|
| `make windows-vm-start`  | Boot the VM headless in the background |
| `make windows-vm-stop`   | Graceful shutdown via QEMU monitor |
| `make windows-vm-status` | Show whether the VM is running |
| `make windows-vm-ssh`    | Interactive SSH session as `testuser` |
| `make windows-vm-gui`    | Boot with SPICE display + Z:-mounted host repo for interactive dev |

SSH listens on `localhost:2222` with key auth via `dev/windows/id_ed25519`. The VM disk lives at `dev/windows/windows.qcow2` (40 GB thin-provisioned). `make clean` wipes all VM artifacts except the Windows ISO itself.

When started with `make windows-vm-gui`, the host repo is mounted at `Z:\` inside the VM via QEMU's built-in SMB. That makes the next two workflows possible without any tar/upload roundtrip.

### Build & test workflows

There are three flows, ranked from fastest iteration to most production-faithful:

#### Inner loop — Tauri dev mode (no MSIX, no on-demand)

```bash
make windows-vm-gui            # SPICE viewer launches
# in the VM PowerShell:
Z:\dev\windows\run-dev.ps1     # npm run tauri dev with hot reload
```

Hot-reloads frontend changes in milliseconds, rebuilds the daemon and Rust shell on save. **No package identity, so on-demand sync is disabled** — the daemon logs `on-demand sync requires MSIX install` and falls back to plain download/upload sync. Use this for everything that isn't on-demand-specific (UI work, sync algorithm, daemon logic, WebDAV client, etc.).

#### Middle loop — fast MSIX iteration (Add-AppxPackage -Register)

```bash
make gui-msix-dev              # builds DLL+daemon+GUI, stages, registers (no signing)
```

Builds `cernbox-cf.dll` via MSVC, the daemon via cgo+MinGW, and the GUI via Tauri+Vite. Stages the layout into `C:\cernbox-sync-msix-stage\` and registers it as an MSIX with `Add-AppxPackage -Register`. **Skips MakeAppx pack and signtool sign entirely** — the registered package reads files directly from the staging directory.

Cold round-trip ~3 min, warm ~30s. After registration:

```powershell
$pkg = Get-AppxPackage ch.cern.cernbox-sync
Start-Process "shell:AppsFolder\$($pkg.PackageFamilyName)!App"

# Or run the daemon directly to see its log output:
& "$($pkg.InstallLocation)\cernbox-syncd.exe" -log-level debug
```

For an even faster loop you can run the underlying script directly inside a `make windows-vm-gui` session, which skips the tar/upload step entirely (the host repo is right there at `Z:\`):

```powershell
Z:\dev\windows\register-msix-dev.ps1                 # full build + register
Z:\dev\windows\register-msix-dev.ps1 -NoBuild         # re-stage + re-register only
```

The dev MSIX is built in **Vite development mode** so the GUI bundle's `VITE_SERVER_URL` defaults to `http://localhost` (resolved to the host's revad via the in-VM portproxy). To build a production-pointing bundle:

```bash
make gui-msix-dev MSIX_SERVER_URL=https://cernbox.cern.ch
```

#### Outer loop — signed MSIX (production-faithful)

```bash
make windows-dev-cert          # one-time: self-signed code-signing cert in C:\cernbox-sync-cert\
make gui-msix                  # full build + MakeAppx pack + signtool sign
```

Produces a signed `.msix` at `dev/windows/out/CERNBox-Sync_<version>_x64.msix` that can be `Add-AppxPackage`'d on a clean machine. ~3–4 min cold, ~1 min warm. Use when validating the actual distribution artefact or testing on a different VM.

The dev cert lives at `C:\cernbox-sync-cert\cernbox-sync-dev.pfx` inside the VM (outside the workspace so it survives `make gui-msix`'s workspace wipes). Production code-signing happens out of band.

### Building the Cloud Files shim by itself

Sometimes you want to validate the C++/WinRT side compiles without doing a full MSIX build:

```bash
make windows-build-cfdll       # cl.exe + Win11 SDK headers, output to gui/src-tauri/binaries/cernbox-cf.dll
```

The build script auto-detects MSVC via `vswhere`, sources the x64 dev environment via `Microsoft.VisualStudio.DevShell` (the same `Enter-VsDevShell` PowerShell module Visual Studio uses), and invokes `cl.exe` directly with the right include and library flags.

### Tests

```bash
make test-windows
```

Runs in two halves:

1. **Plain `go test -tags windows`** for everything under `./...` *except* the cloudfiles package — engine, daemon, db, ipc, etc. None of those need package identity.
2. **`run-tests-msix.ps1`** for the cloudfiles package. The script:
   - Builds `cernbox-cf.dll` (skip with `-NoCfDll`)
   - Compiles the test binary: `go test -c -tags windows ./cloudfiles -o cloudfiles.test.exe`
   - Stages it inside `C:\cernbox-sync-tests-stage\` with a tests-only `AppxManifest.xml` (identity `ch.cern.cernbox-sync-tests`, distinct from the GUI MSIX so test runs don't collide with a registered dev install)
   - The manifest declares an `uap5:AppExecutionAlias` named `cernbox-cloudfiles-test.exe`. After `Add-AppxPackage -Register`, that alias appears at `%LOCALAPPDATA%\Microsoft\WindowsApps\` and behaves like any other console exe — except it carries package identity, so the WinRT registration code paths actually work.
   - Runs the alias with `-test.v -test.timeout=300s`, captures the exit code, then `Remove-AppxPackage` in a `finally` block (no leaked stale registrations even on failure).
   - Auto-versions the manifest per run so re-registration always succeeds without manual bumps.

Directly inside the VM:

```powershell
Z:\dev\windows\run-tests-msix.ps1                              # full run
Z:\dev\windows\run-tests-msix.ps1 -NoCfDll -NoBuild            # just re-run existing binary
Z:\dev\windows\run-tests-msix.ps1 -test.run TestSyncRoot_RegisterConnect
Z:\dev\windows\run-tests-msix.ps1 -Timeout 600s                # for the e2e test
```

The integration test harness (`make test-windows-integration`) runs the build-tagged `// +build windows && integration` tests against the local revad. It currently still uses plain `go test`; once Phase 2 is verified working it'll be migrated to the MSIX harness too.

### Cleaning up stale sync roots

If you've registered sync roots from earlier dev iterations that the daemon doesn't know about — typically because folders were removed manually, or a registration crashed mid-way, or you're upgrading from a build that didn't unregister on folder removal — there's a helper script:

```powershell
# List what's registered for our provider:
Z:\dev\scripts\windows\unregister-sync-roots.ps1 -List

# See raw registry contents (useful when -List shows nothing but Explorer
# still shows cloud icons - the entries may be under a different layout):
Z:\dev\scripts\windows\unregister-sync-roots.ps1 -Dump

# Unregister a specific local root, or all of them:
Z:\dev\scripts\windows\unregister-sync-roots.ps1 -LocalRoot 'C:\Users\testuser\CERNBox'
Z:\dev\scripts\windows\unregister-sync-roots.ps1 -All

# If the API call fails (folder deleted out from under the registration,
# etc) fall back to a registry+fsutil scrub:
Z:\dev\scripts\windows\unregister-sync-roots.ps1 -All -Force
```

The script calls `CfUnregisterSyncRoot` via P/Invoke and identifies "our" registrations by the `ProviderName` REG_SZ value (`cernbox-sync`) under each `HKCU\…\SyncRootManager\<id>` key — not by key name, since the OS auto-builds those.

The daemon itself now calls `CfUnregisterSyncRoot` when a folder is removed via the GUI/CLI, so the script is mostly an escape hatch for stale state from older builds.

### Package layout cheatsheet

What ends up where, and why:

| Path on disk | Persistent? | Contents | Set up by |
|---|---|---|---|
| `C:\workspace\cernbox-sync\` | wiped each `make` run | source tree (tar-uploaded from host) | `make` recipes |
| `C:\dev-cache\cargo-target\` | persistent | cargo build cache | `setup.ps1` |
| `C:\cernbox-sync-cert\` | persistent | dev signing cert (`.pfx` + `.cer`) | `make windows-dev-cert` |
| `C:\cernbox-sync-msix-stage\` | persistent | unpacked MSIX layout, registered as `ch.cern.cernbox-sync` | `register-msix-dev.ps1` |
| `C:\cernbox-sync-tests-stage\` | per-run | unpacked test MSIX, `ch.cern.cernbox-sync-tests` | `run-tests-msix.ps1` (cleaned up at end) |
| `dev/windows/out/*.msix` | persistent (host) | signed MSIX artefacts pulled back from `make gui-msix` | `make gui-msix` |

---

## Directory layout

```
dev/
├── docker-compose.yaml   # Service definitions
├── Dockerfile            # revad image (builds from source via gaia)
├── Dockerfile.eos        # EOS image (from CERN CI registry)
├── pki/                  # TLS certificates for EOS internal HTTPS/gRPC
│   ├── ca.crt / ca.key
│   └── eos.crt / eos.key
├── revad/
│   ├── cernbox.toml      # reva configuration (gateway, storage, auth, WebDAV)
│   ├── users.demo.json   # Demo user credentials and metadata
│   └── groups.demo.json  # Demo group definitions
├── scripts/
│   ├── eos-run.sh        # EOS container entrypoint (init + daemon launch)
│   ├── webdav.sh         # WebDAV client (list, get, put, mkdir, delete, move)
│   ├── graph.sh          # LibreGraph API client (users, spaces, shares, permissions)
│   └── windows/
│       └── unregister-sync-roots.ps1  # Cloud Files sync-root cleanup helper
└── windows/
    ├── autounattend.xml.tpl       # Unattended Windows install answer file (template)
    ├── setup.ps1                  # Post-install: Go, Git, MinGW, MSVC, SPICE, Rust, Node
    ├── run-dev.ps1                # `npm run tauri dev` (inner loop, no MSIX)
    ├── run-build.ps1              # Release build (daemon + cernbox-cf.dll + tauri build)
    ├── new-dev-cert.ps1           # Self-signed code-signing cert for MSIX dev builds
    ├── make-msix.ps1              # MakeAppx pack + signtool sign (signed `.msix` artefact)
    ├── register-msix-dev.ps1      # Add-AppxPackage -Register against unpacked layout (fast loop)
    ├── run-tests-msix.ps1         # Stages cloudfiles test binary as MSIX, runs via AppExecutionAlias
    └── tests-AppxManifest.tpl     # Manifest for the tests-only package
```
