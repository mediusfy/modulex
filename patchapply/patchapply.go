// Package patchapply implements atomic, content-based file mutation with
// rollback journaling for a single target directory. The "Safety and governance"
// section requires that the system:
//
//	preserve dirty-worktree state and unrelated edits
//
//	apply patches atomically and retain rollback information
//
// and docs/planning/agent-safety-policy.md's "Dry runs and rollback"
// section states the same requirement in policy form:
//
//	Patches should be applied atomically: either a full logical change
//	lands, or none of it does.
//
// This package is the concrete mechanism satisfying both.
//
// # Why "patchapply" and not diff-format parsing
//
// "Patch" here means "a set of intended file mutations" — write new
// content to a path, or delete a path — expressed directly as a
// [FileChange] slice, NOT literal unified-diff/git-patch text. An agent (or
// any caller) that has already decided what a file's new content should be
// does not need this package to also parse a diff format to get there; it
// needs a safe place to land that content. Scoping this package to
// content-based changes keeps it a filesystem-transaction primitive with
// zero new dependencies (stdlib only, plus this repository's own
// [github.com/mediusfy/modulex/approval] and
// [github.com/mediusfy/modulex/provenance]) — exactly what the acceptance
// criteria below need, without diff-parsing complexity or a third-party
// library. A caller that starts from unified-diff text is expected to parse
// it into a []FileChange itself (or with a separate library of its
// choosing) before calling [Apply].
//
// The package is named for what it does — apply a patch (a change set) —
// not "diff" (it never parses diff syntax) and not "fs" or "atomicwrite"
// (which would undersell the rollback-journal half of the mechanism).
//
// # The ordering guarantee
//
// [Apply] performs, in this exact order, for the ENTIRE batch before any
// filesystem write happens:
//
//  1. Validate every [FileChange.Path] stays within targetDir. Reject the
//     whole batch on any violation (absolute path, "..", or a path that
//     resolves outside targetDir via a symlink) before touching anything.
//  2. If any change has Delete: true and no [approval.Broker] was supplied
//     at all, reject the whole batch — see "Deletion requires approval"
//     below.
//  3. If a Broker/Scope was supplied (regardless of whether the batch
//     contains a delete), check it now via [approval.Broker.Check] and
//     reject the whole batch (no filesystem access at all) if it does not
//     return [provenance.StatusPass].
//  4. Read every change's CURRENT on-disk content (or note it doesn't
//     exist). This both populates the rollback journal's "original state"
//     and enforces [FileChange.ExpectedPriorContent]: if the file was
//     edited by someone else since the caller computed this change set
//     against a known baseline, that drift is caught here and the whole
//     batch is rejected before any write — this is the concrete mechanism
//     behind "preserve dirty-worktree state and unrelated edits."
//
// Only after all four steps pass for the entire batch does [Apply] write
// anything, one change at a time, via a temp-file-then-[os.Rename] pattern
// (same directory as the target file, so the rename is same-filesystem and
// atomic per file). If any individual write or delete fails partway
// through, [Apply] immediately rolls back every change already applied
// earlier in the same call, using the journal entries captured so far,
// before returning the error.
//
// This ordering is deliberate and matters: validating paths before
// consulting the approval broker means an unapproved or malformed batch
// never spends a single-use grant; checking approval before reading
// content means an unapproved destructive batch never even touches the
// filesystem to look; and checking for drift before writing anything means
// a batch that would have clobbered a human's unrelated edit never lands a
// single byte. [Apply] returns either (a) success with everything applied
// and a [Journal] describing it, or (b) an error with everything already
// rolled back to targetDir's pre-Apply state — there is no third outcome
// where a caller observes a partially-applied batch as Apply's own return.
//
// # Deletion requires approval — the precise rule
//
// A batch containing ANY [FileChange] with Delete: true is rejected
// outright, before touching the filesystem, unless [ApplyOptions.Broker]
// is non-nil AND [approval.Broker.Check] returns [provenance.StatusPass]
// for [ApplyOptions.Scope]. There is no way to delete a file through this
// package without a broker configured and an approved grant for the exact
// scope presented — a nil Broker is not "caller opted out of gating," it
// is "deletion is refused, full stop."
//
// A pure-write batch (no Delete: true entries) may proceed with a nil
// Broker: overwriting a file's content is recoverable via this package's
// own rollback journal (the original bytes are always retained), whereas a
// deletion followed by a process crash before [Rollback] is ever called is
// not recoverable by anything this package controls. That asymmetry — not
// "writes are safe and deletes are dangerous" as a blanket claim, but
// specifically "a write's own undo path lives entirely inside this
// package's journal, while a delete's undo path depends on that journal
// surviving and someone calling Rollback" — is why the line is drawn at
// Delete rather than at "any mutation." A caller applying to a real,
// possibly-shared worktree should still supply a Broker for write-only
// batches too, and should always set [FileChange.ExpectedPriorContent]; the
// "no broker required for pure writes" path is a documented default, not a
// recommendation.
//
// If a Broker/Scope is supplied for a pure-write batch, it is still
// checked (step 3 above) — supplying a Broker signals "gate this batch,"
// independent of whether it happens to contain a delete. Note that
// [approval.Broker.Check] consumes a single-use grant on a match, exactly
// once, at step 3 — before the drift check in step 4. If the batch is
// later rejected in step 4 (content drift) or fails partway through its
// writes (step 5), the grant has already been spent; the caller must
// obtain a fresh grant to retry. This is a direct consequence of the
// required step ordering (approval must be checked before the filesystem
// is touched at all) combined with [approval.Broker]'s single-use-grant
// design, not an oversight — see [approval.Broker.DryRunCheck] if a caller
// wants to preview approval without spending a grant before attempting a
// real [Apply] call.
//
// # Diagnosable without leakage
//
// Any error [Apply], [Rollback], or [Verify] returns, and [Journal]'s
// [Journal.String] method, are safe to log or display: [Journal.String]
// never includes file content at all (only paths, existed-before flags,
// and outcomes), and the few error paths that do include a content
// preview for diagnosis (an [FileChange.ExpectedPriorContent] mismatch, or
// a [Verify] drift report) run that preview through the same best-effort
// secret-pattern redaction this repository's [provenance] and [contract]
// packages use (see secrets.go — the patterns are copied locally, not
// imported, per this package's scope; provenance/provenance.go and
// contract/*.go are not modified). This is a best-effort safety net, not a
// guarantee: it catches common, recognizable secret shapes but can miss
// unrecognized formats and can also over-redact ordinary text that happens
// to match one of the patterns. See secrets.go's doc comment.
//
// # Not yet wired into anything
//
// This package is a standalone mechanism: no CLI, no MCP server, and no
// call site in this repository invokes it yet. It operates on whatever
// targetDir a caller gives it; this package does not create git worktrees,
// branches, or any other form of isolation itself — a caller that wants
// "isolated worktree" semantics (per "The agent edits in an
// isolated worktree or applies an explicit patch") is expected to set that
// up by whatever means before calling [Apply], and pass that path in as
// targetDir. See docs/planning/agent-atomic-patch-guide.md for a worked
// example and the full list of guarantees this package makes (and does not
// make).
package patchapply

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mediusfy/modulex/approval"
	"github.com/mediusfy/modulex/provenance"
)

