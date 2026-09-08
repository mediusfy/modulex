#!/usr/bin/env bash
# POST pointcut — fires when the agent believes it is done (Claude Code Stop,
# opencode session.idle, Antigravity Stop).
#
# Makes "done" mean what modulex.agent.yaml says it means:
#   1. generated docs must not have drifted from the contract
#   2. touched .go files must pass the focused gofmt-check
#   3. `modulex agent verify` runs the verification plan the committed diff
#      recommends (the library itself plans the checks; MODULEX_HOOK_FULL=1
#      escalates to the full gates)
#
# Exit 2 blocks the agent from concluding and hands it the failure to fix.
# A drift-free, clean session exits 0 with a reminder to produce the
# provenance handoff envelope.
set -uo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"

payload="$(read_payload)"

# Without python3 the stop_hook_active loop-breaker below cannot run; a
# blocking gate that can never be released is worse than no gate, so stand
# down loudly instead (never report checks that didn't run as passing).
if ! command -v python3 >/dev/null 2>&1; then
  echo "modulex post-pointcut: python3 unavailable; post gates NOT run — report this, do not report checks as passing." >&2
  exit 0
fi

# Claude Code sets stop_hook_active when a Stop hook already blocked once
# this turn; honor it to avoid an infinite block loop.
if MODULEX_HOOK_PAYLOAD="$payload" python3 -c '
import json, os, sys
raw = os.environ.get("MODULEX_HOOK_PAYLOAD", "")
try:
    p = json.loads(raw) if raw.strip() else {}
except json.JSONDecodeError:
    p = {}
sys.exit(0 if p.get("stop_hook_active") else 1)
'; then
  exit 0
fi

cd "$MODULEX_REPO_ROOT"

if ! ensure_modulex; then
  echo "modulex post-pointcut: go toolchain unavailable; verification NOT run — report this, do not report checks as passing." >&2
  exit 0
fi

# 1. Contract drift: generated AGENTS.md/CLAUDE.md must match the contract.
if ! drift="$("$MODULEX_BIN" agent generate -root "$MODULEX_REPO_ROOT" -check 2>&1)"; then
  {
    echo "modulex post-pointcut: $drift"
    echo "Run \`modulex agent generate\` (or revert the contract change) before concluding."
  } >&2
  exit 2
fi

# 2. Focused gofmt over everything this session touched — tracked edits and
# untracked files a Write tool created.
dirty_go="$({ git diff --name-only HEAD 2>/dev/null; git ls-files --others --exclude-standard 2>/dev/null; } | grep '\.go$' | sort -u || true)"
if [ -n "$dirty_go" ] && command -v gofmt >/dev/null 2>&1; then
  unformatted="$(echo "$dirty_go" | xargs gofmt -s -l 2>/dev/null)"
  if [ -n "$unformatted" ]; then
    printf 'modulex post-pointcut: gofmt-check failed for:\n%s\nRun `gofmt -s -w` on these files.\n' "$unformatted" >&2
    exit 2
  fi
fi

# 3. Library-planned verification over the committed diff.
base="$(git merge-base HEAD origin/main 2>/dev/null || true)"
if [ -z "$base" ] || [ "$base" = "$(git rev-parse HEAD 2>/dev/null)" ]; then
  echo "modulex post-pointcut: no committed diff vs origin/main to verify; focused checks passed."
  [ -n "$dirty_go" ] && echo "Uncommitted .go changes exist — full gates (make build/test/lint) still apply before any push."
  exit 0
fi

verify_flags=()
[ "${MODULEX_HOOK_FULL:-0}" = "1" ] && verify_flags+=("-full")
verify_err="$MODULEX_REPO_ROOT/.modulex/last-verify.err"
verify_out="$MODULEX_REPO_ROOT/.modulex/last-verify.json"
if "$MODULEX_BIN" agent verify -root "$MODULEX_REPO_ROOT" -base "$base" "${verify_flags[@]:-}" >"$verify_out" 2>"$verify_err"; then
  echo "modulex post-pointcut: \`modulex agent verify\` passed (plan + results: .modulex/last-verify.json)."
  echo "Before reporting completion, produce the handoff envelope: \`modulex agent handoff -base $base\` (provenance.Envelope v1.0.0)."
  exit 0
fi

if grep -q "no check was actually executed" "$verify_err" 2>/dev/null; then
  echo "modulex post-pointcut: verify planned no runnable focused checks for this diff (see .modulex/last-verify.err). Full gates still apply before any push (MODULEX_HOOK_FULL=1 runs them here)."
  exit 0
fi

{
  echo "modulex post-pointcut: \`modulex agent verify -base $base\` FAILED:"
  cat "$verify_err"
  echo "Full results: .modulex/last-verify.json — fix the failing checks before concluding."
} >&2
exit 2
