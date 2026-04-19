CLI    := cernbox-sync
DAEMON := cernbox-syncd
GO     := go

COMPOSE := docker compose -f dev/docker-compose.yaml

.PHONY: all build cli daemon test test-gui test-e2e test-e2e-watch test-all clean help dev-up dev-down gui gui-dev

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

clean: ## Remove built binaries
	$(GO) clean
	rm -f $(CLI) $(DAEMON)
	rm -rf gui/src-tauri/target gui/src-tauri/binaries gui/dist gui/node_modules
