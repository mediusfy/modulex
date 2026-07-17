#!/usr/bin/env bash
# check-changelog.sh fails if a change set touches files that should be
# documented in CHANGELOG.md without updating it.
#
# Usage: check-changelog.sh [BASE_REF] [HEAD_REF]
#   BASE_REF defaults to origin/main, HEAD_REF defaults to HEAD.
#
# Exemptions (a change set entirely within these never requires a
# CHANGELOG.md entry):
#   - .github/** (workflows, dependabot config, issue/PR templates)
#   - go.mod / go.sum on their own (a pure dependency version bump; the
#     dependabot PRs this project already merges do not carry changelog
#     entries, and this script should not start failing them)
#
# Set SKIP_CHANGELOG_CHECK=1 to bypass (e.g. for a maintainer-authored
# release-mechanics-only commit); prefer fixing the change set instead.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

if [ "${SKIP_CHANGELOG_CHECK:-}" = "1" ]; then
    echo "SKIP_CHANGELOG_CHECK=1 set; skipping"
    exit 0
fi

if [ "${GITHUB_ACTOR:-}" = "dependabot[bot]" ]; then
    echo "dependabot PR; skipping CHANGELOG.md requirement"
    exit 0
fi

base_ref="${1:-origin/main}"
head_ref="${2:-HEAD}"

if ! git rev-parse --verify --quiet "$base_ref" >/dev/null; then
    echo "base ref '$base_ref' not found locally; fetching origin/main"
    git fetch --quiet origin main
    base_ref="origin/main"
fi

merge_base="$(git merge-base "$base_ref" "$head_ref")"
changed_files="$(git diff --name-only "$merge_base" "$head_ref")"

if [ -z "$changed_files" ]; then
    echo "no changes between $base_ref and $head_ref"
    exit 0
fi

if echo "$changed_files" | grep -qx "CHANGELOG.md"; then
    echo "ok: CHANGELOG.md was updated"
    exit 0
fi

# Everything left after stripping exempt paths must be empty for this
# change set to be exempt from the CHANGELOG.md requirement.
non_exempt="$(echo "$changed_files" | grep -v -E '^\.github/' | grep -v -E '^go\.(mod|sum)$' || true)"

if [ -z "$non_exempt" ]; then
    echo "ok: change set is limited to CI config / dependency bumps, no CHANGELOG.md entry required"
    exit 0
fi

echo "error: this change touches files that should be documented in CHANGELOG.md, but CHANGELOG.md was not updated:" >&2
echo "$non_exempt" | sed 's/^/  /' >&2
echo >&2
echo "add an entry under [Unreleased] in CHANGELOG.md, or set SKIP_CHANGELOG_CHECK=1 if this really doesn't need one." >&2
exit 1
