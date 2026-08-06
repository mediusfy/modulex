#!/usr/bin/env bash
# install-codegraph-hooks.sh installs git hooks that run `codegraph sync`
# automatically on commit, checkout, merge, and rewrite, per AGENTS.md's
# "Keeping CodeGraph in sync" section. This keeps .codegraph/codegraph.db
# current without every agent session having to remember to run
# `codegraph sync` itself.
#
# Idempotent: each installed hook's body is guarded by a marker comment
# (markerLine below); re-running this script is a no-op for any hook that
# already has it. If a hook file exists without the marker (installed by
# something else, or hand-written), this script does not overwrite it —
# it prints the line to add manually instead, so it never clobbers a git
# hook that isn't this script's to own.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
hooks_dir="$repo_root/.git/hooks"

if [[ ! -d "$hooks_dir" ]]; then
    echo "error: $hooks_dir does not exist; is $repo_root a git repository?" >&2
    exit 1
fi

marker="# modulex:codegraph-sync"
sync_line='command -v codegraph >/dev/null 2>&1 && codegraph sync || true'

hooks=(post-commit post-checkout post-merge post-rewrite)

installed=()
skipped=()

for hook in "${hooks[@]}"; do
    hook_file="$hooks_dir/$hook"

    if [[ -f "$hook_file" ]] && grep -qF "$marker" "$hook_file"; then
        installed+=("$hook (already installed)")
        continue
    fi

    if [[ -f "$hook_file" ]]; then
        skipped+=("$hook")
        continue
    fi

    {
        echo "#!/usr/bin/env bash"
        echo "$marker"
        echo "$sync_line"
    } > "$hook_file"
    chmod +x "$hook_file"
    installed+=("$hook (installed)")
done

for entry in "${installed[@]}"; do
    echo "ok: $entry"
done

if [[ ${#skipped[@]} -gt 0 ]]; then
    echo
    echo "skipped (existing hook file has other content, not overwritten):"
    for hook in "${skipped[@]}"; do
        echo "  - $hooks_dir/$hook — add this line to it manually:"
        echo "      $sync_line"
    done
fi

echo
echo "done: CodeGraph will now sync automatically on commit/checkout/merge/rewrite."
