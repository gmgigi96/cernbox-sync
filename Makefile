BINARY := syncclient
GO     := go

.PHONY: all build test clean

all: build

build:
	$(GO) build -o $(BINARY) .

test:
	$(GO) test ./...

clean:
	$(GO) clean
	rm -f $(BINARY)
