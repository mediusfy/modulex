.PHONY: test test-arch fmt lint build help

help:
	@echo "Available targets:"
	@echo "  test      - Run go test"
	@echo "  test-arch - Run go test -race ./..."
	@echo "  fmt       - Format Go code using gofmt"
	@echo "  lint      - Run golangci-lint if installed"
	@echo "  build     - Compile packages and examples"

test:
	go test -v ./...

test-arch:
	go test -race ./... -count=1

fmt:
	gofmt -s -w .

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed. Skipping."; \
	fi

build:
	go build ./...