// ErrApprovalRequired is returned (wrapped, via fmt.Errorf's %w) by [Apply]
// when a batch requires approval that was not granted — either because it
// contains a Delete with no Broker configured at all, or because a
// supplied Broker/Scope did not return provenance.StatusPass. See the
// package doc comment's "Deletion requires approval" section.
var ErrApprovalRequired = errors.New("patchapply: approval required")

// ErrPriorContentMismatch is returned (wrapped) by [Apply] when a
// [FileChange.ExpectedPriorContent] does not match the file's current
// on-disk content — the mechanism behind "preserve dirty-worktree state
// and unrelated edits."
var ErrPriorContentMismatch = errors.New("patchapply: prior content mismatch")

// ErrJournalNotRestorable is returned (wrapped) by [Rollback] when a
// [JournalEntry] claims a path existed before [Apply] touched it
// (ExistedBefore is true) but carries no OriginalContent to restore. See
// the package doc comment's "Journals are in-memory only" section: because
// [JournalEntry.OriginalContent] is deliberately excluded from JSON (it may
// contain secret-shaped file content that must never enter a persisted
// diagnostic artifact), marshaling a Journal to JSON and unmarshaling it
// back silently collapses OriginalContent to nil for every entry — Rollback
// would otherwise overwrite an existing file with empty content instead of
// its real prior bytes, a silent data-loss bug rather than a caught one. A
// genuine capture via Apply always sets OriginalContent to a non-nil slice
// for an existing file (os.ReadFile returns a non-nil, zero-length slice
// for an empty file, never nil — only readCurrent's not-exists path
// returns nil, paired with ExistedBefore false), so ExistedBefore=true with
// a nil OriginalContent can only mean the Journal passed to Rollback was
// reconstructed from something other than Apply's own return value —  most
// likely a JSON round-trip. Rollback refuses to guess in that case.
var ErrJournalNotRestorable = errors.New("patchapply: journal entry claims the path existed but has no original content to restore (was this Journal serialized and reloaded? see the package doc comment)")

// FileChange describes one intended mutation to a single file within a
// target directory: either write NewContent to Path, or delete Path. This
// is a content-based description of a mutation, not a unified-diff hunk —
// see the package doc comment's "Why patchapply and not diff-format
// parsing" section.
type FileChange struct {
	// Path is relative to the target directory passed to [Apply]. It is
	// validated per the package doc comment's ordering guarantee step 1:
	// an absolute path, a path containing "..", or a path whose nearest
	// existing ancestor directory resolves outside the target directory
	// via a symlink is rejected — the whole batch, not just this entry.
	Path string
	// NewContent is the file's new content. Ignored if Delete is true.
	NewContent []byte
	// Delete, if true, means Path should be removed rather than written.
	// See the package doc comment's "Deletion requires approval" section:
	// a batch containing any Delete: true entry requires an approved
	// [ApplyOptions.Broker]/[ApplyOptions.Scope].
	Delete bool
	// ExpectedPriorContent, if non-nil, must exactly match (via
	// [bytes.Equal]) the file's CURRENT on-disk content before Apply
	// writes anything, checked across the entire batch before any file in
	// the batch is touched. This is the mechanism for "preserves unrelated
	// dirty-worktree changes": if a human edited this file since the
	// caller computed this change set against a known baseline, the
	// mismatch is caught here and the ENTIRE Apply call fails before any
	// write happens, rather than silently overwriting their edit.
	//
	// nil means "don't check, apply unconditionally" — document to your
	// own callers that this is less safe, and that a caller applying to a
	// real, possibly-dirty worktree should always set this to the exact
	// content the change set was computed against (or to nil only when
	// the caller has some other, equally strong guarantee that the file
	// has not changed).
	//
	// Because comparison uses [bytes.Equal], a nil current file (does not
	// exist) and an ExpectedPriorContent of a non-nil empty slice
	// ([]byte{}) compare equal, identically to an existing empty file —
	// this package does not distinguish "expected absent" from "expected
	// present but empty." A caller that must distinguish those two cases
	// needs to track existence itself, separately from this field.
	ExpectedPriorContent []byte
}

