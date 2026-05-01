CLI    := cernbox-sync
DAEMON := cernbox-syncd
GO     := go

COMPOSE := docker compose -f dev/docker-compose.yaml

# ── Windows VM settings ───────────────────────────────────────────────────────
WINDOWS_VM_DIR  := dev/windows
WINDOWS_DISK    := $(WINDOWS_VM_DIR)/windows.qcow2
WINDOWS_PID     := $(WINDOWS_VM_DIR)/vm.pid
WINDOWS_MON     := $(WINDOWS_VM_DIR)/vm.monitor
WINDOWS_KEY     := $(WINDOWS_VM_DIR)/id_ed25519
WINDOWS_ISO     ?= $(WINDOWS_VM_DIR)/windows.iso
# Public KMS client setup keys (Generic Volume License Keys). Override
# with WINDOWS_PRODUCT_KEY=... to match your ISO's edition.
#   Win 10/11 Pro:        W269N-WFGWX-YVC9B-4J6C9-T83GX  (default)
#   Win 10/11 Enterprise: NPPR9-FWDCX-D2C8J-H872K-2YT43
#   Win 10/11 Education:  NW6C2-QMPVW-D7KKK-3GKT6-VCFB2
#   Win 10/11 Home:       TX9XD-98N7V-6WMQ6-BX7FG-H8Q99
WINDOWS_PRODUCT_KEY ?= W269N-WFGWX-YVC9B-4J6C9-T83GX
# Edition name as it appears in the ISO's install.wim (run `wiminfo /path/to/install.wim`
# to list editions). Override to match WINDOWS_PRODUCT_KEY's edition.
WINDOWS_IMAGE_NAME  ?= Windows 11 Pro
OVMF_CODE       := /usr/share/edk2/x64/OVMF_CODE.4m.fd
OVMF_VARS_SRC   := /usr/share/edk2/x64/OVMF_VARS.4m.fd
OVMF_VARS       := $(WINDOWS_VM_DIR)/OVMF_VARS.fd
WINDOWS_SSH_CMD := ssh -i $(WINDOWS_KEY) -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR testuser@localhost
WINDOWS_SCP_CMD := scp -i $(WINDOWS_KEY) -P 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR

# Poll until SSH is ready (used inside recipes via $(call wait-for-windows-ssh))
define wait-for-windows-ssh
@printf "Waiting for Windows VM SSH (up to 5 min)...\n"
@for i in $$(seq 1 60); do \
	$(WINDOWS_SSH_CMD) "exit" 2>/dev/null && printf "SSH ready\n" && exit 0 || true; \
	printf "  [%02d/60] waiting...\n" $$i; \
	sleep 5; \
done; \
printf "Timed out waiting for SSH\n"; exit 1
endef

.PHONY: all build cli daemon test test-gui test-e2e test-e2e-watch test-all lint clean help dev-up dev-down gui gui-dev
.PHONY: windows-vm-create windows-vm-start windows-vm-stop windows-vm-status windows-vm-ssh windows-vm-setup windows-vm-gui test-windows test-windows-integration windows-dev-cert gui-msix gui-msix-dev windows-build-cfdll

all: build

help: ## Show this help message
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m\n\nTargets:\n"} \
	/^[a-zA-Z0-9_-]+:.*##/ { printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: cli daemon ## Build both binaries

cli: ## Build the CLI client (cernbox-sync)
	$(GO) build -o $(CLI) .

daemon: ## Build the background daemon (cernbox-syncd)
	$(GO) build -o $(DAEMON) ./cmd/cernbox-syncd

test: ## Run all tests
	$(GO) test ./...

test-gui: ## Run GUI unit tests (Vitest)
	cd gui && npm install && npm test

test-e2e: ## Run GUI end-to-end tests (Playwright, requires make dev-up)
	cd gui && npm install && npm run test:e2e

test-e2e-watch: ## Run e2e tests headed, serial, with slow-mo (for debugging)
	cd gui && npm install && PWHEADED=1 npx playwright test --workers=1

test-integration: build ## Run integration tests
	$(GO) test -v -tags integration -timeout 120s ./integration/

test-all: dev-up test test-gui test-integration test-e2e ## Run all tests (unit, GUI, integration, e2e)

lint: ## Run linters (go vet + golangci-lint)
	$(GO) vet ./...
	golangci-lint run ./...

fmt: ## Format code
	$(GO) fmt ./...

dev-up: ## Start dev services via docker compose
	$(COMPOSE) up --build -d

