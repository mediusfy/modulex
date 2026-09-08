# Shared helpers for the agent pointcut hooks (pre.sh, guard.sh, while.sh,
# post.sh). Sourced, not executed. The hooks are tool-agnostic: Claude Code,
# opencode, and Antigravity each wire these same scripts into their own hook
# systems (.claude/settings.json, .opencode/plugin/modulex-pointcuts.js,
# .antigravity/hooks/hooks.json), so the repository contract is enforced
# identically no matter which agent is driving. See
# docs/planning/agent-pointcuts-guide.md.

# Resolve the repo root from this file's location so the hooks work
# regardless of the caller's working directory.
AGENTHOOKS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MODULEX_REPO_ROOT="$(cd "$AGENTHOOKS_DIR/../.." && pwd)"
MODULEX_BIN="$MODULEX_REPO_ROOT/.modulex/bin/modulex"
MODULEX_CONTRACT="$MODULEX_REPO_ROOT/modulex.agent.yaml"
export MODULEX_REPO_ROOT

# Build the repo's own `modulex` CLI into the gitignored .modulex/bin cache.
# The hooks deliberately call the library's CLI (the same domain logic
# tools/mcpserver exposes over MCP) instead of re-implementing checks — the
# library is the single source of repository truth. go's build cache makes
# repeat builds near-instant.
ensure_modulex() {
  command -v go >/dev/null 2>&1 || return 1
  mkdir -p "$MODULEX_REPO_ROOT/.modulex/bin"
  (cd "$MODULEX_REPO_ROOT/tools/agentcli" && go build -o "$MODULEX_BIN" ./cmd/modulex) || return 1
  return 0
}

# Print the flat "- item" entries of a top-level list key in the contract
# (e.g. protected_paths), one per line.
contract_list() {
  local key="$1"
  awk -v key="$key:" '
    $0 == key {inblock=1; next}
    inblock && /^[^[:space:]#]/ {inblock=0}
    inblock && /^[[:space:]]*-[[:space:]]/ {sub(/^[[:space:]]*-[[:space:]]*/, ""); print}
  ' "$MODULEX_CONTRACT" 2>/dev/null
  return 0
}

# Print the contract's command matrix as "class<TAB>name<TAB>command" lines.
contract_commands() {
  awk '
    /^commands:/ {inblock=1; next}
    inblock && /^[^[:space:]#]/ {inblock=0}
    inblock && /^[[:space:]]*-[[:space:]]*name:/ {name=$0; sub(/.*name:[[:space:]]*/, "", name)}
    inblock && /^[[:space:]]+command:/ {cmd=$0; sub(/.*command:[[:space:]]*/, "", cmd)}
    inblock && /^[[:space:]]+class:/ {cls=$0; sub(/.*class:[[:space:]]*/, "", cls); printf "%s\t%s\t%s\n", cls, name, cmd}
  ' "$MODULEX_CONTRACT" 2>/dev/null
  return 0
}

# For globally-registered hooks (Kimi's ~/.kimi-code/config.toml), the
# payload carries the session cwd; return 0 (outside) when that cwd is not
# inside this repository, so the hook can stand down. Repo-local adapters
# never send a foreign cwd, so this is a no-op for them.
cwd_outside_repo() {
  local cwd
  cwd="$(MODULEX_HOOK_PAYLOAD="$1" python3 -c '
import json, os
raw = os.environ.get("MODULEX_HOOK_PAYLOAD", "")
try:
    p = json.loads(raw) if raw.strip() else {}
except json.JSONDecodeError:
    p = {}
print(p.get("cwd") or "")
' 2>/dev/null)" || return 1
  [[ -n "$cwd" && "$cwd" != "$MODULEX_REPO_ROOT" && "$cwd" != "$MODULEX_REPO_ROOT"/* ]]
}

# Hook payload: adapters that cannot pipe stdin (the opencode plugin) pass
# the JSON via MODULEX_HOOK_PAYLOAD instead; stdin wins when present.
read_payload() {
  if [[ -n "${MODULEX_HOOK_PAYLOAD:-}" ]]; then
    printf '%s' "$MODULEX_HOOK_PAYLOAD"
  elif [[ ! -t 0 ]]; then
    cat
  fi
  return 0
}