// ApplyOptions configures [Apply]'s approval gating. See the package doc
// comment's "Deletion requires approval" section for the exact rule this
// enforces, and [approval.Broker]'s own doc comment for what "approved"
// means (a matching, unexpired, unused [approval.Grant]).
type ApplyOptions struct {
	// Broker, if non-nil, is consulted via [approval.Broker.Check] or
	// [approval.Broker.CheckToken] before any filesystem access. Required
	// (non-nil, with an approved grant for the scope [Apply] derives from
	// this batch — see [BatchScope]) for any batch containing a Delete:
	// true change; optional otherwise.
	Broker *approval.Broker
	// Scope.Action is checked verbatim against Broker. Scope.Resource is
	// NOT trusted as-is: Apply always overrides it with a value derived
	// deterministically from the exact set of paths in this batch (via
	// [BatchScope]), rather than whatever the caller independently
	// supplied here. This closes the gap where a grant obtained for one
	// resource string (e.g. "docs/old.md") could otherwise be presented to
	// authorize an unrelated batch of changes that merely happens to set
	// the same Scope.Resource — the grant a human actually approves must
	// cover the exact batch, not a caller's independent claim about what
	// that batch contains. A caller that wants to pre-obtain the exact
	// grant Apply will check should compute it via
	// [BatchScope](scope.Action, changes) before calling
	// [approval.Broker.Grant], not invent a Resource string by hand. Only
	// consulted if Broker is non-nil.
	Scope approval.Scope
	// Token, if non-empty, is checked via [approval.Broker.CheckToken]
	// instead of [approval.Broker.Check]. Preferred (and strongly
	// recommended) for any batch containing a Delete: true change: it
	// consumes one specific, caller-identified grant deterministically,
	// rather than relying on [approval.Broker.Check]'s own scope-matching
	// search across every stored grant to decide which one to consume.
	// Only consulted if Broker is non-nil.
	Token string
}

// batchResource returns a stable, deterministic resource identifier
// derived from the exact, sorted set of paths — a SHA-256 hex digest of
// the sorted paths joined by a NUL byte (which cannot itself appear in any
// of these paths, since they were already validated by [resolvedPath]).
// This is the mechanism behind [BatchScope]: a batch's enforced scope is
// computed FROM the batch itself, never trusted as an independent claim
// from the caller.
func batchResource(paths []string) string {
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\x00")))
	return hex.EncodeToString(sum[:])
}

// BatchScope returns the exact [approval.Scope] [Apply] and [Rollback]
// enforce for action over paths: Resource is always derived
// deterministically from the exact, sorted set of paths (see
// batchResource), never supplied independently. A caller that wants to
// obtain, ahead of time, the precise grant a human must approve for a
// given batch (so that grant can be requested, displayed, or persisted
// before the batch is attempted) should compute it with this function —
// not by inventing a Resource string by hand — so that what gets approved
// is guaranteed to match what [Apply]/[Rollback] will actually check.
//
// For [Apply], paths should be every [FileChange.Path] in the batch being
// applied (in any order; BatchScope sorts them). For [Rollback], paths
// should be every [JournalEntry.Path] whose ExistedBefore is false (the
// paths Rollback will delete to undo [Apply]'s own writes) — see
// [RollbackOptions].
func BatchScope(action string, paths []string) approval.Scope {
	return approval.Scope{Action: action, Resource: batchResource(paths)}
}

// EntryOutcome records what actually happened to one [JournalEntry] during
// [Apply].
type EntryOutcome string

const (
	// OutcomeWritten means new content was written to the path (the path
	// may or may not have existed before).
	OutcomeWritten EntryOutcome = "written"
	// OutcomeDeleted means the path existed and was removed.
	OutcomeDeleted EntryOutcome = "deleted"
	// OutcomeNoop means a Delete change targeted a path that already did
	// not exist: nothing was removed, since there was nothing there.
	OutcomeNoop EntryOutcome = "noop"
)

