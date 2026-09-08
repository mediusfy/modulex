#!/usr/bin/env bash
# GUARD (pre-tool) pointcut — fires before each file edit or shell command
# (Claude Code PreToolUse, opencode tool.execute.before, Antigravity
# PreToolUse). Reads the tool-call JSON from stdin (or MODULEX_HOOK_PAYLOAD)
# and enforces modulex.agent.yaml:
#
#   - protected_paths may not be modified by an agent — via edit tools or
#     via shell commands that write to them
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

# Without python3 the guard cannot parse the payload; stand down loudly
# rather than silently (never report enforcement that didn't happen).
if ! command -v python3 >/dev/null 2>&1; then
  echo "modulex guard: python3 unavailable; contract guard NOT enforced for this call" >&2
  exit 0
fi

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
import re
import sys

def deny(msg):
    print(msg, file=sys.stderr)
    sys.exit(2)

def parse_rfc3339(value):
    # Go writes expires_at as RFC3339Nano (1-9 fractional digits after
    # trailing-zero trim); fromisoformat before Python 3.11 accepts only
    # exactly 3 or 6, so normalize the fraction to 6 digits first.
    s = str(value).replace("Z", "+00:00")
    m = re.match(r"^([^.]*)\.(\d+)(.*)$", s)
    if m:
        s = f"{m.group(1)}.{(m.group(2) + '000000')[:6]}{m.group(3)}"
    return datetime.datetime.fromisoformat(s)

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
GENERATED_DOCS = ("AGENTS.md", "CLAUDE.md")

protected = [p for p in os.environ.get("MODULEX_PROTECTED", "").splitlines() if p]

if tool in EDIT_TOOLS and file_path:
    rel = os.path.relpath(os.path.abspath(file_path), root)
    if not rel.startswith(".."):
        for pattern in protected:
            if rel == pattern or fnmatch.fnmatch(rel, pattern):
                deny(f"modulex guard: {rel} is a protected path in modulex.agent.yaml; "
                     "modifying it requires explicit human approval (agent-safety-policy.md).")
        if rel in GENERATED_DOCS:
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
                    expires = parse_rfc3339(grant.get("expires_at", ""))
                except ValueError:
                    continue
                if expires > now:
                    approved.add(grant.get("scope", {}).get("Action", ""))
    except (FileNotFoundError, json.JSONDecodeError):
        pass

    # Split the command into segments so classed commands are caught in any
    # position: after newlines, ;, &&, ||, |, &, or inside a subshell. Strip
    # common wrappers so "sudo git clean -f" still matches "git clean".
    segments = []
    for seg in re.split(r"[;\n|&]+", command):
        seg = " ".join(seg.split()).lstrip("( ").strip()
        changed = True
        while changed:
            changed = False
            for wrapper in ("sudo ", "command ", "exec ", "time ", "env "):
                if seg.startswith(wrapper):
                    seg = seg[len(wrapper):]
                    changed = True
        if seg:
            segments.append(seg)

    for line in os.environ.get("MODULEX_CMDS", "").splitlines():
        parts = line.split("\t")
        if len(parts) != 3 or parts[0] not in ("destructive", "approval_required"):
            continue
        cls, name, contract_cmd = parts
        prefix = " ".join(contract_cmd.split()[:2])
        if prefix and any(seg.startswith(prefix) for seg in segments):
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

    # Protected paths must not be writable through the shell either — the
    # edit-tool deny above would otherwise teach the exact bypass. A command
    # that names a protected path AND contains a write indicator is denied;
    # read-only mentions (git diff go.mod, cat CHANGELOG.md) stay allowed.
    WRITE_MARKERS = (">", ">>", "rm ", "mv ", "cp ", "sed -i", "tee ", "truncate ",
                     "chmod ", "git checkout --", "git restore ", "touch ", "ln ")
    if any(m in norm for m in WRITE_MARKERS):
        tokens = []
        for t in norm.split():
            t = t.strip("'\"`;()")
            while t.startswith("./"):
                t = t[2:]
            if t:
                tokens.append(t)
        for pattern in list(protected) + list(GENERATED_DOCS):
            hit = next((t for t in tokens
                        if t == pattern or fnmatch.fnmatch(t, pattern)), None)
            if hit:
                extra = (" (generated from modulex.agent.yaml — use `modulex agent generate`)"
                         if pattern in GENERATED_DOCS else "")
                deny(f"modulex guard: this command contains a write indicator and references "
                     f"{hit}, a protected{' or generated' if extra else ''} path in "
                     f"modulex.agent.yaml{extra}; modifying it requires explicit human "
                     "approval (agent-safety-policy.md).")

sys.exit(0)
PYEOF
