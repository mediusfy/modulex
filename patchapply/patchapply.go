// Package patchapply implements atomic, content-based file mutation with
// rollback journaling for a single target directory, per ADR-0032
// ("Agent-First Development Experience"), P2: "Add atomic patch application
// and rollback journaling" (Jira MOD-70). The ADR's "Safety and governance"
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
// "isolated worktree" semantics (per ADR-0032's "The agent edits in an
// isolated worktree or applies an explicit patch") is expected to set that
// up by whatever means before calling [Apply], and pass that path in as
// targetDir. See docs/planning/agent-atomic-patch-guide.md for a worked
// example and the full list of guarantees this package makes (and does not
// make).
package patchapply

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	// Broker, if non-nil, is consulted via [approval.Broker.Check] before
	// any filesystem access. Required (non-nil, with an approved Scope)
	// for any batch containing a Delete: true change; optional otherwise.
	Broker *approval.Broker
	// Scope is the exact action/resource pair checked against Broker. Only
	// consulted if Broker is non-nil.
	Scope approval.Scope
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
	// CreatedDir is the absolute path of a directory chain this entry's
	// write created (via os.MkdirAll), or "" if the parent directory
	// already existed. Rollback uses this to remove a directory chain
	// this package itself created, rather than leaving stray empty
	// directories behind after undoing a write to a brand-new path.
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
// approval" rule (steps 2-3 of the ordering guarantee).
func checkApproval(changes []FileChange, opts ApplyOptions) error {
	hasDelete := false
	for _, c := range changes {
		if c.Delete {
			hasDelete = true
			break
		}
	}
	if hasDelete && opts.Broker == nil {
		return fmt.Errorf("patchapply: %w: batch contains a Delete change but no approval.Broker was configured", ErrApprovalRequired)
	}
	if opts.Broker != nil {
		if status := opts.Broker.Check(opts.Scope); status != provenance.StatusPass {
			return fmt.Errorf("patchapply: %w: scope=%s status=%s", ErrApprovalRequired, opts.Scope, status)
		}
	}
	return nil
}

// captureCurrentState reads every change's current on-disk content,
// enforces ExpectedPriorContent, and returns the provisional journal
// entries (pre-apply state) in change order. It returns an error, before
// any write, on the first read failure or content mismatch.
func captureCurrentState(changes []FileChange, fullPaths []string) ([]JournalEntry, error) {
	entries := make([]JournalEntry, len(changes))
	for i, c := range changes {
		current, existed, err := readCurrent(fullPaths[i])
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
			createdDir, err := applyWrite(fullPaths[i], c.NewContent)
			if err != nil {
				rollbackErr := rollbackEntries(realTargetDir, applied)
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

// readCurrent returns path's current content and whether it existed. A
// nonexistent path is not an error (nil, false, nil); any other read
// failure (including "is a directory" or a permission error) is returned
// as an error, which — during Apply's step 4 — causes the whole batch to
// be rejected before any write. This means a FileChange whose Path
// currently refers to a directory (rather than a regular file or a
// nonexistent path) is always caught here, before Apply attempts to touch
// it.
func readCurrent(path string) ([]byte, bool, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return b, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, err
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

// applyWrite writes content to path atomically: it creates a temp file in
// path's directory (creating that directory first via ensureDir if
// missing), writes content, closes the temp file, and renames it onto
// path. Using the same directory for the temp file keeps the rename
// same-filesystem, which is what makes os.Rename atomic. It returns the
// directory this call created (via ensureDir), if any, so Rollback can
// remove it again.
func applyWrite(path string, content []byte) (string, error) {
	dir := filepath.Dir(path)
	createdDir, err := ensureDir(dir)
	if err != nil {
		return "", err
	}
	if err := writeViaRename(dir, path, content); err != nil {
		return createdDir, err
	}
	return createdDir, nil
}

// writeViaRename is the shared temp-file-then-rename primitive used by
// both applyWrite (Apply's forward path) and restoreEntry (Rollback's
// restore path), so both give the same atomicity guarantee.
func writeViaRename(dir, path string, content []byte) error {
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
// brought into existence, safe for Rollback to remove wholesale with
// os.RemoveAll, since nothing but this call's own writes can exist under a
// directory chain that did not exist a moment before.
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
func Rollback(targetDir string, j Journal) error {
	realTargetDir, err := filepath.EvalSymlinks(targetDir)
	if err != nil {
		return fmt.Errorf("patchapply: resolving targetDir %q: %w", targetDir, err)
	}
	if j.TargetDir != "" && j.TargetDir != realTargetDir {
		return fmt.Errorf("patchapply: journal was produced for targetDir %q, not %q", j.TargetDir, realTargetDir)
	}
	return rollbackEntries(realTargetDir, j.Entries)
}

// rollbackEntries restores every entry in entries against realTargetDir,
// in reverse application order, collecting (rather than stopping at) every
// error encountered so a caller sees the full picture of anything that
// could not be restored.
func rollbackEntries(realTargetDir string, entries []JournalEntry) error {
	var errs []error
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		full, err := resolvedPath(realTargetDir, e.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("patchapply: rolling back %q: %w", e.Path, err))
			continue
		}
		if err := restoreEntry(full, e); err != nil {
			errs = append(errs, fmt.Errorf("patchapply: rolling back %q: %w", e.Path, err))
		}
	}
	return errors.Join(errs...)
}

// restoreEntry restores one file at full to the state recorded in e.
func restoreEntry(full string, e JournalEntry) error {
	if !e.ExistedBefore {
		// The path did not exist before Apply: remove whatever is there
		// now, and remove any directory chain Apply created for it.
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return err
		}
		if e.CreatedDir != "" {
			if err := os.RemoveAll(e.CreatedDir); err != nil {
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
	// The path existed before Apply: restore its exact original bytes,
	// via the same temp-then-rename primitive Apply itself uses.
	dir := filepath.Dir(full)
	if _, err := ensureDir(dir); err != nil {
		return err
	}
	return writeViaRename(dir, full, e.OriginalContent)
}

// Verify re-reads every file j references and confirms targetDir's
// on-disk state exactly matches j's recorded pre-Apply state (Path,
// ExistedBefore, OriginalContent for every entry). It is intended to be
// called after [Rollback] to confirm restoration succeeded, satisfying the
// "rollback and repeat-verification behavior are tested" acceptance
// criterion — but it works equally well as a drift check at any other
// time, since it only ever reads.
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
		current, existed, err := readCurrent(full)
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
	}
	return errors.Join(errs...)
}
