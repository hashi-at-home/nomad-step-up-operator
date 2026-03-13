# Makefile for nomad-step-up-operator project

# Variables
GOCMD=go
GOBUILD=$(GOCMD) build
GOTEST=$(GOCMD) test
GOMOD=$(GOCMD) mod
GOFMT=$(GOCMD) fmt
GOVET=$(GOCMD) vet
BINARY_DIR=bin
BINARY_NAME=nomad-step-up-operator

# Build flags
LDFLAGS=-ldflags "-s -w"
BUILD_FLAGS=-trimpath

.PHONY: all build clean test fmt vet deps run help

all: clean deps fmt vet build

# Build the operator binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BINARY_DIR)
	$(GOBUILD) $(BUILD_FLAGS) $(LDFLAGS) -o $(BINARY_DIR)/$(BINARY_NAME) .

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -rf $(BINARY_DIR)
	@rm -f $(BINARY_NAME)

# Run tests
test:
	@echo "Running tests..."
	$(GOTEST) -v -cover ./...

# Format code
fmt:
	@echo "Formatting code..."
	$(GOFMT) ./...

# Run go vet
vet:
	@echo "Running go vet..."
	$(GOVET) ./...

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	$(GOMOD) download
	$(GOMOD) tidy

# Run the operator locally for testing
run: build
	@echo "Running $(BINARY_NAME)..."
	./$(BINARY_DIR)/$(BINARY_NAME)

# Display help
help:
	@echo "Available targets:"
	@echo "  all       - Clean, download deps, format, vet, and build"
	@echo "  build     - Build the operator binary"
	@echo "  clean     - Remove build artifacts"
	@echo "  test      - Run tests"
	@echo "  fmt       - Format code"
	@echo "  vet       - Run go vet"
	@echo "  deps      - Download and tidy dependencies"
	@echo "  run       - Build and run the operator locally"
	@echo "  help      - Display this help message"
