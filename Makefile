.PHONY: deps test test-arch fmt lint build vuln help

help:
	@echo "Available targets:"
	@echo "  deps      - Download Go module dependencies"
	@echo "  test      - Run go test"
	@echo "  test-arch - Run go test -race ./..."
	@echo "  fmt       - Format Go code using gofmt"
	@echo "  lint      - Run golangci-lint if installed"
	@echo "  build     - Compile packages and examples"
	@echo "  vuln      - Run govulncheck vulnerability scan"

deps:
	go mod download

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

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