dev-down: ## Stop dev services via docker compose
	$(COMPOSE) down

gui: ## Build the GUI (Tauri app)
	$(GO) build -ldflags="-s -w" -o gui/src-tauri/binaries/cernbox-syncd-$$(rustc -vV | awk '/^host:/{print $$2}') ./cmd/cernbox-syncd
	cd gui && npm install && NO_STRIP=1 npm run tauri build

gui-dev: ## Start the GUI in development mode
	cd gui && npm install && npm run tauri dev

clean: ## Remove built binaries and generated VM artifacts
	$(GO) clean
	rm -f $(CLI) $(DAEMON)
	rm -rf gui/src-tauri/target gui/src-tauri/binaries gui/dist gui/node_modules
	rm -f $(WINDOWS_DISK) $(WINDOWS_PID) $(WINDOWS_VM_DIR)/vm.log $(WINDOWS_MON)
	rm -f $(WINDOWS_VM_DIR)/autounattend.xml $(WINDOWS_VM_DIR)/autounattend.iso
	rm -f $(WINDOWS_VM_DIR)/id_ed25519 $(WINDOWS_VM_DIR)/id_ed25519.pub
	rm -f $(OVMF_VARS)

# ── Windows VM targets ────────────────────────────────────────────────────────

windows-vm-create: ## Create the Windows VM and run the OS installer (one-time; set WINDOWS_ISO=/path/to/iso)
	@command -v qemu-system-x86_64 >/dev/null 2>&1 || { echo "Error: qemu-system-x86_64 not found — install qemu-full or qemu-base"; exit 1; }
	@command -v qemu-img          >/dev/null 2>&1 || { echo "Error: qemu-img not found — install qemu-img"; exit 1; }
	@command -v xorriso           >/dev/null 2>&1 || { echo "Error: xorriso not found — install xorriso"; exit 1; }
	@test -f "$(OVMF_CODE)" || { echo "Error: OVMF not found at $(OVMF_CODE) — install edk2-ovmf"; exit 1; }
	@test -f "$(WINDOWS_ISO)" || { echo "Error: ISO not found at $(WINDOWS_ISO) — download a Windows 10 Enterprise Evaluation ISO and set WINDOWS_ISO=/path/to/iso"; exit 1; }
	@test ! -f "$(WINDOWS_DISK)" || { echo "Error: $(WINDOWS_DISK) already exists — delete it first to recreate"; exit 1; }
	@# Generate SSH keypair (skip if already exists)
	@test -f "$(WINDOWS_KEY)" || \
		ssh-keygen -t ed25519 -f "$(WINDOWS_KEY)" -N "" -C "cernbox-sync-windows-test" -q
	@# Copy per-VM writable OVMF variable store
	cp "$(OVMF_VARS_SRC)" "$(OVMF_VARS)"
	@# Generate autounattend.xml from template (inject SSH key, product key, edition)
	sed -e "s|__SSH_PUBKEY__|$$(cat $(WINDOWS_KEY).pub)|g" \
		-e "s|__PRODUCT_KEY__|$(WINDOWS_PRODUCT_KEY)|g" \
		-e "s|__IMAGE_NAME__|$(WINDOWS_IMAGE_NAME)|g" \
		$(WINDOWS_VM_DIR)/autounattend.xml.tpl > $(WINDOWS_VM_DIR)/autounattend.xml
	@# Build a small ISO carrying the answer file
	mkdir -p $(WINDOWS_VM_DIR)/answer-iso-root
	cp $(WINDOWS_VM_DIR)/autounattend.xml $(WINDOWS_VM_DIR)/answer-iso-root/
	xorriso -as mkisofs -J -r -o $(WINDOWS_VM_DIR)/autounattend.iso \
		$(WINDOWS_VM_DIR)/answer-iso-root/ 2>/dev/null
	rm -rf $(WINDOWS_VM_DIR)/answer-iso-root
	@# Create 40 GB thin-provisioned disk image
	qemu-img create -f qcow2 "$(WINDOWS_DISK)" 40G
	@echo ""
	@echo "Starting Windows installer (20–30 min). The VM window will close when done."
	@echo "Then run: make windows-vm-start"
	@echo ""
	qemu-system-x86_64 \
		-machine q35,accel=kvm \
		-cpu host \
		-m 4G \
		-smp 4 \
		-drive if=pflash,format=raw,readonly=on,file=$(OVMF_CODE) \
		-drive if=pflash,format=raw,file=$(OVMF_VARS) \
		-device ahci,id=ahci \
		-drive id=disk,if=none,file=$(WINDOWS_DISK),format=qcow2 \
		-device ide-hd,drive=disk,bus=ahci.0 \
		-drive id=winiso,if=none,file=$(WINDOWS_ISO),media=cdrom,readonly=on \
		-device ide-cd,drive=winiso,bus=ahci.1,bootindex=1 \
		-drive id=ansiso,if=none,file=$(WINDOWS_VM_DIR)/autounattend.iso,media=cdrom,readonly=on \
		-device ide-cd,drive=ansiso,bus=ahci.2 \
		-netdev user,id=net0,hostfwd=tcp::2222-:22 \
		-device e1000e,netdev=net0 \
		-display sdl

