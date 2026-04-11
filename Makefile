CLI    := cernbox-sync
DAEMON := cernbox-syncd
GO     := go

.PHONY: all build cli daemon test clean

all: build

build: cli daemon

cli:
	$(GO) build -o $(CLI) .

daemon:
	$(GO) build -o $(DAEMON) ./cmd/cernbox-syncd

test:
	$(GO) test ./...

clean:
	$(GO) clean
	rm -f $(CLI) $(DAEMON)