// JournalEntry records everything needed to exactly restore one file's
// prior state, or to explain what happened to it, in a machine- and
// human-readable shape. OriginalContent holds the file's exact prior bytes
// (nil if it did not exist before) — see the package doc comment's
// "Diagnosable without leakage" section for why this raw content is never
// included in [Journal.String] or in any default formatting, only in
// [Rollback]/[Verify]'s own restore/compare logic.
//
// # OriginalContent is deliberately excluded from JSON — Journal is
// in-memory-only
//
// OriginalContent is tagged json:"-" so that marshaling a Journal (e.g. to
// embed a summary in a log line or a provenance artifact) can never leak a
// file's raw — possibly secret-containing — prior content. This makes a
// JSON round-trip (marshal, then unmarshal back into a Journal) lossy by
// design: every entry's OriginalContent comes back nil, indistinguishable
// from "this path did not exist before Apply." [Rollback] refuses (with
// [ErrJournalNotRestorable]) to treat a nil OriginalContent as "restore to
// empty" for any entry whose ExistedBefore is true, specifically to turn
// that lossy round-trip into a loud, immediate error instead of silently
// overwriting an existing file with nothing. Treat a Journal as a
// same-process, in-memory handle to hand directly to [Rollback] or
// [Verify] later in the same program — not as a durable or portable
// artifact to serialize, persist to disk, or send to another process.
type JournalEntry struct {
	// Path is relative to the Journal's TargetDir, exactly as given in the
	// originating FileChange.
	Path string `json:"path"`
	// ExistedBefore records whether Path existed on disk before Apply
	// touched it.
	ExistedBefore bool `json:"existed_before"`
	// OriginalContent is the file's exact content before Apply touched
	// it, or nil if it did not exist. This is what Rollback restores and
	// Verify compares against — never redact or truncate this field, only
	// its rendering in error messages and Journal.String. Excluded from
	// JSON; see the type doc comment's "OriginalContent is deliberately
	// excluded from JSON" section before ever serializing a Journal.
	OriginalContent []byte `json:"-"`
	// Outcome records what Apply actually did to this path.
	Outcome EntryOutcome `json:"outcome"`
	// OriginalMode is the file's permission mode (os.FileMode.Perm(), e.g.
	// 0644 or 0755) before Apply touched it, or 0 if it did not exist.
	// Apply chmods the temp file it writes to this mode before renaming it
	// into place (falling back to a sensible default only for a genuinely
	// new file — see defaultNewFileMode), and Rollback restores it for any
	// entry whose ExistedBefore is true, so a patched or rolled-back file
	// never silently collapses to os.CreateTemp's own default mode (0600)
	// regardless of what it was before. Unlike OriginalContent, this is
	// NOT excluded from JSON: a permission mode is not secret-shaped
	// content, so it survives a Journal's JSON round-trip even though
	// OriginalContent does not.
	OriginalMode os.FileMode `json:"original_mode,omitempty"`
	// CreatedDir is the absolute path of the topmost directory this
	// entry's write newly created (via os.MkdirAll inside ensureDir), or
	// "" if the parent directory already existed. Rollback uses this as
	// the upper bound of the directory chain it may remove when undoing
	// this entry: it walks bottom-up from the entry's own directory up to
	// (and including) CreatedDir, removing each directory with os.Remove
	// ONLY if it is already empty, and validates CreatedDir resolves
	// (after following symlinks) to a path within the real target
	// directory before touching it at all. This means a human-added file
	// anywhere in that chain, or a CreatedDir that does not actually point
	// inside the target directory (e.g. in a stale or maliciously
	// constructed Journal), survives — Rollback never uses os.RemoveAll on
	// this path.
	CreatedDir string `json:"created_dir,omitempty"`
}

// Journal records, per file, enough information to exactly restore prior
// state ([Rollback]) or confirm restoration ([Verify]) after an [Apply]
// call. TargetDir is the resolved (symlink-free) absolute directory the
// journal applies to; [Rollback] and [Verify] reject a Journal produced
// for a different directory.
//
// A Journal is an in-memory handle, not a durable or portable artifact —
// see [JournalEntry]'s doc comment for why serializing one (to JSON or any
// other format) and reloading it loses the information [Rollback] needs to
// restore an existing file's content, and how [Rollback] responds to that
// (a loud [ErrJournalNotRestorable] error, not silent data loss).
type Journal struct {
	TargetDir string         `json:"target_dir"`
	Entries   []JournalEntry `json:"entries"`
}