windows-vm-start: ## Start the Windows VM headless in the background
	@test -f "$(WINDOWS_DISK)" || { echo "Error: VM disk not found — run: make windows-vm-create WINDOWS_ISO=..."; exit 1; }
	@if [ -f "$(WINDOWS_PID)" ] && kill -0 "$$(cat $(WINDOWS_PID))" 2>/dev/null; then \
		echo "VM already running (PID $$(cat $(WINDOWS_PID)))"; exit 0; \
	fi
	@echo "Starting Windows VM (headless)..."
	@nohup qemu-system-x86_64 \
		-machine q35,accel=kvm \
		-cpu host \
		-m 4G \
		-smp 4 \
		-drive if=pflash,format=raw,readonly=on,file=$(OVMF_CODE) \
		-drive if=pflash,format=raw,file=$(OVMF_VARS) \
		-device ahci,id=ahci \
		-drive id=disk,if=none,file=$(WINDOWS_DISK),format=qcow2 \
		-device ide-hd,drive=disk,bus=ahci.0 \
		-netdev user,id=net0,hostfwd=tcp::2222-:22 \
		-device e1000e,netdev=net0 \
		-monitor unix:$(WINDOWS_MON),server,nowait \
		-display none \
		> $(WINDOWS_VM_DIR)/vm.log 2>&1 & echo $$! > $(WINDOWS_PID)
	@echo "VM started (PID $$(cat $(WINDOWS_PID))). Log: $(WINDOWS_VM_DIR)/vm.log"

windows-vm-stop: ## Gracefully shut down the Windows VM
	@if [ ! -f "$(WINDOWS_PID)" ] || ! kill -0 "$$(cat $(WINDOWS_PID))" 2>/dev/null; then \
		echo "VM is not running"; rm -f $(WINDOWS_PID); exit 0; \
	fi
	@echo "Sending shutdown signal..."
	@{ command -v socat >/dev/null 2>&1 && \
		echo "system_powerdown" | socat - unix-connect:$(WINDOWS_MON) 2>/dev/null; } || \
		$(WINDOWS_SSH_CMD) "Stop-Computer -Force" 2>/dev/null || true
	@echo "Waiting for VM to stop (up to 60s)..."
	@for i in $$(seq 1 60); do \
		kill -0 "$$(cat $(WINDOWS_PID))" 2>/dev/null || { rm -f $(WINDOWS_PID); echo "VM stopped"; exit 0; }; \
		sleep 1; \
	done
	@echo "Force-killing VM..."
	@kill "$$(cat $(WINDOWS_PID))" 2>/dev/null || true
	@rm -f $(WINDOWS_PID)

windows-vm-status: ## Show whether the Windows VM is running
	@if [ -f "$(WINDOWS_PID)" ] && kill -0 "$$(cat $(WINDOWS_PID))" 2>/dev/null; then \
		echo "VM is running (PID $$(cat $(WINDOWS_PID)))"; \
	else \
		echo "VM is not running"; rm -f "$(WINDOWS_PID)" 2>/dev/null; \
	fi

windows-vm-ssh: ## Open an interactive SSH session into the Windows VM
	@test -f "$(WINDOWS_KEY)" || { echo "Error: SSH key not found — run: make windows-vm-create WINDOWS_ISO=..."; exit 1; }
	$(WINDOWS_SSH_CMD)

windows-vm-setup: ## Install Go, Git, and MinGW inside the Windows VM (run once after windows-vm-create)
	$(call wait-for-windows-ssh)
	@echo "Uploading setup script..."
	$(WINDOWS_SCP_CMD) $(WINDOWS_VM_DIR)/setup.ps1 testuser@localhost:C:/setup.ps1
	@echo "Running setup (installs Go, Git, MinGW — takes ~10 min)..."
	$(WINDOWS_SSH_CMD) "powershell.exe -ExecutionPolicy Bypass -File C:/setup.ps1"

