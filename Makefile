BINARY   := mage-mediagc
PKG      := github.com/shuaiZend/mage-mediagc
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
HOST_OS  := $(shell GOENV=off go env GOHOSTOS)
HOST_ARCH:= $(shell GOENV=off go env GOHOSTARCH)

LDFLAGS := -s -w \
	-X $(PKG)/internal/version.Version=$(VERSION) \
	-X $(PKG)/internal/version.Commit=$(COMMIT) \
	-X $(PKG)/internal/version.BuildDate=$(DATE)

.DEFAULT_GOAL := help

.PHONY: help
help: ## List every available target
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build for the GOOS/GOARCH in go env (for cross-compiling to a server)
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/$(BINARY)

.PHONY: build-local
build-local: ## Build for this machine regardless of go env (local debugging)
	CGO_ENABLED=0 GOOS=$(HOST_OS) GOARCH=$(HOST_ARCH) go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/$(BINARY)

.PHONY: test
test: ## Run the test suite, pinned to the host platform so a cross-compile GOOS cannot interfere
	GOOS=$(HOST_OS) GOARCH=$(HOST_ARCH) go test -race -covermode=atomic -coverprofile=coverage.txt ./...

.PHONY: test-short
test-short: ## Fast test run, without -race
	GOOS=$(HOST_OS) GOARCH=$(HOST_ARCH) go test ./...

.PHONY: cover
cover: test ## Test and render an HTML coverage report
	go tool cover -html=coverage.txt -o coverage.html
	@echo "coverage report: coverage.html"

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run ./...

.PHONY: fmt
fmt: ## Format the source
	gofmt -s -w .

.PHONY: vet
vet: ## Run go vet
	GOOS=$(HOST_OS) GOARCH=$(HOST_ARCH) go vet ./...

.PHONY: tidy
tidy: ## Tidy the module graph
	go mod tidy

.PHONY: check
check: fmt vet test ## Pre-commit check: format, vet, test

.PHONY: snapshot
snapshot: ## Produce goreleaser snapshot artefacts locally (publishes nothing)
	goreleaser release --snapshot --clean

.PHONY: docker
docker: ## Build the container image
	docker build -t $(BINARY):$(VERSION) .

.PHONY: clean
clean: ## Remove build artefacts
	rm -rf $(BINARY) dist bin coverage.txt coverage.html