// String renders j as a redaction-free-by-construction summary: path,
// existed-before flag, and outcome for every entry, and NOTHING from
// OriginalContent or any change's NewContent. This is always safe to log,
// print, or embed in a provenance/handoff artifact — there is no file
// content anywhere in this representation to leak in the first place. See
// the package doc comment's "Diagnosable without leakage" section.
func (j Journal) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Journal{target_dir=%s entries=[", j.TargetDir)
	for i, e := range j.Entries {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s(existed_before=%t,outcome=%s)", e.Path, e.ExistedBefore, e.Outcome)
	}
	b.WriteString("]}")
	return b.String()
}

// Apply validates and applies changes to files within targetDir as a
// single all-or-nothing batch, recording a rollback [Journal] as it goes.
// See the package doc comment's "The ordering guarantee" section for the
// exact algorithm and why the order matters.
//
// Apply returns either:
//
//   - success: every change was applied, and the returned Journal
//     describes exactly what happened to every file (safe to pass to
//     [Rollback] or [Verify] later); or
//   - failure: the returned error explains what went wrong, targetDir has
//     been restored to its exact pre-Apply state (any change already
//     applied earlier in this same call was rolled back), and the returned
//     Journal is the zero value.
//
// There is no third outcome: a caller never observes a partially-applied
// batch as Apply's own return value.
func Apply(targetDir string, changes []FileChange, opts ApplyOptions) (Journal, error) {
	realTargetDir, info, err := resolveTargetDir(targetDir)
	if err != nil {
		return Journal{}, err
	}
	if !info.IsDir() {
		return Journal{}, fmt.Errorf("patchapply: targetDir %q is not a directory", targetDir)
	}

	// Step 1: validate every path before touching anything.
	fullPaths, err := validateChangePaths(realTargetDir, changes)
	if err != nil {
		return Journal{}, err
	}

	// Steps 2-3: approval gating, before any filesystem access.
	if err := checkApproval(changes, opts); err != nil {
		return Journal{}, err
	}

	// Step 4: read current state, enforce ExpectedPriorContent, build the
	// provisional (pre-apply) journal. Rejects the whole batch, still
	// before any write, on the first mismatch or read error found.
	entries, err := captureCurrentState(changes, fullPaths)
	if err != nil {
		return Journal{}, err
	}

	// Step 5: apply, rolling back everything already applied in this call
	// if any individual step fails.
	applied, err := applyAll(realTargetDir, changes, fullPaths, entries)
	if err != nil {
		return Journal{}, err
	}

	return Journal{TargetDir: realTargetDir, Entries: applied}, nil
}

// resolveTargetDir resolves targetDir to its real (symlink-free) absolute
// form and confirms it exists, returning its os.FileInfo for the caller to
// check IsDir.
func resolveTargetDir(targetDir string) (string, os.FileInfo, error) {
	realTargetDir, err := filepath.EvalSymlinks(targetDir)
	if err != nil {
		return "", nil, fmt.Errorf("patchapply: resolving targetDir %q: %w", targetDir, err)
	}
	infoResult, err := os.Stat(realTargetDir)
	if err != nil {
		return "", nil, fmt.Errorf("patchapply: statting targetDir %q: %w", targetDir, err)
	}
	return realTargetDir, infoResult, nil
}

// validateChangePaths validates every change's Path per resolvedPath and
// rejects duplicate targets within the same batch (two changes racing to
// decide one file's final state within a single Apply call is always a
// caller error, not something this package should silently pick a winner
// for). It returns the resolved absolute path for each change, in order.
func validateChangePaths(realTargetDir string, changes []FileChange) ([]string, error) {
	fullPaths := make([]string, len(changes))
	seen := make(map[string]int, len(changes))
	for i, c := range changes {
		full, err := resolvedPath(realTargetDir, c.Path)
		if err != nil {
			return nil, fmt.Errorf("patchapply: change %d (%q): %w", i, c.Path, err)
		}
		if prev, dup := seen[full]; dup {
			return nil, fmt.Errorf("patchapply: change %d and change %d both target %q", prev, i, c.Path)
		}
		seen[full] = i
		fullPaths[i] = full
	}
	return fullPaths, nil
}

// checkApproval enforces the package doc comment's "Deletion requires
// approval" rule (steps 2-3 of the ordering guarantee). The scope actually
// checked against opts.Broker is derived from the batch itself (via
// [BatchScope]) rather than trusted directly from opts.Scope — see
// [ApplyOptions.Scope]'s doc comment for why.
func checkApproval(changes []FileChange, opts ApplyOptions) error {
	hasDelete := false
	paths := make([]string, len(changes))
	for i, c := range changes {
		paths[i] = c.Path
		if c.Delete {
			hasDelete = true
		}
	}
	if hasDelete && opts.Broker == nil {
		return fmt.Errorf("patchapply: %w: batch contains a Delete change but no approval.Broker was configured", ErrApprovalRequired)
	}
	if opts.Broker != nil {
		scope := BatchScope(opts.Scope.Action, paths)
		status := checkBrokerScope(opts.Broker, opts.Token, scope)
		if status != provenance.StatusPass {
			return fmt.Errorf("patchapply: %w: scope=%s status=%s", ErrApprovalRequired, scope, status)
		}
	}
	return nil
}

// checkBrokerScope checks scope against broker, preferring
// [approval.Broker.CheckToken] (a deterministic, caller-identified grant)
// over [approval.Broker.Check] (a scope-matching search across every
// stored grant) whenever token is non-empty.
func checkBrokerScope(broker *approval.Broker, token string, scope approval.Scope) provenance.Status {
	if token != "" {
		return broker.CheckToken(token, scope)
	}
	return broker.Check(scope)
}

// captureCurrentState reads every change's current on-disk content and
// mode, enforces ExpectedPriorContent, and returns the provisional journal
// entries (pre-apply state) in change order. It returns an error, before
// any write, on the first read failure or content mismatch.
func captureCurrentState(changes []FileChange, fullPaths []string) ([]JournalEntry, error) {
	entries := make([]JournalEntry, len(changes))
	for i, c := range changes {
		current, existed, mode, err := readCurrent(fullPaths[i])
		if err != nil {
			return nil, fmt.Errorf("patchapply: change %d (%q): reading current content: %w", i, c.Path, err)
		}
		if c.ExpectedPriorContent != nil && !bytes.Equal(current, c.ExpectedPriorContent) {
			return nil, fmt.Errorf("patchapply: change %d (%q): %w: expected %s, found %s",
				i, c.Path, ErrPriorContentMismatch, previewContent(c.ExpectedPriorContent), previewContent(current))
		}
		entries[i] = JournalEntry{
			Path:            c.Path,
			ExistedBefore:   existed,
			OriginalContent: current,
			OriginalMode:    mode,
		}
	}
	return entries, nil
}

// applyAll performs the actual writes/deletes in change order, rolling
// back everything already applied in this call if any individual step
// fails, per the package doc comment's "The ordering guarantee" section.
func applyAll(realTargetDir string, changes []FileChange, fullPaths []string, entries []JournalEntry) ([]JournalEntry, error) {
	applied := make([]JournalEntry, 0, len(changes))
	for i, c := range changes {
		entry := entries[i]
		if c.Delete {
			outcome, err := applyDelete(fullPaths[i], entry.ExistedBefore)
			if err != nil {
				rollbackErr := rollbackEntries(realTargetDir, applied)
				return nil, combineApplyRollbackErr(
					fmt.Errorf("patchapply: change %d (%q): deleting: %w", i, c.Path, err), rollbackErr)
			}
			entry.Outcome = outcome
		} else {
			mode := entry.OriginalMode
			if !entry.ExistedBefore || mode == 0 {
				// A genuinely new file (or, defensively, an existing file
				// whose mode somehow came back as 0) gets a sensible
				// default rather than silently landing at os.CreateTemp's
				// own default (0600) regardless of what it should be.
				mode = defaultNewFileMode
			}
			createdDir, err := applyWrite(fullPaths[i], c.NewContent, mode)
			if err != nil {
				// applyWrite can fail after it already created a new
				// directory chain (ensureDir succeeded, the write/rename
				// that followed did not). createdDir names that chain even
				// on this error path, so it must be folded into the entry
				// handed to rollbackEntries alongside the already-applied
				// ones — otherwise this call's own directory creation is
				// never rolled back, leaving it on disk despite Apply
				// reporting a full rollback to the pre-Apply state.
				entry.CreatedDir = createdDir
				rollbackErr := rollbackEntries(realTargetDir, append(applied, entry))
				return nil, combineApplyRollbackErr(
					fmt.Errorf("patchapply: change %d (%q): writing: %w", i, c.Path, err), rollbackErr)
			}
			entry.Outcome = OutcomeWritten
			entry.CreatedDir = createdDir
		}
		applied = append(applied, entry)
	}
	return applied, nil
}

// combineApplyRollbackErr folds an optional rollback error into applyErr's
// message so a caller can see both what failed and whether the ensuing
// rollback itself hit any trouble, without losing either error.
func combineApplyRollbackErr(applyErr, rollbackErr error) error {
	if rollbackErr != nil {
		return fmt.Errorf("%w (rollback of already-applied changes also encountered errors: %v)", applyErr, rollbackErr)
	}
	return applyErr
}

// readCurrent returns path's current content, whether it existed, and (if
// it existed) its permission mode. A nonexistent path is not an error
// (nil, false, 0, nil); any other read failure (including "is a
// directory" or a permission error) is returned as an error, which —
// during Apply's step 4 — causes the whole batch to be rejected before
// any write. This means a FileChange whose Path currently refers to a
// directory (rather than a regular file or a nonexistent path) is always
// caught here, before Apply attempts to touch it.
func readCurrent(path string) ([]byte, bool, os.FileMode, error) {
	info, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, false, 0, nil
		}
		return nil, false, 0, statErr
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false, 0, err
	}
	return b, true, info.Mode().Perm(), nil
}

