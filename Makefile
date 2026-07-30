.PHONY: deps test test-arch fmt lint build vuln check-consumer-boundary check-module-boundary check-api-compat check-changelog release publish-godev proto-gen help

help:
	@echo "Available targets:"
	@echo "  deps                    - Download Go module dependencies"
	@echo "  test                    - Run go test"
	@echo "  test-arch               - Run go test -race ./..."
	@echo "  fmt                     - Format code using gofmt"
	@echo "  lint                    - Verify linter config and run golangci-lint"
	@echo "  build                   - Compile packages and examples"
	@echo "  vuln                    - Run govulncheck vulnerability scan"
	@echo "  check-consumer-boundary - Verify core package has no adapter deps"
	@echo "  check-module-boundary   - Run the optional modboundary analyzer on examples/deployment"
	@echo "  check-api-compat        - Report API changes since the latest git tag"
	@echo "  check-changelog         - Verify CHANGELOG.md is updated when required (PR diff vs origin/main)"
	@echo "  release                 - Tag, push, and create a GitHub release (VERSION required)"
	@echo "  publish-godev           - Manually request go.dev to re-index the module"
	@echo "  proto-gen               - Regenerate notificationpb from its .proto (requires protoc; not part of build/test/CI)"

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

check-changelog:
	./scripts/check-changelog.sh

release:
ifndef VERSION
	$(error VERSION is not set. Usage: make release VERSION=v0.2.0)
endif
	@echo "Creating release $(VERSION)..."
	git tag -a "$(VERSION)" -m "Release $(VERSION)"
	git push origin "$(VERSION)"

proto-gen:
	@command -v protoc >/dev/null 2>&1 || { echo "protoc not found on PATH; see docs/planning/grpc-adapter-guide.md"; exit 1; }
	@command -v protoc-gen-go >/dev/null 2>&1 || { echo "protoc-gen-go not found on PATH (go install google.golang.org/protobuf/cmd/protoc-gen-go@latest)"; exit 1; }
	@command -v protoc-gen-go-grpc >/dev/null 2>&1 || { echo "protoc-gen-go-grpc not found on PATH (go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest)"; exit 1; }
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		examples/deployment/notification/notificationpb/notification.proto
	@echo "Regenerated notificationpb. Review and commit the .pb.go/_grpc.pb.go changes."

publish-godev:
	@echo "Requesting proxy.golang.org to index the latest version..."
	@MODULE="github.com/mediusfy/modulex"; \
	LATEST=$$(git describe --tags --abbrev=0 2>/dev/null || echo "latest"); \
	echo "Fetching $$MODULE@$$LATEST from proxy.golang.org..."; \
	curl -sL "https://proxy.golang.org/$$MODULE/@v/$$LATEST.info" || \
		echo "Note: you can also run: GOPROXY=https://proxy.golang.org GO111MODULE=on go get $$MODULE@$$LATEST"; \
	echo ""; \
	echo "Then visit: https://pkg.go.dev/$$MODULE"
