.PHONY: build test race lint vet tidy clean

GO        ?= go
BIN_DIR   := bin
BINARY    := $(BIN_DIR)/bns
PKG       := ./...

build:
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -o $(BINARY) ./cmd/bns

test:
	$(GO) test $(PKG)

race:
	$(GO) test -race $(PKG)

vet:
	$(GO) vet $(PKG)

lint:
	golangci-lint run

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR) dist
