#!/usr/bin/env bash
# WHILE pointcut — fires as work happens: after each file edit (Claude Code
# PostToolUse, opencode file.edited) or after each agent turn (Antigravity
# PostInvocation, which sends no per-file payload).
#
# Runs the contract's focused checks (verification.focused in
# modulex.agent.yaml — today gofmt-check) on exactly the touched surface, so
# formatting drift is fed back to the agent the moment it appears instead of
# surfacing at the post gate. Exit 2 reports the finding to the agent;
# anything heavier belongs to the post pointcut.
set -uo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"

payload="$(read_payload)"

command -v python3 >/dev/null 2>&1 || exit 0
cwd_outside_repo "$payload" && exit 0

# Extract the edited file from the payload, if one is present.
file="$(MODULEX_HOOK_PAYLOAD="$payload" python3 -c '
import json, os
raw = os.environ.get("MODULEX_HOOK_PAYLOAD", "")
try:
    p = json.loads(raw) if raw.strip() else {}
except json.JSONDecodeError:
    p = {}
ti = p.get("tool_input") or p.get("args") or {}
if isinstance(ti, dict):
    print(ti.get("file_path") or ti.get("filePath") or ti.get("path") or "")
')"

targets=()
if [[ -n "$file" ]]; then
  case "$file" in
    *.go) [[ -f "$file" ]] && targets+=("$file") ;;
    *) exit 0 ;;
  esac
else
  # No per-file payload (per-turn adapters): check every dirty .go file,
  # including untracked ones a Write tool just created.
  while IFS= read -r f; do
    [[ -n "$f" && -f "$MODULEX_REPO_ROOT/$f" ]] && targets+=("$MODULEX_REPO_ROOT/$f")
  done < <(cd "$MODULEX_REPO_ROOT" && {
    git diff --name-only HEAD 2>/dev/null
    git ls-files --others --exclude-standard 2>/dev/null
  } | grep '\.go$' | sort -u)
fi

[[ "${#targets[@]}" -eq 0 ]] && exit 0
command -v gofmt >/dev/null 2>&1 || exit 0

unformatted="$(gofmt -s -l "${targets[@]}" 2>/dev/null)"
if [[ -n "$unformatted" ]]; then
  {
    echo "modulex while-pointcut: focused check gofmt-check failed for:"
    echo "$unformatted"
    echo "Run \`gofmt -s -w <file>\` (or \`make fmt\`) before continuing."
  } >&2
  exit 2
fi
exit 0
