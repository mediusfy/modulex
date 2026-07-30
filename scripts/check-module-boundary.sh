#!/usr/bin/env bash
# check-module-boundary.sh runs the optional modboundary go/analysis tool
# (tools/modboundary) against examples/deployment, demonstrating that its
# feature modules (consumer, notification) only cross each other's boundary
# through the "ports" subpackage. See:
#   docs/planning/library-readiness-checklist.md ("Architectural enforcement")
#   tools/modboundary/modboundary.go
set -euo pipefail
# globstar makes "**" recurse into subdirectories in the existence check
# below; without it, bash treats "**" exactly like "*" (one path segment
# only), silently missing a migrations directory nested more than one level
# under examples/. The -dbschema pattern passed to modboundary further down
# has its own, independent "**" support (see globToRegexp in
# tools/modboundary/modboundary.go) — both need "**" for a nested migrations
# directory to actually be found by both the existence check and the
# analyzer's own scan.
shopt -s globstar

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bin="$(mktemp -d)/modboundary"
trap 'rm -rf "$(dirname "$bin")"' EXIT

(cd "$repo_root/tools/modboundary" && go build -o "$bin" ./cmd/modboundary)

cd "$repo_root"
"$bin" -root=github.com/mediusfy/modulex/examples/deployment ./examples/deployment/...

# Run the database boundary check if any SQL migration files exist under
# examples, at any depth (globstar enables recursive "**" above).
if ls examples/**/migrations/*.sql 2>/dev/null; then
	"$bin" -root=github.com/mediusfy/modulex/examples/deployment \
	       -dbschema="**/migrations/*.sql" \
	       ./examples/deployment/...
fi

echo "ok: examples/deployment respects the module and database boundary"
