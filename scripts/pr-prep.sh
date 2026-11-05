#!/usr/bin/env bash
# pr-prep.sh — local pre-PR helper for any mediusfy repo.
#
# Two tiers, so running this is never a mandatory multi-minute/token-costing
# step unless you ask for it:
#
#   1. Fast gate (always runs, no secrets, no network by default): builds
#      and runs this checkout's own `modulex agent handoff` against your
#      branch — the exact same deterministic engine pr_pipeline's CI `engine`
#      job runs (secret scan with the `# nosecret: <reason>` exception
#      convention, protected-paths check, and the target repo's own gates —
#      test-arch/build/lint/test — via its modulex.agent.yaml contract, or a
#      doctor-reported "no contract" if it has none). Reusing this instead of
#      a second hand-rolled secret-pattern list means local and CI can never
#      drift apart on what counts as a leak.
#   2. Full review (`--full`, opt-in): shells out to pr_pipeline's own
#      `make review-supplied`, the real LLM review crew run against your
#      diff with no GitHub Actions / PR required. Costs LLM tokens and needs
#      PR_PIPELINE_LITELLM_KEY set (pr_pipeline/.env, loaded by its own
#      Makefile) — this script does not fetch or generate that secret.
#
# It also prints a change summary (commits + files touched since the merge
# base) to paste into the PR description — the "logs changes" half of the
# ask; this is plain `git log`/`git diff --stat`, not something the handoff
# envelope carries (Envelope.Changes is for an *agent's own* edits, not a
# base..head diff, and pr_pipeline's "engine" job leaves it empty).
#
# Usage:
#   pr-prep.sh [-C <target-repo>] [-b <base-ref>] [--full] [--allow-network]
#              [--remote <host>]
#
#   -C <path>          Repo to check (default: current directory).
#   -b, --base <ref>   Base ref to diff from (default: origin's default
#                       branch, falling back to origin/main).
#   --full              Also run pr_pipeline's full crew review locally.
#   --allow-network     Let the fast gate's checks make network calls
#                       (passed through to `modulex agent handoff`).
#   --remote <host>     Re-run this entire script over SSH on <host> instead
#                       of locally. Does NOT provision anything: <host> must
#                       already have this modulex checkout and the target
#                       repo cloned at the same path relative to its own
#                       $HOME, plus whatever toolchain the target repo's
#                       gates need (go/uv/node/...). badger-dev in
#                       particular has idle-shutdown automation and was
#                       unreachable as of the last infra pass — if this
#                       times out, that's most likely why; wake it or drop
#                       --remote and run locally instead.
#
# Exit code is the fast gate's: 0 only if every check passed (or there was
# nothing to review). --full's own exit code is not folded in — read its
# output for the verdict (APPROVE / REQUEST_CHANGES / COMMENT).
set -euo pipefail

modulex_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pr_pipeline_root="${PR_PIPELINE_ROOT:-$HOME/programming/projects/mediusfy/pr_pipeline}"

usage() {
    sed -n '2,51p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

target_root="$(pwd)"
base_ref=""
full=0
allow_network=0
remote=""

while [[ $# -gt 0 ]]; do
    case "$1" in
    -C)
        target_root="$2"
        shift 2
        ;;
    -b | --base)
        base_ref="$2"
        shift 2
        ;;
    --full)
        full=1
        shift
        ;;
    --allow-network)
        allow_network=1
        shift
        ;;
    --remote)
        remote="$2"
        shift 2
        ;;
    -h | --help)
        usage
        exit 0
        ;;
    *)
        echo "pr-prep.sh: unknown argument '$1'" >&2
        usage >&2
        exit 2
        ;;
    esac
done

if [[ ! -d "$target_root" ]]; then
    echo "pr-prep.sh: '$target_root' is not a directory" >&2
    exit 2
fi
target_root="$(cd "$target_root" && pwd)"