windows-vm-gui: ## Start the Windows VM with a graphical display + repo mounted as Z: drive
	@test -f "$(WINDOWS_DISK)" || { echo "Error: VM disk not found — run: make windows-vm-create WINDOWS_ISO=..."; exit 1; }
	@command -v smbd >/dev/null 2>&1 || { echo "Error: smbd not found — install samba (Arch: pacman -S samba)"; exit 1; }
	@command -v remote-viewer >/dev/null 2>&1 || { echo "Error: remote-viewer not found — install virt-viewer (Arch: pacman -S virt-viewer)"; exit 1; }
	@if [ -f "$(WINDOWS_PID)" ] && kill -0 "$$(cat $(WINDOWS_PID))" 2>/dev/null; then \
		echo "Error: VM is already running headlessly (PID $$(cat $(WINDOWS_PID))). Stop it first with: make windows-vm-stop"; exit 1; \
	fi
	@echo "Starting Windows VM with graphical display (SPICE)..."
	@echo "SSH is also forwarded on localhost:2222 for make test-windows / test-windows-integration."
	@echo "The cernbox dev server on the host is reachable from the VM at http://10.0.2.2/"
	@echo "The host repo is mounted inside the VM as drive Z: (\\\\10.0.2.4\\qemu)."
	@echo "To run the GUI: open PowerShell in the VM and run  Z:\\dev\\windows\\run-dev.ps1"
	@# SPICE display + vdagent channel: enables host<->guest clipboard
	@# sharing and dynamic resize once spice-guest-tools is installed in
	@# the guest (windows-vm-setup does that). -display spice-app auto-
	@# launches remote-viewer; replace with `-display none` if you'd
	@# rather connect manually with `remote-viewer spice://localhost:5930`.
	qemu-system-x86_64 \
		-machine q35,accel=kvm \
		-cpu host \
		-m 4G \
		-smp 4 \
		-drive if=pflash,format=raw,readonly=on,file=$(OVMF_CODE) \
		-drive if=pflash,format=raw,file=$(OVMF_VARS) \
		-device ahci,id=ahci \
		-drive id=disk,if=none,file=$(WINDOWS_DISK),format=qcow2 \
		-device ide-hd,drive=disk,bus=ahci.0 \
		-netdev user,id=net0,hostfwd=tcp::2222-:22,smb=$(CURDIR) \
		-device e1000e,netdev=net0 \
		-device virtio-serial-pci \
		-spice port=5930,disable-ticketing=on \
		-device virtserialport,chardev=spicechannel0,name=com.redhat.spice.0 \
		-chardev spicevmc,id=spicechannel0,name=vdagent \
		-display spice-app

test-windows: ## Build and run Windows-specific tests inside the VM (plain go test; cernbox-cf.dll must be on PATH)
	$(call wait-for-windows-ssh)
	@echo "Uploading source..."
	@# Wipe the workspace first so files removed locally also disappear in
	@# the VM — tar -x merges into the destination but doesn't delete
	@# stale files from previous runs.
	$(WINDOWS_SSH_CMD) "if (Test-Path C:/workspace/cernbox-sync) { Remove-Item -Recurse -Force C:/workspace/cernbox-sync }; New-Item -Force -ItemType Directory C:/workspace/cernbox-sync | Out-Null"
	@tar -czf - \
		--exclude='.git' \
		--exclude='$(WINDOWS_VM_DIR)/*.qcow2' \
		--exclude='$(WINDOWS_VM_DIR)/*.iso' \
		. | $(WINDOWS_SSH_CMD) "tar -xzf - -C 'C:/workspace/cernbox-sync/'"
	@# Build the WinRT shim DLL first so cloudfiles/daemon tests can load
	@# it via LazyDLL. Tests skip cleanly (requireCfShim) if the DLL is
	@# missing, but we want them to actually run, so building is part of
	@# the recipe. The DLL lands in gui/src-tauri/binaries/; we surface it
	@# on PATH for the go test invocation that follows.
	@echo "Building cernbox-cf.dll inside VM (MSVC + Windows SDK)..."
	$(WINDOWS_SSH_CMD) "Set-Location C:/workspace/cernbox-sync; powershell.exe -ExecutionPolicy Bypass -File cloudfiles/winrt/build.ps1"
	@echo "Running Windows tests inside VM (plain go test)..."
	$(WINDOWS_SSH_CMD) "\$$Env:Path = 'C:\workspace\cernbox-sync\gui\src-tauri\binaries;' + \$$Env:Path; Set-Location C:/workspace/cernbox-sync; go test -tags windows ./..."

