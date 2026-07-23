BINARY     := unifi-kuma
MODULE     := github.com/johntdyer/unifi-kuma
CMD        := ./cmd/$(BINARY)
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS    := -s -w -X main.version=$(VERSION)
BUILD_DIR  := ./dist

DOCKER_IMAGE := ghcr.io/johntdyer/unifi-kuma
DOCKER_TAG   := $(VERSION)

GO      := go
GOTEST  := $(GO) test
GOBUILD := $(GO) build

.PHONY: all build test test-race test-cover test-docker lint vet fmt \
        clean run docker-build docker-push deps tidy help

all: build ## Default: build the binary

## ── Build ──────────────────────────────────────────────────────────────────

build: ## Build the binary to ./dist/
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) $(CMD)
	@echo "Built $(BUILD_DIR)/$(BINARY) ($(VERSION))"

build-all: ## Cross-compile for Linux amd64, arm64, arm
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64  $(GOBUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-amd64 $(CMD)
	GOOS=linux GOARCH=arm64  $(GOBUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-arm64 $(CMD)
	GOOS=linux GOARCH=arm GOARM=7 $(GOBUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-armv7 $(CMD)
	@echo "Cross-compiled binaries written to $(BUILD_DIR)/"

## ── Test ───────────────────────────────────────────────────────────────────

test: ## Run unit tests
	$(GOTEST) ./...

test-race: ## Run tests with race detector
	$(GOTEST) -race ./...

test-cover: ## Run tests and show coverage summary
	$(GOTEST) -race -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -func=coverage.out
	@rm -f coverage.out

test-cover-html: ## Generate and open HTML coverage report
	$(GOTEST) -race -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"
	@rm -f coverage.out

test-docker: ## Run tests inside Docker (matches CI environment)
	docker build --target tester --progress=plain .

## ── Code Quality ───────────────────────────────────────────────────────────

lint: ## Run golangci-lint
	golangci-lint run ./...

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Run gofmt
	gofmt -w -s .

## ── Docker ─────────────────────────────────────────────────────────────────

docker-build: ## Build the Docker image locally
	docker build \
		--build-arg VERSION=$(VERSION) \
		--tag $(DOCKER_IMAGE):$(DOCKER_TAG) \
		--tag $(DOCKER_IMAGE):latest \
		.

docker-push: docker-build ## Push the Docker image to GHCR
	docker push $(DOCKER_IMAGE):$(DOCKER_TAG)
	docker push $(DOCKER_IMAGE):latest

## ── Local Dev ──────────────────────────────────────────────────────────────

run: ## Run the binary (reads from .env if present)
	@[ -f .env ] && export $$(grep -v '^#' .env | xargs) || true; \
	$(GO) run $(CMD)

dev-deps: ## Start local Uptime Kuma for development
	docker compose -f docker-compose.test.yml up -d
	@echo "Uptime Kuma running at http://localhost:3001"

dev-stop: ## Stop local development dependencies
	docker compose -f docker-compose.test.yml down

## ── Module Management ──────────────────────────────────────────────────────

deps: ## Download Go module dependencies
	$(GO) mod download

tidy: ## Tidy go.mod and go.sum
	$(GO) mod tidy

verify: ## Verify module dependencies
	$(GO) mod verify

## ── Cleanup ────────────────────────────────────────────────────────────────

clean: ## Remove build artefacts
	@rm -rf $(BUILD_DIR) coverage.out coverage.html

## ── Help ───────────────────────────────────────────────────────────────────

help: ## Display this help message
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} \
	/^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
