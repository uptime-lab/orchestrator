# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
BINARY_NAME=server
BINARY_UNIX=$(BINARY_NAME)_unix

# Build the application
.PHONY: build
build:
	$(GOBUILD) -o $(BINARY_NAME) -v ./cmd/server

# Build for Linux
.PHONY: build-linux
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GOBUILD) -o $(BINARY_UNIX) -v ./cmd/server

# Clean build artifacts
.PHONY: clean
clean:
	$(GOCLEAN)
	rm -f $(BINARY_NAME)
	rm -f $(BINARY_UNIX)

# Run the application
.PHONY: run
run:
	$(GOBUILD) -o $(BINARY_NAME) -v ./cmd/server
	./$(BINARY_NAME)

# Run tests
.PHONY: test
test:
	$(GOTEST) -v ./...

# Run tests in Docker
#
# TEST_IMAGE is overridable because a fixed tag is only safe on an ephemeral
# runner: on a persistent machine two concurrent runs of this target would race
# on the same tag, and `docker run` could execute the other run's image — a
# green test against code that was never built. CI passes a run-scoped tag.
TEST_IMAGE ?= orchestrator-backend-test

.PHONY: test-docker
test-docker:
	@echo "Building test Docker image..."
	docker build -f Dockerfile.test -t $(TEST_IMAGE) .
	@echo "Running unit tests in Docker..."
	docker run --rm $(TEST_IMAGE)

# Run tests with coverage
.PHONY: test-coverage
test-coverage:
	$(GOTEST) -v -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out

# Download dependencies
.PHONY: deps
deps:
	$(GOMOD) download
	$(GOMOD) tidy

# Format code
.PHONY: fmt
fmt:
	$(GOCMD) fmt ./...

# Lint code
.PHONY: lint
lint:
	golangci-lint run

# Install development tools (swagger generator)
.PHONY: tools
tools:
	@echo "Installing development tools..."
	@echo "Installing swagger generator (swag)..."
	@go install github.com/swaggo/swag/cmd/swag@v1.16.4
	@echo "Tools installed successfully!"

# Generate API documentation
# Note: Install swag CLI first using 'make tools'
.PHONY: docs
docs:
	@echo "Generating API documentation..."
	@if ! command -v swag &> /dev/null && [ ! -f $$HOME/go/bin/swag ]; then \
		echo "Error: swag CLI not found. Please run 'make tools' first to install development tools."; \
		exit 1; \
	fi
	@SWAG_CMD=$$(command -v swag || echo $$HOME/go/bin/swag); \
	$$SWAG_CMD init -g cmd/server/main.go --parseDependency --parseDepth 1 --parseInternal -o docs 2>&1 | \
		grep -v "warning: failed to get package name" | \
		grep -v "warning: failed to evaluate const" || true
	@echo "Documentation generated in ./docs directory"

# Help
.PHONY: help
help:
	@echo "Available commands:"
	@echo "  build          - Build the application"
	@echo "  build-linux    - Build for Linux"
	@echo "  clean          - Clean build artifacts"
	@echo "  run            - Run the application"
	@echo "  test           - Run tests"
	@echo "  test-coverage  - Run tests with coverage"
	@echo "  deps           - Download dependencies"
	@echo "  fmt            - Format code"
	@echo "  lint           - Lint code"
	@echo "  tools          - Install development tools (swagger generator)"
	@echo "  docs           - Generate API documentation (Swagger)"
	@echo "  help           - Show this help message"
