BIN := yank

# VERSION overrides main.Version; left empty, the default in cmd/yank/main.go stands.
VERSION ?=
LDFLAGS := $(if $(VERSION),-X main.Version=$(VERSION))

.PHONY: all build run test race vet fmt fmt-check check clean help

all: check build

build: ## Build ./yank
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/yank

run: build ## Build and run; pass a URL with ARGS=<url>
	./$(BIN) $(ARGS)

test: ## Run all tests
	go test ./...

race: ## Run the internal tests under the race detector
	go test -race ./internal/...

vet: ## Run go vet
	go vet ./...

fmt: ## Format the source in place
	gofmt -w .

fmt-check: ## Fail if any file needs gofmt
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

check: fmt-check vet test ## gofmt, vet and tests, as the release checklist runs them

clean: ## Remove the built binary
	rm -f $(BIN)

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'