# --remote: re-exec the same invocation over SSH, translating target_root to
# the remote's own $HOME-relative path. Everything else (building modulex,
# running the gate) happens identically on the remote side — this block only
# ever runs once, on whichever host the user is sitting at.
if [[ -n "$remote" ]]; then
    if [[ "$target_root" != "$HOME"/* ]]; then
        echo "pr-prep.sh: --remote requires the target repo to live under \$HOME locally ('$target_root' does not), so its remote-relative path can't be derived" >&2
        exit 2
    fi
    remote_rel="${target_root#"$HOME"/}"
    remote_args=(-C "\$HOME/$remote_rel")
    [[ -n "$base_ref" ]] && remote_args+=(-b "$base_ref")
    [[ $full -eq 1 ]] && remote_args+=(--full)
    [[ $allow_network -eq 1 ]] && remote_args+=(--allow-network)

    echo "pr-prep.sh: running on $remote ..."
    if ! ssh -o ConnectTimeout=8 -o BatchMode=yes "$remote" true 2>/dev/null; then
        echo "pr-prep.sh: could not reach '$remote' over SSH." >&2
        echo "  if this is badger-dev: it has idle-shutdown automation and was unreachable as of the last infra pass -- it may just be asleep. Wake it, or drop --remote to run locally." >&2
        exit 1
    fi
    # remote_args is intentionally expanded locally (it is this script's
    # own trusted flags, not attacker input) before reaching the remote
    # shell.
    # shellcheck disable=SC2029
    exec ssh "$remote" "bash \$HOME/${modulex_root#"$HOME"/}/scripts/pr-prep.sh ${remote_args[*]}"
fi

for bin in git go make; do
    command -v "$bin" >/dev/null 2>&1 || {
        echo "pr-prep.sh: '$bin' not found on PATH" >&2
        exit 1
    }
done

cleanup_files=()
cleanup() {
    # An `if` (not `[[ ]] && ...`) so this trap's own exit status is always
    # 0 and never clobbers the status `exit N` already set before the EXIT
    # trap ran.
    if [[ ${#cleanup_files[@]} -gt 0 ]]; then
        rm -f "${cleanup_files[@]}"
    fi
}
trap cleanup EXIT

cd "$target_root"
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    echo "pr-prep.sh: '$target_root' is not a git repository" >&2
    exit 2
fi

if [[ -z "$base_ref" ]]; then
    base_ref="$(git symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null || echo origin/main)"
fi
if ! git rev-parse --verify --quiet "$base_ref" >/dev/null; then
    echo "pr-prep.sh: fetching ${base_ref#origin/} ..."
    git fetch --quiet origin "${base_ref#origin/}"
fi

if [[ -n "$(git status --porcelain)" ]]; then
    echo "pr-prep.sh: warning: working tree has uncommitted changes; this checks base..HEAD only, so commit first for an accurate run" >&2
fi

merge_base="$(git merge-base "$base_ref" HEAD)"
if [[ "$merge_base" == "$(git rev-parse HEAD)" ]]; then
    echo "pr-prep.sh: HEAD is not ahead of $base_ref -- nothing to review"
    exit 0
fi

echo "== modulex doctor: $target_root =="
(cd "$modulex_root/tools/agentcli" && go run ./cmd/modulex doctor -root "$target_root")

echo
echo "== fast gate: secret scan, protected paths, repo gates (modulex agent handoff) =="
handoff_args=(agent handoff -root "$target_root" -agent "local:$(whoami)" -base "$base_ref" -head HEAD)
[[ $allow_network -eq 1 ]] && handoff_args+=(-allow-network)

envelope_json="$(mktemp)"
envelope_stderr="$(mktemp)"
cleanup_files+=("$envelope_json" "$envelope_stderr")
set +e
(cd "$modulex_root/tools/agentcli" && go run ./cmd/modulex "${handoff_args[@]}") >"$envelope_json" 2>"$envelope_stderr"
handoff_status=$?
set -e

if command -v jq >/dev/null 2>&1 && jq -e . "$envelope_json" >/dev/null 2>&1; then
    # Valid envelope: the per-check table below already covers any failure
    # exitOnFailedChecks' stderr line would otherwise repeat out of order
    # (stderr streams live; the table only prints once the run finishes).
    jq -r '.verification[]? | "  [\(.status)] \(.name)" + (if (.message // "") != "" then ": \(.message)" elif (.reason // "") != "" then ": \(.reason)" else "" end)' "$envelope_json"
else
    # No valid JSON to show (build failure, usage error, jq missing) --
    # surface whatever modulex itself printed instead.
    cat "$envelope_stderr" >&2
    cat "$envelope_json"
fi

if [[ $handoff_status -ne 0 ]]; then
    echo
    echo "pr-prep.sh: fast gate failed -- fix the check(s) marked FAIL above before opening a PR." >&2
    echo "  secret flagged but it's not one? add an inline '# nosecret: <reason>' comment next to it." >&2
    echo "  protected path needs sign-off? from $target_root: modulex agent approve -action <name> -approved-by <you>" >&2
    echo "  a gate (lint/build/test/test-arch) failed? re-run that make target in $target_root to see the full output." >&2
    exit "$handoff_status"
fi
echo "pr-prep.sh: fast gate passed."

echo
echo "== change summary (for the PR description) =="
echo "Base:   $base_ref ($merge_base)"
echo "Branch: $(git rev-parse --abbrev-ref HEAD)"
echo
echo "Commits:"
git log --oneline "$merge_base"..HEAD
echo
echo "Files changed:"
git diff --stat "$merge_base"..HEAD

if [[ $full -eq 1 ]]; then
    if [[ ! -d "$pr_pipeline_root" ]]; then
        echo
        echo "pr-prep.sh: --full requested but pr_pipeline isn't cloned at $pr_pipeline_root (set PR_PIPELINE_ROOT=... to override)" >&2
        exit 1
    fi
    echo
    echo "== full review: pr_pipeline's review-supplied crew (costs LLM tokens; needs PR_PIPELINE_LITELLM_KEY in $pr_pipeline_root/.env) =="
    diff_file="$pr_pipeline_root/supplied.patch"
    git diff "$merge_base"..HEAD >"$diff_file"
    cleanup_files+=("$diff_file")
    (cd "$pr_pipeline_root" && make review-supplied DIFF=supplied.patch)
fi
