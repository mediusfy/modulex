#!/usr/bin/env bash
# check-api-compat.sh reports API compatibility between the latest git tag
# and the working tree, using golang.org/x/exp/cmd/apidiff.
#
# Per COMPATIBILITY.md, v0.x releases carry no backward-compatibility
# guarantee across minor versions, but patch releases must not change the
# public API. This script is advisory during v0: it always prints a report
# and exits 0, so it does not block ordinary development, but it makes
# incompatible changes visible before a release is tagged so the person
# cutting the release can choose the correct version bump. Pass -strict to
# exit non-zero when incompatible changes are found (useful once the
# project reaches v1 and patch/minor releases must stay compatible).
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

strict=0
if [[ "${1:-}" = "-strict" ]]; then
    strict=1
fi

latest_tag="$(git tag -l 'v*' --sort=-v:refname | head -n1)"
if [[ -z "$latest_tag" ]]; then
    echo "no tagged release yet; nothing to compare the API against"
    exit 0
fi

echo "comparing public API against $latest_tag"

# Pinned so behavior doesn't drift silently between CI runs; x/exp has no
# tagged releases, so this is a specific pseudo-version.
apidiff_version="golang.org/x/exp/cmd/apidiff@v0.0.0-20260709172345-9ea1abe57597"

apidiff_bindir="$(mktemp -d)"
apidiff_bin="$apidiff_bindir/apidiff"
worktree_dir=""
cleanup() {
    rm -rf "$apidiff_bindir"
    if [[ -n "$worktree_dir" ]]; then
        git worktree remove "$worktree_dir" --force 2>/dev/null || rm -rf "$worktree_dir"
    fi
}
trap cleanup EXIT

GOBIN="$apidiff_bindir" go install "$apidiff_version"

worktree_dir="$(mktemp -d)"
git worktree add --detach "$worktree_dir" "$latest_tag" --quiet

# Public packages that make up the compatibility surface. Examples, tools,
# and mocks are not part of the published API and are excluded.
packages=(. ./app ./chi ./nats ./rabbitmq ./watermill ./otel ./workerpool)

found_incompatible=0
for pkg in "${packages[@]}"; do
    old_export="$(mktemp)"
    new_export="$(mktemp)"

    if ! (cd "$worktree_dir" && "$apidiff_bin" -w "$old_export" "$pkg" 2>/dev/null); then
        echo "--- $pkg: not present at $latest_tag, skipping ---"
        rm -f "$old_export" "$new_export"
        continue
    fi
    "$apidiff_bin" -w "$new_export" "$pkg"

    echo "--- $pkg ---"
    report="$("$apidiff_bin" "$old_export" "$new_export")"
    if [[ -z "$report" ]]; then
        echo "no changes"
    else
        echo "$report"
    fi
    incompatible="$("$apidiff_bin" -incompatible "$old_export" "$new_export")"
    if [[ -n "$incompatible" ]]; then
        found_incompatible=1
    fi

    rm -f "$old_export" "$new_export"
done

if [[ "$found_incompatible" -eq 1 ]]; then
    echo
    echo "incompatible API changes detected since $latest_tag." >&2
    echo "allowed for a v0 minor release per COMPATIBILITY.md; not allowed for a patch release." >&2
    if [[ "$strict" -eq 1 ]]; then
        exit 1
    fi
fi

exit 0
