.PHONY: deps test test-arch fmt lint build vuln check-consumer-boundary check-module-boundary check-api-compat check-changelog check-nested-modules release publish-godev proto-gen help

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
	@echo "  check-nested-modules    - Verify tidy/build/test for tools/modboundary, tools/scaffold, examples/external-consumer"
	@echo "  release                 - Preflight (clean tree, on main, up to date, no duplicate tag, gates pass), then tag/push (VERSION required)"
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

check-nested-modules:
	@for dir in tools/modboundary tools/scaffold examples/external-consumer; do \
		echo "--- $$dir ---"; \
		(cd "$$dir" && go mod tidy -diff && go build ./... && go test ./...) || exit 1; \
	done
	@echo "ok: nested modules (tools/modboundary, tools/scaffold, examples/external-consumer) are tidy and build/test cleanly"

# release runs a local preflight before tagging: a dirty tree, a non-main
# branch, a local main that has diverged from origin/main, a duplicate tag,
# or a missing CHANGELOG.md section for VERSION all abort before anything
# is tagged or pushed. See docs/planning/... release-process notes and the
# v0.4.0/v0.5.1 incident history for why this exists: a tag pushed to a
# proxy-cached module version is effectively irreversible.
release:
ifndef VERSION
	$(error VERSION is not set. Usage: make release VERSION=v0.2.0)
endif
	@echo "Running release preflight checks for $(VERSION)..."
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo "error: working tree is not clean; commit or stash changes before releasing" >&2; \
		exit 1; \
	fi
	@current_branch="$$(git rev-parse --abbrev-ref HEAD)"; \
	if [ "$$current_branch" != "main" ]; then \
		echo "error: on branch '$$current_branch', not 'main'; releases must be tagged from main" >&2; \
		exit 1; \
	fi
	@git fetch --quiet origin main
	@local_head="$$(git rev-parse HEAD)"; \
	remote_head="$$(git rev-parse origin/main)"; \
	if [ "$$local_head" != "$$remote_head" ]; then \
		echo "error: local main ($$local_head) does not match origin/main ($$remote_head); push or pull before releasing" >&2; \
		exit 1; \
	fi
	@if git rev-parse --verify --quiet "refs/tags/$(VERSION)" >/dev/null 2>&1 || \
	   git ls-remote --exit-code --tags origin "refs/tags/$(VERSION)" >/dev/null 2>&1; then \
		echo "error: tag $(VERSION) already exists locally or on origin" >&2; \
		exit 1; \
	fi
	@version_no_v="$${VERSION#v}"; \
	if ! grep -q "^## \[$$version_no_v\]" CHANGELOG.md; then \
		echo "error: CHANGELOG.md has no '## [$$version_no_v]' section; add one before releasing" >&2; \
		exit 1; \
	fi
	@echo "Running required local gates (build, test-arch, lint, check-consumer-boundary, check-module-boundary, check-nested-modules)..."
	$(MAKE) build test-arch lint check-consumer-boundary check-module-boundary check-nested-modules
	@echo "Preflight passed. Creating release $(VERSION)..."
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
