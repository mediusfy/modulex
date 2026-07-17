.PHONY: deps test test-arch fmt lint build vuln check-consumer-boundary check-module-boundary check-api-compat help

help:
	@echo "Available targets:"
	@echo "  deps                    - Download Go module dependencies"
	@echo "  test                    - Run go test"
	@echo "  test-arch               - Run go test -race ./..."
	@echo "  fmt                     - Format Go code using gofmt"
	@echo "  lint                    - Verify linter config and run golangci-lint"
	@echo "  build                   - Compile packages and examples"
	@echo "  vuln                    - Run govulncheck vulnerability scan"
	@echo "  check-consumer-boundary - Verify core package has no adapter deps"
	@echo "  check-module-boundary   - Run the optional modboundary analyzer on examples/deployment"
	@echo "  check-api-compat        - Report API changes since the latest git tag"

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
		golangci-lint config verify && golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed. Skipping."; \
	fi

build:
	go build ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

check-consumer-boundary:
	./scripts/check-consumer-boundary.sh

check-module-boundary:
	./scripts/check-module-boundary.sh

check-api-compat:
	./scripts/check-api-compat.sh
