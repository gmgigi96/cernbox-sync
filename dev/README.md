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

The Windows-specific code paths (e.g. the Cloud Files API integration for on-demand sync) are built and tested inside a local QEMU/KVM Windows VM. The Makefile provisions, runs, and tears it down.

### Prerequisites (Arch Linux)

```bash
sudo pacman -S qemu-full edk2-ovmf xorriso
```

A Windows installation ISO is also required. Download the **Windows 11 Multi-Edition** or **Windows 11 Enterprise Evaluation** ISO from the Microsoft Evaluation Center.

### One-time setup

```bash
# 1. Run the unattended Windows installer (~25 min, displays an SDL window)
make windows-vm-create WINDOWS_ISO=/path/to/win11.iso

# When Windows reaches the desktop and the FirstLogonCommands finish,
# shut it down from the Start menu, then:

# 2. Boot it headless in the background
make windows-vm-start

# 3. Install Go, Git, and MinGW inside the VM (~10 min)
make windows-vm-setup
```

The installer is fully unattended: it picks the edition, accepts the EULA, partitions the disk, creates `testuser` (Administrators group, password `TestPass123!`), enables OpenSSH Server, opens the firewall, and installs the host's generated SSH public key. Windows 11 hardware checks (TPM, Secure Boot, RAM) are bypassed via registry keys written during the WinPE phase.

### Selecting a different edition

The default targets `Windows 11 Pro` with the public Pro KMS client setup key. To install another edition, override **both** variables — the key must match the edition name:

```bash
make windows-vm-create WINDOWS_ISO=... \
  WINDOWS_IMAGE_NAME="Windows 11 Home" \
  WINDOWS_PRODUCT_KEY=TX9XD-98N7V-6WMQ6-BX7FG-H8Q99
```

The Makefile lists the public KMS keys for Pro, Enterprise, Education, and Home in a comment near the variable definition.

### Day-to-day commands

| Command | Description |
|---------|-------------|
| `make windows-vm-start`  | Boot the VM headless in the background |
| `make windows-vm-stop`   | Graceful shutdown via QEMU monitor |
| `make windows-vm-status` | Show whether the VM is running |
| `make windows-vm-ssh`    | Interactive SSH session as `testuser` |
| `make test-windows`      | Ship the source into the VM and run `go test -tags windows ./...` |

SSH listens on `localhost:2222` with key authentication via `dev/windows/id_ed25519`. The VM disk lives at `dev/windows/windows.qcow2` (40 GB thin-provisioned). `make clean` wipes all VM artifacts except the Windows ISO itself.

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
│   └── graph.sh          # LibreGraph API client (users, spaces, shares, permissions)
└── windows/
    ├── autounattend.xml.tpl  # Unattended Windows install answer file (template)
    └── setup.ps1             # Post-install: installs Go, Git, MinGW via Chocolatey
```
