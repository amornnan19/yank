BIN := yank

# VERSION overrides main.Version; left empty, yank takes it from Go's build info.
VERSION ?=
LDFLAGS := $(if $(VERSION),-X main.Version=$(VERSION))

# The exact GoReleaser the release workflow runs, run through go so nothing needs installing.
GORELEASER := go run github.com/goreleaser/goreleaser/v2@v2.18.1

.PHONY: all build install run test race vet fmt fmt-check check snapshot clean help

all: check build

build: ## Build ./yank
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/yank

install: ## Install yank into $(go env GOBIN), or GOPATH/bin, so it runs from anywhere
	go install -ldflags "$(LDFLAGS)" ./cmd/yank

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

snapshot: ## Build the release archives, checksums and formula into dist/ without publishing
	$(GORELEASER) release --snapshot --clean --skip=publish

clean: ## Remove the built binary and dist/
	rm -rf $(BIN) dist

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'
