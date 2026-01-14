.PHONY: help build clean docker-build docker-push test run

# Binary name
BINARY_NAME := talos-remote-config

# Version from git tag or default
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

# Docker configuration
REGISTRY ?= ghcr.io
IMAGE_OWNER ?= $(shell git config --get remote.origin.url | sed -n 's#.*/\([^/]*\)/[^/]*$$#\1#p' || echo "$(USER)")
IMAGE_NAME := $(REGISTRY)/$(IMAGE_OWNER)/$(BINARY_NAME)

# Build configuration
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
CGO_ENABLED ?= 0

# Build flags
LDFLAGS := -s -w -X main.version=$(VERSION)

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the binary
	@echo "Building $(BINARY_NAME) $(VERSION) for $(GOOS)/$(GOARCH)..."
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) go build \
		-ldflags "$(LDFLAGS)" \
		-o $(BINARY_NAME) \
		main.go
	@echo "Binary built: $(BINARY_NAME)"

clean: ## Clean build artifacts
	@echo "Cleaning..."
	rm -f $(BINARY_NAME)
	@echo "Clean complete"

docker-build: ## Build the Docker container
	@echo "Building Docker image $(IMAGE_NAME):$(VERSION)..."
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE_NAME):$(VERSION) -t $(IMAGE_NAME):latest .
	@echo "Docker image built: $(IMAGE_NAME):$(VERSION)"

docker-push: docker-build ## Build and push the Docker container to registry
	@echo "Pushing Docker image $(IMAGE_NAME):$(VERSION)..."
	docker push $(IMAGE_NAME):$(VERSION)
	docker push $(IMAGE_NAME):latest
	@echo "Docker image pushed"

test: ## Run tests
	@echo "Running tests..."
	go test -v ./...

run: build ## Build and run the binary
	@echo "Running $(BINARY_NAME)..."
	./$(BINARY_NAME)

# Cross-compilation targets
build-linux-amd64: ## Build for Linux AMD64
	@$(MAKE) build GOOS=linux GOARCH=amd64

build-linux-arm64: ## Build for Linux ARM64
	@$(MAKE) build GOOS=linux GOARCH=arm64

build-darwin-amd64: ## Build for macOS AMD64
	@$(MAKE) build GOOS=darwin GOARCH=amd64

build-darwin-arm64: ## Build for macOS ARM64 (Apple Silicon)
	@$(MAKE) build GOOS=darwin GOARCH=arm64

build-all: build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 ## Build for all platforms
