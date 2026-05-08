APP        := claude-usage-bar
PREFIX     ?= /usr/local
BINDIR     := $(PREFIX)/bin
BUILD_DIR  := bin
BIN        := $(BUILD_DIR)/$(APP)

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS    := -s -w -X main.version=$(VERSION)

.PHONY: all build run dev install uninstall setup clean fmt vet tidy test release help

all: build

build: ## Build the binary into ./bin
	@mkdir -p $(BUILD_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN) .

run: build ## Build and run in foreground (for debugging)
	$(BIN) --foreground

dev: ## Run directly with `go run` in foreground
	go run . --foreground

install: build ## Install binary to $(BINDIR) (Apple Silicon: use PREFIX=/opt/homebrew; Intel/system: may require sudo)
	install -d $(BINDIR)
	install -m 0755 $(BIN) $(BINDIR)/$(APP)
	@echo "Installed to $(BINDIR)/$(APP)"

uninstall: ## Remove installed binary and run app's uninstall (config, LaunchAgent, statusLine)
	-$(BINDIR)/$(APP) uninstall 2>/dev/null || true
	rm -f $(BINDIR)/$(APP)

setup: build ## Configure ~/.claude/settings.json statusLine
	$(BIN) setup

clean: ## Remove build artifacts
	rm -rf $(BUILD_DIR)

fmt: ## Format sources
	go fmt ./...

vet: ## Run go vet
	go vet ./...

tidy: ## Tidy go.mod / go.sum
	go mod tidy

test: ## Run tests
	go test ./...

release: clean ## Build release binaries for darwin amd64 and arm64
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(APP)-darwin-arm64 .
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(APP)-darwin-amd64 .

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
