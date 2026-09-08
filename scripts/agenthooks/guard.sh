#!/usr/bin/env bash
# GUARD (pre-tool) pointcut — fires before each file edit or shell command
# (Claude Code PreToolUse, opencode tool.execute.before, Antigravity
# PreToolUse). Reads the tool-call JSON from stdin (or MODULEX_HOOK_PAYLOAD)
# and enforces modulex.agent.yaml:
#
#   - protected_paths may not be edited by an agent
#   - AGENTS.md/CLAUDE.md are generated; hand-edits would drift from the
#     contract and are redirected to `modulex agent generate`
#   - commands classed destructive/approval_required are blocked unless a
#     matching, unexpired grant exists in .modulex/approvals.json (written by
#     a human running `modulex agent approve` outside the agent loop)
#
# Exit 0 allows the call; exit 2 denies it with the reason on stderr, which
# every adapter surfaces back to the agent.
set -uo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"

# Capture the payload before the heredoc below takes over stdin.
MODULEX_HOOK_PAYLOAD="$(read_payload)"
export MODULEX_HOOK_PAYLOAD
MODULEX_PROTECTED="$(contract_list protected_paths)"
MODULEX_CMDS="$(contract_commands)"
export MODULEX_PROTECTED MODULEX_CMDS

python3 - <<'PYEOF'
import datetime
import fnmatch
import json
import os
import sys

def deny(msg):
    print(msg, file=sys.stderr)
    sys.exit(2)

raw = os.environ.get("MODULEX_HOOK_PAYLOAD", "")
try:
    payload = json.loads(raw) if raw.strip() else {}
except json.JSONDecodeError:
    payload = {}

tool = str(payload.get("tool_name") or payload.get("tool") or payload.get("name") or "").lower()
tool_input = payload.get("tool_input") or payload.get("args") or payload.get("input") or {}
if not isinstance(tool_input, dict):
    tool_input = {}

root = os.environ["MODULEX_REPO_ROOT"]
file_path = tool_input.get("file_path") or tool_input.get("filePath") or tool_input.get("path") or ""
command = tool_input.get("command") or ""

EDIT_TOOLS = {"edit", "write", "multiedit", "notebookedit", "update", "create", "patch", "write_to_file", "replace_file_content"}
SHELL_TOOLS = {"bash", "shell", "run", "exec", "run_command", "execute_command", "run_terminal_cmd"}

if tool in EDIT_TOOLS and file_path:
    rel = os.path.relpath(os.path.abspath(file_path), root)
    if not rel.startswith(".."):
        for pattern in os.environ.get("MODULEX_PROTECTED", "").splitlines():
            if pattern and (rel == pattern or fnmatch.fnmatch(rel, pattern)):
                deny(f"modulex guard: {rel} is a protected path in modulex.agent.yaml; "
                     "modifying it requires explicit human approval (agent-safety-policy.md).")
        if rel in ("AGENTS.md", "CLAUDE.md"):
            deny(f"modulex guard: {rel} is GENERATED from modulex.agent.yaml — hand-edits drift "
                 "from the contract. Edit modulex.agent.yaml, then run `modulex agent generate`.")

elif tool in SHELL_TOOLS and command:
    norm = " ".join(command.split())

    approved = set()
    try:
        with open(os.path.join(root, ".modulex", "approvals.json")) as f:
            now = datetime.datetime.now(datetime.timezone.utc)
            for grant in json.load(f):
                if grant.get("used"):
                    continue
                try:
                    expires = datetime.datetime.fromisoformat(
                        str(grant.get("expires_at", "")).replace("Z", "+00:00"))
                except ValueError:
                    continue
                if expires > now:
                    approved.add(grant.get("scope", {}).get("Action", ""))
    except (FileNotFoundError, json.JSONDecodeError):
        pass

    for line in os.environ.get("MODULEX_CMDS", "").splitlines():
        parts = line.split("\t")
        if len(parts) != 3 or parts[0] not in ("destructive", "approval_required"):
            continue
        cls, name, contract_cmd = parts
        prefix = " ".join(contract_cmd.split()[:2])
        if prefix and (norm.startswith(prefix) or f"&& {prefix}" in norm or f"; {prefix}" in norm):
            if name in approved:
                print(f"modulex guard: '{prefix}' permitted by approval grant '{name}'.")
                break
            deny(f"modulex guard: '{prefix}' is classed {cls} in modulex.agent.yaml and needs "
                 f"current-session human approval first. A human must run "
                 f"`modulex agent approve -action {name} -approved-by <name>` outside the agent "
                 "loop; then retry.")

    for marker, why in (("git push --force", "force-pushing"),
                        ("git push -f", "force-pushing"),
                        ("--no-verify", "skipping commit hooks/CI gates")):
        if marker in norm:
            deny(f"modulex guard: {why} always requires explicit human approval "
                 "per agent-safety-policy.md.")

sys.exit(0)
PYEOF