// applyDelete removes path if it exists, reporting OutcomeNoop (not an
// error) if it already did not exist — a Delete change is idempotent with
// respect to absence.
func applyDelete(path string, existed bool) (EntryOutcome, error) {
	if !existed {
		return OutcomeNoop, nil
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return OutcomeDeleted, nil
}

// defaultNewFileMode is the permission mode applied to a file that did not
// exist before Apply wrote it. It is deliberately NOT os.CreateTemp's own
// default (0600): a caller writing an ordinary new file within a target
// directory expects the repository's usual umask-shaped default (0644),
// not a mode that makes the file unreadable by anyone else on the system.
// A file that DID exist before Apply touched it keeps its own original
// mode instead (see applyAll and restoreEntry) — this default only ever
// applies to a genuinely new path.
const defaultNewFileMode = os.FileMode(0o644)

// applyWrite writes content to path atomically: it creates a temp file in
// path's directory (creating that directory first via ensureDir if
// missing), writes content, chmods the temp file to mode, closes it, and
// renames it onto path. Using the same directory for the temp file keeps
// the rename same-filesystem, which is what makes os.Rename atomic. It
// returns the directory this call created (via ensureDir), if any, so
// Rollback can remove it again.
//
// mode is chmod'd onto the temp file BEFORE the rename, not after: a
// window between "file lands at its final path" and "file has its
// intended mode" would mean something else could observe (or, for an
// overly permissive mode, even use) the file in its wrong-mode state.
// os.CreateTemp itself always creates the temp file at mode 0600
// regardless of this parameter — chmod is what corrects that to whatever
// the caller (applyAll) decided the final mode should be: the original
// file's mode for an existing path, or defaultNewFileMode for a brand-new
// one. Without this, every patched (or rolled-back) file would silently
// collapse to 0600 no matter what it was before, which is exactly the bug
// this parameter fixes.
func applyWrite(path string, content []byte, mode os.FileMode) (string, error) {
	dir := filepath.Dir(path)
	createdDir, err := ensureDir(dir)
	if err != nil {
		return "", err
	}
	if err := writeViaRename(dir, path, content, mode); err != nil {
		return createdDir, err
	}
	return createdDir, nil
}

// writeViaRename is the shared temp-file-then-rename primitive used by
// both applyWrite (Apply's forward path) and restoreEntry (Rollback's
// restore path), so both give the same atomicity guarantee AND the same
// mode-preservation guarantee (see applyWrite's doc comment).
func writeViaRename(dir, path string, content []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(dir, ".patchapply-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, writeErr := tmp.Write(content)
	closeErr := tmp.Close()
	if writeErr != nil {
		_ = os.Remove(tmpName)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpName)
		return closeErr
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// ensureDir ensures dir exists, creating it (and any missing parents) via
// os.MkdirAll if necessary. If dir already exists (and is a directory), it
// returns ("", nil): nothing was created, so there is nothing for Rollback
// to remove. If dir did not exist, it returns the topmost ancestor
// directory that had to be created — the precise subtree this call
// brought into existence. Rollback (via removeCreatedDir) walks this
// subtree bottom-up and removes only directories it finds empty at
// rollback time — see removeCreatedDir's doc comment for why it never
// uses os.RemoveAll even though, at the moment ensureDir creates this
// chain, nothing but this call's own writes exists under it yet.
func ensureDir(dir string) (string, error) {
	info, err := os.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("patchapply: %q exists and is not a directory", dir)
		}
		return "", nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}

	top := dir
	for {
		parent := filepath.Dir(top)
		if parent == top {
			break
		}
		if _, statErr := os.Stat(parent); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		}
		top = parent
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return top, nil
}

// Rollback restores targetDir to the state recorded in j — the state
// immediately before the [Apply] call that produced j. It is exposed as a
// standalone function (distinct from Apply's own internal partial-failure
// rollback) for a caller who wants to undo a previously SUCCESSFUL Apply
// call later, e.g. after review or a failed downstream step. j must be the
// Journal Apply actually returned (or a value derived from it within the
// same process) — see [Journal]'s doc comment for why serializing and
// reloading a Journal is unsafe, and the [ErrJournalNotRestorable] error
// Rollback returns instead of guessing when it detects that has happened.
//
// Rollback is idempotent-ish in the sense that restoring a path that
// already matches j's recorded original state is a harmless no-op write or
// no-op remove; it does not, however, re-check
// [FileChange.ExpectedPriorContent] or otherwise guard against a third
// party having modified a file again after Apply and before Rollback —
// Rollback unconditionally overwrites/removes to match j. Use [Verify]
// afterward to confirm the result.
// RollbackOptions configures [Rollback]'s approval gating for the
// DELETIONS it performs on j's behalf — specifically, removing a path and
// any now-empty directory chain above it for every [JournalEntry] with
// ExistedBefore false (a path [Apply] created, which Rollback must delete
// to undo). Restoring an entry's ORIGINAL content when ExistedBefore is
// true is a WRITE, not a deletion, and is never gated by RollbackOptions —
// this exactly mirrors [ApplyOptions]'s own "deletion requires approval,
// writes do not" rule, now applied to Rollback's own deletions, which
// previously bypassed approval checking entirely even though they are
// just as unrecoverable as an Apply-side Delete.
type RollbackOptions struct {
	// Broker, if non-nil, is consulted before Rollback removes anything.
	// Required (non-nil, with an approved grant for the scope Rollback
	// derives from the paths it must delete — see [BatchScope]) whenever j
	// contains any entry with ExistedBefore false. A nil Broker in that
	// situation is not "caller opted out of gating": Rollback rejects the
	// entire call and deletes nothing, exactly like [ApplyOptions.Broker]
	// for [Apply].
	Broker *approval.Broker
	// Scope.Action is checked verbatim; Scope.Resource is ignored and
	// replaced with a value Rollback derives deterministically from the
	// exact set of paths it is about to remove (via [BatchScope]) — see
	// [ApplyOptions.Scope]'s doc comment for why a caller-supplied
	// Resource is never trusted directly. Only consulted if Broker is
	// non-nil.
	Scope approval.Scope
	// Token, if non-empty, is checked via [approval.Broker.CheckToken]
	// instead of [approval.Broker.Check] — preferred for the same reason
	// described in [ApplyOptions.Token].
	Token string
}

func Rollback(targetDir string, j Journal, opts RollbackOptions) error {
	realTargetDir, err := filepath.EvalSymlinks(targetDir)
	if err != nil {
		return fmt.Errorf("patchapply: resolving targetDir %q: %w", targetDir, err)
	}
	if j.TargetDir != "" && j.TargetDir != realTargetDir {
		return fmt.Errorf("patchapply: journal was produced for targetDir %q, not %q", j.TargetDir, realTargetDir)
	}

	// Step 1 (mirroring Apply's own ordering guarantee): validate every
	// entry's path before touching anything or consulting the approval
	// broker, so a malicious or corrupted Journal is rejected outright —
	// see TestRollback_RejectsMaliciousJournalPathTraversal — rather than
	// having some entries acted on before a later one is caught.
	for _, e := range j.Entries {
		if _, err := resolvedPath(realTargetDir, e.Path); err != nil {
			return fmt.Errorf("patchapply: rolling back %q: %w", e.Path, err)
		}
	}

	// Step 2: approval gating for any deletions this rollback must
	// perform. See checkRollbackApproval's doc comment for exactly which
	// entries count as a deletion.
	if err := checkRollbackApproval(j.Entries, opts); err != nil {
		return err
	}

	return rollbackEntries(realTargetDir, j.Entries)
}

// checkRollbackApproval enforces [RollbackOptions]'s rule: every entry
// with ExistedBefore false names a path Rollback is about to DELETE (undo
// Apply's own write of a brand-new file), and that deletion requires the
// same kind of approval [checkApproval] requires for an Apply-side
// Delete. An entry with ExistedBefore true only ever causes a WRITE
// (restoring original content) and is never counted here.
func checkRollbackApproval(entries []JournalEntry, opts RollbackOptions) error {
	var deletePaths []string
	for _, e := range entries {
		if !e.ExistedBefore {
			deletePaths = append(deletePaths, e.Path)
		}
	}
	if len(deletePaths) == 0 {
		return nil
	}
	if opts.Broker == nil {
		return fmt.Errorf("patchapply: %w: rollback must delete %d path(s) but no approval.Broker was configured",
			ErrApprovalRequired, len(deletePaths))
	}
	scope := BatchScope(opts.Scope.Action, deletePaths)
	status := checkBrokerScope(opts.Broker, opts.Token, scope)
	if status != provenance.StatusPass {
		return fmt.Errorf("patchapply: %w: scope=%s status=%s", ErrApprovalRequired, scope, status)
	}
	return nil
}

// rollbackEntries restores every entry in entries against realTargetDir,
// in reverse application order, collecting (rather than stopping at) every
// error encountered so a caller sees the full picture of anything that
// could not be restored. This is also Apply's own internal partial-failure
// rollback path (called directly, bypassing Rollback's approval gate):
// undoing a batch's own already-applied changes because THAT SAME batch
// failed partway through is not a new destructive action requiring a
// fresh human approval — the approval (if any) already required and
// checked at the start of that same Apply call covers it.
func rollbackEntries(realTargetDir string, entries []JournalEntry) error {
	var errs []error
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		full, err := resolvedPath(realTargetDir, e.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("patchapply: rolling back %q: %w", e.Path, err))
			continue
		}
		if err := restoreEntry(realTargetDir, full, e); err != nil {
			errs = append(errs, fmt.Errorf("patchapply: rolling back %q: %w", e.Path, err))
		}
	}
	return errors.Join(errs...)
}

