.PHONY: test fmt lint build help

help:
	@echo "Available targets:"
	@echo "  test    - Run go test"
	@echo "  fmt     - Format Go code using gofmt"
	@echo "  lint    - Run golangci-lint if installed"
	@echo "  build   - Compile packages and examples"

test:
	go test -v ./...

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