# Coordinates for test-windows-integration. Default points at the host machine
# via the QEMU user-networking gateway (10.0.2.2) so it works out of the box.
WEBDAV_BASE ?= http://10.0.2.2/remote.php/webdav/eos/user/e/einstein
E2E_USER    ?= einstein
E2E_PASS    ?= relativity

# ── MSIX packaging ────────────────────────────────────────────────────────────
#
# MSIX is the production install format for the Tauri GUI app on Windows -
# it gives Start-menu integration, an upgrade story, and the right deployment
# story for a CERN-signed shippable artefact.
#
# It is NOT required to call StorageProviderSyncRootManager.Register: the
# working ownCloud client registers from a plain unpackaged Win32 process
# (vfs_win.cpp ::registerFolder), and we follow that approach. Tests run
# unpackaged via plain `go test`; the MSIX targets here exist purely for
# end-to-end packaging verification of the GUI install bundle.
#
# These targets build a signed Desktop Bridge MSIX inside the Windows VM,
# pulling the artefact back to dev/windows/out/ on the host.
#
# Prereqs:
#   - VM created and set up: make windows-vm-create && make windows-vm-setup
#   - VM running:            make windows-vm-start  (or make windows-vm-gui)
#   - Tauri build present in the VM (gui/src-tauri/target/release). The
#     gui-msix target uploads the source tree and builds the bundle as a
#     prerequisite — same pattern as test-windows.
#   - One-time signing cert: make windows-dev-cert  (generates a self-signed
#     code-signing cert in the VM under dev/windows/cert/).

# Persistent VM-side location for the dev signing cert. Lives outside
# C:/workspace/cernbox-sync because gui-msix wipes that tree each run; we
# don't want to re-generate the cert (and re-trust it) on every build.
WINDOWS_VM_CERT_DIR := C:/cernbox-sync-cert

# Backend URL baked into the GUI bundle (vite VITE_SERVER_URL). Defaults to
# the local revad reachable from the VM (setup.ps1 forwards localhost:80 to
# the host's 10.0.2.2:80 via netsh portproxy). For a production-pointing
# bundle, override:  make gui-msix MSIX_SERVER_URL=https://cernbox.cern.ch
MSIX_SERVER_URL ?= http://localhost

windows-build-cfdll: ## Compile the Cloud Files WinRT shim (cernbox-cf.dll) inside the VM
	$(call wait-for-windows-ssh)
	@echo "Uploading source..."
	$(WINDOWS_SSH_CMD) "if (Test-Path C:/workspace/cernbox-sync) { Remove-Item -Recurse -Force C:/workspace/cernbox-sync }; New-Item -Force -ItemType Directory C:/workspace/cernbox-sync | Out-Null"
	@tar -czf - \
		--exclude='.git' \
		--exclude='$(WINDOWS_VM_DIR)/*.qcow2' \
		--exclude='$(WINDOWS_VM_DIR)/*.iso' \
		. | $(WINDOWS_SSH_CMD) "tar -xzf - -C 'C:/workspace/cernbox-sync/'"
	@echo "Compiling cernbox-cf.dll inside VM (MSVC + Windows SDK)..."
	$(WINDOWS_SSH_CMD) "Set-Location C:/workspace/cernbox-sync; powershell.exe -ExecutionPolicy Bypass -File cloudfiles/winrt/build.ps1"

windows-dev-cert: ## Generate a self-signed dev code-signing cert inside the VM (one-time)
	$(call wait-for-windows-ssh)
	@echo "Uploading new-dev-cert.ps1..."
	$(WINDOWS_SCP_CMD) $(WINDOWS_VM_DIR)/new-dev-cert.ps1 testuser@localhost:C:/new-dev-cert.ps1
	$(WINDOWS_SSH_CMD) "powershell.exe -ExecutionPolicy Bypass -File C:/new-dev-cert.ps1 -OutDir $(WINDOWS_VM_CERT_DIR)"
	@echo "Pulling cert back to host (public .cer only; .pfx stays in VM)..."
	@mkdir -p $(WINDOWS_VM_DIR)/cert
	$(WINDOWS_SCP_CMD) testuser@localhost:$(WINDOWS_VM_CERT_DIR)/cernbox-sync-dev.cer $(WINDOWS_VM_DIR)/cert/cernbox-sync-dev.cer

