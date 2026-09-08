#!/usr/bin/env bash
# PRE pointcut — fires once when an agent session starts (Claude Code
# SessionStart, opencode plugin init, Antigravity PreInvocation).
#
# Everything printed to stdout lands in the agent's context, so this is the
# library introducing itself to the agent: repository health (`modulex
# doctor`), contract/generated-doc drift (`modulex agent generate -check`),
# and a reminder of the active pointcuts. Never blocks — it informs.
set -uo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"

# Globally-registered hosts (Kimi) fire this everywhere; only speak up when
# the session is actually in this repository.
if command -v python3 >/dev/null 2>&1 && cwd_outside_repo "$(read_payload)"; then
  exit 0
fi

echo "== modulex pre-pointcut: repository self-report =="

if ensure_modulex; then
  "$MODULEX_BIN" doctor -root "$MODULEX_REPO_ROOT" || true
  if drift="$("$MODULEX_BIN" agent generate -root "$MODULEX_REPO_ROOT" -check 2>&1)"; then
    echo "docs:            AGENTS.md/CLAUDE.md match modulex.agent.yaml"
  else
    echo "docs:            DRIFTED — $drift"
  fi
else
  echo "modulex CLI unavailable (go toolchain missing or build failed);"
  echo "falling back to the contract file directly: modulex.agent.yaml"
fi

cat <<'EOF'

Active pointcuts in this session (scripts/agenthooks/):
  pre    this report — doctor + contract drift at session start
  guard  before each edit/command — protected paths and the contract's
         destructive/approval_required command classes are blocked until a
         human grants approval via `modulex agent approve` (outside this loop)
  while  after each edit — the contract's focused checks run immediately
  post   before the session concludes — `modulex agent verify` runs the
         verification plan for the diff; produce the provenance handoff with
         `modulex agent handoff -base <ref>` before reporting completion
EOF
exit 0
