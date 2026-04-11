CLI    := cernbox-sync
DAEMON := cernbox-syncd
GO     := go

.PHONY: all build cli daemon test clean help

all: build

help: ## Show this help message
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m\n\nTargets:\n"} \
	/^[a-zA-Z_-]+:.*##/ { printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: cli daemon ## Build both binaries

cli: ## Build the CLI client (cernbox-sync)
	$(GO) build -o $(CLI) .

daemon: ## Build the background daemon (cernbox-syncd)
	$(GO) build -o $(DAEMON) ./cmd/cernbox-syncd

test: ## Run all tests
	$(GO) test ./...

clean: ## Remove built binaries
	$(GO) clean
	rm -f $(CLI) $(DAEMON)