// restoreEntry restores one file at full (realTargetDir's resolved join of
// e.Path) to the state recorded in e.
func restoreEntry(realTargetDir, full string, e JournalEntry) error {
	if !e.ExistedBefore {
		// The path did not exist before Apply: remove whatever is there
		// now, and remove any directory chain Apply created for it (see
		// removeCreatedDir — never os.RemoveAll).
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return err
		}
		if e.CreatedDir != "" {
			if err := removeCreatedDir(realTargetDir, filepath.Dir(full), e.CreatedDir); err != nil {
				return err
			}
		}
		return nil
	}
	if e.OriginalContent == nil {
		// A genuine Apply-produced entry with ExistedBefore true always has
		// non-nil OriginalContent (even []byte{} for a previously-empty
		// file) — see ErrJournalNotRestorable's doc comment. Refuse to
		// silently overwrite full with empty content; that would be a
		// worse outcome than doing nothing.
		return fmt.Errorf("%w: %q", ErrJournalNotRestorable, e.Path)
	}
	// The path existed before Apply: restore its exact original bytes AND
	// mode, via the same temp-then-rename primitive Apply itself uses.
	mode := e.OriginalMode
	if mode == 0 {
		// Should not happen for a genuine Apply-produced entry (an
		// existing file always has a nonzero stat mode), but fall back to
		// the same sensible default Apply itself uses for a new file
		// rather than chmod-ing to 0.
		mode = defaultNewFileMode
	}
	dir := filepath.Dir(full)
	if _, err := ensureDir(dir); err != nil {
		return err
	}
	return writeViaRename(dir, full, e.OriginalContent, mode)
}

