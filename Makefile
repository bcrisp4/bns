.PHONY: build test race lint vet tidy clean build-stress stress stress-server stress-clean

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

build-stress: build
	$(GO) build -trimpath -o $(BIN_DIR)/mockupstream ./cmd/mockupstream
	$(GO) build -trimpath -o $(BIN_DIR)/bns-stress   ./cmd/bns-stress

stress: build-stress
	./$(BIN_DIR)/bns-stress --scenario mixed --duration 60s --concurrency 50

stress-server: build
	@echo "Run mockupstream and bns serve manually for the remote-target topology."
	@echo "See docs/specs/2026-05-19-bns-dnspyre-stress-test-design.md §3."

stress-clean:
	rm -rf dist/stress