gui-msix-dev: ## Stage + Add-AppxPackage -Register inside the VM (fast MSIX iteration; no pack/sign)
	$(call wait-for-windows-ssh)
	@echo "Uploading source..."
	$(WINDOWS_SSH_CMD) "if (Test-Path C:/workspace/cernbox-sync) { Remove-Item -Recurse -Force C:/workspace/cernbox-sync }; New-Item -Force -ItemType Directory C:/workspace/cernbox-sync | Out-Null"
	@tar -czf - \
		--exclude='.git' \
		--exclude='$(WINDOWS_VM_DIR)/*.qcow2' \
		--exclude='$(WINDOWS_VM_DIR)/*.iso' \
		. | $(WINDOWS_SSH_CMD) "tar -xzf - -C 'C:/workspace/cernbox-sync/'"
	@echo "Building + registering staged MSIX inside VM (VITE_SERVER_URL=$(MSIX_SERVER_URL))..."
	$(WINDOWS_SSH_CMD) "Set-Location C:/workspace/cernbox-sync; powershell.exe -ExecutionPolicy Bypass -File dev/windows/register-msix-dev.ps1 -ServerUrl '$(MSIX_SERVER_URL)'"

gui-msix: ## Build a signed MSIX of the Tauri app inside the VM
	$(call wait-for-windows-ssh)
	@echo "Uploading source..."
	$(WINDOWS_SSH_CMD) "if (Test-Path C:/workspace/cernbox-sync) { Remove-Item -Recurse -Force C:/workspace/cernbox-sync }; New-Item -Force -ItemType Directory C:/workspace/cernbox-sync | Out-Null"
	@tar -czf - \
		--exclude='.git' \
		--exclude='$(WINDOWS_VM_DIR)/*.qcow2' \
		--exclude='$(WINDOWS_VM_DIR)/*.iso' \
		. | $(WINDOWS_SSH_CMD) "tar -xzf - -C 'C:/workspace/cernbox-sync/'"
	@echo "Running Tauri build inside VM (VITE_SERVER_URL=$(MSIX_SERVER_URL))..."
	$(WINDOWS_SSH_CMD) "Set-Location C:/workspace/cernbox-sync; powershell.exe -ExecutionPolicy Bypass -File dev/windows/run-build.ps1 -ServerUrl '$(MSIX_SERVER_URL)'"
	@echo "Packaging MSIX inside VM..."
	$(WINDOWS_SSH_CMD) "Set-Location C:/workspace/cernbox-sync; powershell.exe -ExecutionPolicy Bypass -File dev/windows/make-msix.ps1 -PfxPath $(WINDOWS_VM_CERT_DIR)/cernbox-sync-dev.pfx"
	@echo "Pulling .msix back to host..."
	@mkdir -p $(WINDOWS_VM_DIR)/out
	$(WINDOWS_SCP_CMD) -r 'testuser@localhost:C:/workspace/cernbox-sync/dev/windows/out/*.msix' $(WINDOWS_VM_DIR)/out/

test-windows-integration: ## Run Windows + cernbox backend integration tests inside the VM (requires make dev-up)
	$(call wait-for-windows-ssh)
	@echo "Uploading source..."
	$(WINDOWS_SSH_CMD) "if (Test-Path C:/workspace/cernbox-sync) { Remove-Item -Recurse -Force C:/workspace/cernbox-sync }; New-Item -Force -ItemType Directory C:/workspace/cernbox-sync | Out-Null"
	@tar -czf - \
		--exclude='.git' \
		--exclude='$(WINDOWS_VM_DIR)/*.qcow2' \
		--exclude='$(WINDOWS_VM_DIR)/*.iso' \
		. | $(WINDOWS_SSH_CMD) "tar -xzf - -C 'C:/workspace/cernbox-sync/'"
	@echo "Running Windows integration tests against $(WEBDAV_BASE)..."
	$(WINDOWS_SSH_CMD) "\$$Env:E2E_WEBDAV='$(WEBDAV_BASE)'; \$$Env:E2E_USER='$(E2E_USER)'; \$$Env:E2E_PASS='$(E2E_PASS)'; Set-Location C:/workspace/cernbox-sync; go test -v -tags 'windows integration' -timeout 120s ./cloudfiles/..."