// removeCreatedDir removes the directory chain Apply created for one
// entry (recorded as CreatedDir), starting from fileDir (the entry's own,
// deepest directory) and walking upward to CreatedDir, inclusive. It is
// deliberately conservative wherever the situation is ambiguous:
//
//   - CreatedDir is resolved via filepath.EvalSymlinks and confirmed to
//     land within realTargetDir before anything is removed. A Journal's
//     CreatedDir survives a JSON round-trip (unlike OriginalContent), so a
//     stale or maliciously constructed Journal naming an unrelated
//     absolute path (e.g. a home directory) here must never be acted on —
//     this is rejected outright rather than silently skipped, so the
//     caller learns about it instead of Rollback quietly doing nothing.
//   - Each directory from fileDir up to CreatedDir is removed with
//     os.Remove ONLY if it is already empty — NEVER os.RemoveAll. The
//     first non-empty directory encountered (e.g. because a human added a
//     file into it after Apply ran, or a sibling JournalEntry under the
//     same chain has not been rolled back yet) stops the walk entirely:
//     nothing further up the chain is touched either, since a directory
//     that still contains something can never itself be "created by this
//     call and now empty."
func removeCreatedDir(realTargetDir, fileDir, createdDir string) error {
	resolvedCreated, err := filepath.EvalSymlinks(createdDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Already removed, e.g. while cleaning up a sibling entry
			// under the same chain earlier in this same Rollback call.
			return nil
		}
		return fmt.Errorf("patchapply: resolving created dir %q: %w", createdDir, err)
	}
	if !isWithin(realTargetDir, resolvedCreated) {
		return fmt.Errorf("%w: created_dir %q resolves outside targetDir, refusing to remove it",
			ErrPathTraversal, createdDir)
	}

	dir := fileDir
	for isWithin(resolvedCreated, dir) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				// Already gone; nothing to do at this level, but keep
				// walking upward in case an ancestor still needs removing.
			} else {
				return fmt.Errorf("patchapply: reading created dir %q: %w", dir, err)
			}
		} else if len(entries) > 0 {
			// Not empty: something (a human's file, or a sibling entry's
			// own path not yet rolled back) still lives here. Stop —
			// never remove this directory, and never remove anything
			// further up the chain either.
			return nil
		} else if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("patchapply: removing created dir %q: %w", dir, err)
		}
		if dir == resolvedCreated {
			return nil
		}
		dir = filepath.Dir(dir)
	}
	return nil
}

// Verify re-reads every file j references and confirms targetDir's
// on-disk state exactly matches j's recorded pre-Apply state (Path,
// ExistedBefore, OriginalContent, and — for an entry that existed before —
// OriginalMode). It is intended to be called after [Rollback] to confirm
// restoration succeeded, satisfying the "rollback and repeat-verification
// behavior are tested" acceptance criterion — but it works equally well as
// a drift check at any other time, since it only ever reads.
//
// A non-nil error names every file that does not match, joined via
// [errors.Join]; nil means targetDir exactly matches j's recorded state.
func Verify(targetDir string, j Journal) error {
	realTargetDir, err := filepath.EvalSymlinks(targetDir)
	if err != nil {
		return fmt.Errorf("patchapply: resolving targetDir %q: %w", targetDir, err)
	}
	if j.TargetDir != "" && j.TargetDir != realTargetDir {
		return fmt.Errorf("patchapply: journal was produced for targetDir %q, not %q", j.TargetDir, realTargetDir)
	}

	var errs []error
	for _, e := range j.Entries {
		full, err := resolvedPath(realTargetDir, e.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("patchapply: verifying %q: %w", e.Path, err))
			continue
		}
		current, existed, mode, err := readCurrent(full)
		if err != nil {
			errs = append(errs, fmt.Errorf("patchapply: verifying %q: reading: %w", e.Path, err))
			continue
		}
		if existed != e.ExistedBefore {
			errs = append(errs, fmt.Errorf("patchapply: verifying %q: expected existed=%t, found existed=%t",
				e.Path, e.ExistedBefore, existed))
			continue
		}
		if !bytes.Equal(current, e.OriginalContent) {
			errs = append(errs, fmt.Errorf("patchapply: verifying %q: content mismatch: expected %s, found %s",
				e.Path, previewContent(e.OriginalContent), previewContent(current)))
		}
		if existed && e.OriginalMode != 0 && mode != e.OriginalMode {
			errs = append(errs, fmt.Errorf("patchapply: verifying %q: mode mismatch: expected %#o, found %#o",
				e.Path, e.OriginalMode, mode))
		}
	}
	return errors.Join(errs...)
}
