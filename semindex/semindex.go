// Package semindex diagnoses whether a semantic code index (CodeGraph,
// TokenSave, or any similar tool that builds an offline index of a
// repository) actually belongs to the git worktree an agent is currently
// working in, per ADR-0032 ("Agent-First Development Experience"), P2:
// "Add CodeGraph/TokenSave index-root validation and diagnostics" (Jira
// MOD-71). ADR-0032's "Safety and governance" section requires that
// agents:
//
//	verify that semantic indexes such as CodeGraph or TokenSave point at
//	the active worktree before their results are trusted.
//
// # The problem this solves
//
// A semantic index is typically built once and then queried repeatedly,
// often from a long-lived MCP server process that outlives any single
// checkout. If an agent later runs the same tool from a *different* git
// worktree of the same logical repository (a second clone, a worktree
// checked out for another branch, a directory that was renamed or moved),
// the index may silently keep answering from stale, wrong-branch, or
// wrong-repository state. The tool itself may have no way to know its
// answers no longer match what the agent is looking at. This has been
// observed in practice: an index built against one checkout answered
// queries run from a sibling checkout of a different branch, with nothing
// in the response making that obvious short of the tool proactively
// reporting its own build root.
//
// # The marker-file convention, and its limits
//
// This package cannot inspect an arbitrary third-party index's on-disk
// format — CodeGraph's and TokenSave's index stores are undocumented,
// tool-owned binary/SQLite state, and depending on their schemas (or
// adding a SQLite driver just to peek at them) would violate this
// package's "does not enter Modulex core dependencies" constraint and
// couple it to formats that owe this package no compatibility guarantee.
//
// Instead, semindex defines a trivial, dependency-free convention any
// index tool may *choose* to adopt: a plain-text marker file named
// [MarkerFileName], written inside the index's own directory, whose first
// non-empty line is the absolute path the index was built against. No
// JSON, no YAML, no schema — a single line, so there is no parsing
// ambiguity for a tool author to get wrong. [MarkerFileReader] and
// [DefaultMarkerReader] read this convention; [WriteMarkerFile] lets a
// tool (or a test) write it.
//
// Today, nothing in the CodeGraph or TokenSave ecosystem actually writes
// this marker file — adopting it there is future integration work, out of
// scope for this package. Until (and unless) a tool adopts it, or a caller
// supplies a [RootReader] that already knows how to extract that specific
// tool's own root declaration (for example, a closure that shells out to a
// tool's status command and parses its output — logic that belongs to the
// caller, not to this package), an index directory that exists but
// declares no readable root is reported as [StatusUnverifiable], never as
// matching. "Unknown" and "matches" must never be confused, since the
// entire point of this package is to stop an agent from trusting a result
// it cannot actually verify.
//
// # Four states, not two
//
// [Diagnose] never collapses to a boolean. [StatusOK] means the index root
// was determined and matches the active worktree. [StatusMismatch] means
// it was determined and does *not* match — a stale or wrong-repository
// index. [StatusMissing] means no index directory was found at all.
// [StatusUnverifiable] means an index directory exists but its declared
// root could not be determined, which is deliberately distinct from both
// OK and Mismatch: a caller that only checked for "not mismatch" would
// wrongly treat "I couldn't check" the same as "it's fine."
//
// # Severity is contract-driven, not hardcoded
//
// Whether a [StatusMismatch] (or [StatusMissing], or [StatusUnverifiable])
// diagnosis should merely warn or should block a workflow is a policy
// decision, not something this package decides on its own — see
// [EvaluateSeverity] and [Policy].
//
// # Relationship to discovery.IndexStatus
//
// github.com/mediusfy/modulex/discovery already reports whether a
// well-known index directory (.codegraph, .tokensave) is *present* at a
// repository root (discovery.IndexStatus). semindex answers a deeper
// question discovery does not attempt: given that a directory is present,
// does it actually belong to *this* worktree? semindex does not import
// discovery and does not require a discovery.Repository — Diagnose works
// from a plain root path and index directory, so a caller who already ran
// discovery.Discover can feed it an index's Dir, and a caller who has not
// can call Diagnose directly.
//
// # Not a Modulex core dependency
//
// Like provenance, discovery, contract, and verify, semindex is a
// standalone leaf package: it imports nothing from the core modulex
// package, requires no third-party dependency (no SQLite driver, no
// YAML/JSON library), and integrating it into an agent workflow is
// entirely optional.
//
// See docs/planning/semantic-index-diagnostics-guide.md for the full
// guide, including a worked example.
package semindex

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MarkerFileName is the well-known filename this package's built-in
// convention uses for an index's root-declaration marker file, written
// directly inside the index directory (e.g. .codegraph/.modulex-index-root
// or .tokensave/.modulex-index-root). See the package doc comment's "The
// marker-file convention, and its limits" section.
const MarkerFileName = ".modulex-index-root"

// Status is the outcome of diagnosing one semantic index against the
// active worktree. Modeled as an explicit four-value enum (mirroring
// provenance.Status's discipline of never conflating "did not run" with
// "ran and failed") so "I couldn't verify this" is never confused with
// either "it's fine" or "it's wrong."
type Status string

const (
	// StatusOK means the index's declared root was determined and matches
	// the active worktree.
	StatusOK Status = "ok"
	// StatusMismatch means the index's declared root was determined and
	// does not match the active worktree — a stale or wrong-repository
	// index.
	StatusMismatch Status = "mismatch"
	// StatusMissing means no index directory was found at the given path
	// at all.
	StatusMissing Status = "missing"
	// StatusUnverifiable means an index directory exists but its declared
	// root could not be determined: no RootReader was supplied, the
	// supplied RootReader reported ok=false, or it returned an error. This
	// is distinct from both StatusOK and StatusMismatch — it means
	// "unknown," never "assume it's fine."
	StatusUnverifiable Status = "unverifiable"
)

// IndexRoot is the raw result of attempting to read a semantic index's
// declared root, before that root is compared against anything. Name and
// Dir describe the index this attempt is about; Root and Determined
// describe the outcome. Determined is false whenever Root should not be
// trusted — including when Root happens to be empty — so a caller can
// never mistake "we couldn't determine a root" for "the root is empty."
type IndexRoot struct {
	// Name is a short, human-readable identifier for the index tool this
	// directory belongs to (e.g. "codegraph", "tokensave").
	Name string
	// Dir is the path to the index directory that was inspected, exactly
	// as the caller supplied it (this package neither requires nor forces
	// it to be relative or absolute).
	Dir string
	// Root is the root path the index declares, from the marker file or a
	// custom RootReader. Only meaningful when Determined is true; "" when
	// Determined is false.
	Root string
	// Determined is true iff Root was successfully read from the index.
	// false means "unknown/unverifiable," never "matches" or "does not
	// match."
	Determined bool
}

// Diagnosis is the result of comparing one semantic index's declared root
// against the active worktree root, including human-readable remediation
// guidance. Per ADR-0032's "redact command output before it enters
// provenance artifacts" and this ticket's "without exposing source
// content" requirement, Remediation is built only from Name, the two root
// paths, and static guidance text — never file listings, index contents,
// or anything else about the repository.
type Diagnosis struct {
	// Name is the short, human-readable identifier for the index tool
	// this diagnosis is about (e.g. "codegraph", "tokensave").
	Name string
	// Status is the diagnosis outcome. See the Status constants.
	Status Status
	// WorktreeRoot is the active worktree root this index was compared
	// against, as given to Diagnose.
	WorktreeRoot string
	// IndexRoot is the root path the index declared, or "" if Status is
	// StatusMissing or StatusUnverifiable (i.e. no root was determined).
	IndexRoot string
	// Remediation is human-readable, actionable guidance appropriate to
	// Status. Never empty.
	Remediation string
}

// RootReader is a caller-supplied function that extracts a semantic
// index's declared root by whatever means that specific tool requires:
// reading its own file format, shelling out to a status command and
// parsing its output, or anything else. This package never implements
// tool-specific extraction itself — that is precisely the boundary that
// keeps tool-specific logic (and any dependency it would require) out of
// this dependency-light core. Use [MarkerFileReader] or
// [DefaultMarkerReader] for indexes that adopt this package's own
// marker-file convention.
//
// ok is false (with a nil error) when the index simply does not declare a
// root by whatever means reader knows how to check — the normal "can't
// verify this one" case, not a failure. A non-nil error is reserved for an
// unexpected failure while a root declaration that should have been
// readable was being read (e.g. a permissions error, a malformed status
// response).
type RootReader func(indexDir string) (root string, ok bool, err error)

// ReadIndexRoot attempts to determine the root name (an index tool
// identifier) at dir declares, using reader. A nil reader always yields an
// undetermined IndexRoot (Determined: false) — Diagnose treats that the
// same as any other reader that could not determine a root, reporting
// StatusUnverifiable rather than silently treating a missing reader as a
// match.
//
// ReadIndexRoot does not check whether dir exists or is a directory; see
// Diagnose, which checks that first and only calls ReadIndexRoot once it
// knows dir is present.
func ReadIndexRoot(name, dir string, reader RootReader) IndexRoot {
	out := IndexRoot{Name: name, Dir: dir}
	if reader == nil {
		return out
	}
	root, ok, err := reader(dir)
	if err != nil || !ok || strings.TrimSpace(root) == "" {
		return out
	}
	out.Root = root
	out.Determined = true
	return out
}

// Diagnose compares indexDir's declared root (as determined by reader)
// against worktreeRoot, and returns a Diagnosis with human-readable
// remediation guidance.
//
//   - If indexDir does not exist (or is not a directory), Diagnose returns
//     StatusMissing.
//   - If indexDir exists but reader is nil, returns ok=false, or returns
//     an error, Diagnose returns StatusUnverifiable — the root could not
//     be determined, which must never be treated as a match.
//   - Otherwise, worktreeRoot and the determined index root are both
//     resolved to an absolute, symlink-resolved form (see
//     filepath.EvalSymlinks) before comparison, so that e.g. /tmp vs.
//     /private/tmp on macOS never produces a false-positive mismatch.
//     Diagnose returns StatusOK if they match, StatusMismatch otherwise.
//
// name identifies the index tool for the returned Diagnosis (e.g.
// "codegraph", "tokensave") and appears only as a plain label — it is
// never interpolated into a shell command or file path by this package.
func Diagnose(worktreeRoot, indexDir, name string, reader RootReader) Diagnosis {
	d := Diagnosis{Name: name, WorktreeRoot: worktreeRoot}

	info, err := os.Stat(indexDir)
	if err != nil || !info.IsDir() {
		d.Status = StatusMissing
		d.Remediation = fmt.Sprintf(
			"no index found at %s; initialize it if this tool's features are needed here.",
			indexDir,
		)
		return d
	}

	ir := ReadIndexRoot(name, indexDir, reader)
	if !ir.Determined {
		d.Status = StatusUnverifiable
		d.Remediation = fmt.Sprintf(
			"index directory exists at %s but its root could not be determined (no root marker found); treat its results with caution until this is resolved.",
			indexDir,
		)
		return d
	}

	d.IndexRoot = ir.Root

	if normalizeRoot(worktreeRoot) == normalizeRoot(ir.Root) {
		d.Status = StatusOK
		d.Remediation = "index root matches the active worktree; no remediation needed."
		return d
	}

	d.Status = StatusMismatch
	d.Remediation = fmt.Sprintf(
		"this index was built against %s but the active worktree is %s; re-run the index tool's sync/init command in this worktree, or point the tool at the correct root.",
		ir.Root, worktreeRoot,
	)
	return d
}

// normalizeRoot resolves p to an absolute, symlink-resolved path for
// comparison purposes only (it never mutates or requires anything to
// exist on disk beyond what filepath.Abs/EvalSymlinks themselves need). If
// p cannot be fully resolved — it doesn't exist on disk, or filepath.Abs
// itself fails — normalizeRoot falls back to the cleaned form of whatever
// resolution succeeded, so an unresolvable or nonexistent path still
// compares consistently (and is never treated as automatically mismatching
// or matching by virtue of the failure) rather than aborting the
// diagnosis.
func normalizeRoot(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(abs)
}

// MarkerFileReader returns a RootReader implementing this package's
// marker-file convention (see the package doc comment): it looks for a
// plain-text file named markerName directly inside the index directory
// and returns its first non-empty line, trimmed of surrounding whitespace,
// as the declared root.
//
// ok is false (with a nil error) if the marker file does not exist, or
// exists but contains no non-empty line — both are the ordinary "this
// index doesn't declare a root" case, not a failure. A non-nil error is
// reserved for an unexpected I/O failure reading a marker file that does
// exist (e.g. a permissions problem).
//
// MarkerFileReader expects (but does not enforce) that the declared root
// is an absolute path, matching what WriteMarkerFile writes; a relative
// value degrades to whatever normalizeRoot's filepath.Abs resolution
// produces (relative to the process's current working directory), which
// is unlikely to match any worktree root and will typically surface as
// StatusMismatch rather than a crash.
func MarkerFileReader(markerName string) RootReader {
	return func(indexDir string) (string, bool, error) {
		data, err := os.ReadFile(filepath.Join(indexDir, markerName))
		if err != nil {
			if os.IsNotExist(err) {
				return "", false, nil
			}
			return "", false, fmt.Errorf("semindex: reading marker file %s: %w", markerName, err)
		}
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				return line, true, nil
			}
		}
		if err := scanner.Err(); err != nil {
			return "", false, fmt.Errorf("semindex: reading marker file %s: %w", markerName, err)
		}
		return "", false, nil
	}
}

// DefaultMarkerReader is a ready-to-use RootReader for MarkerFileName, for
// any caller adopting this package's marker-file convention as-is rather
// than a custom marker filename.
var DefaultMarkerReader = MarkerFileReader(MarkerFileName)

// WriteMarkerFile writes root as the declared root for the index at
// indexDir, using this package's marker-file convention: a single line of
// plain text in a file named markerName inside indexDir. It creates
// indexDir (and any missing parents) if necessary.
//
// WriteMarkerFile is not required by Diagnose, ReadIndexRoot, or
// MarkerFileReader — those only ever read. It exists so that a real index
// tool wanting to adopt this convention (or a test exercising the full
// marker-file path without a real third-party tool installed) can do so
// in one call, without needing to know this package's exact on-disk
// format beyond calling this function.
func WriteMarkerFile(indexDir, markerName, root string) error {
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		return fmt.Errorf("semindex: creating index directory %s: %w", indexDir, err)
	}
	content := root + "\n"
	if err := os.WriteFile(filepath.Join(indexDir, markerName), []byte(content), 0o644); err != nil {
		return fmt.Errorf("semindex: writing marker file %s: %w", markerName, err)
	}
	return nil
}

// ResolveWorktreeRoot determines the active worktree root to diagnose
// indexes against. It prefers `git -C dir rev-parse --show-toplevel`,
// falling back to fallbackRoot if git is not on PATH, dir is not inside a
// git working tree, or the command otherwise fails or returns an empty
// result.
//
// This runs a fixed git subcommand with exactly one interpolated
// argument — dir, the directory whose worktree root is being resolved —
// and no other input built from anything an external contributor,
// repository file, or index tool controls. That matters here specifically
// because this package's whole purpose is comparing paths that come from
// places this package does not control (a marker file's declared root, a
// caller-supplied RootReader's output): per the discipline
// verify.isPathSafeForCommand exists to enforce (never let untrusted text
// reach a shell-interpolated command), ResolveWorktreeRoot's exec.Command
// call takes dir as a discrete argv element, never concatenated into a
// shell string, and never touches an index's declared root at all — that
// value only ever reaches string comparison in normalizeRoot, never a
// command line.
//
// dir is expected to be a filesystem path the caller already trusts
// (typically the process's own working directory), not arbitrary
// untrusted text.
func ResolveWorktreeRoot(dir, fallbackRoot string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	root := strings.TrimSpace(string(out))
	if err != nil || root == "" {
		if strings.TrimSpace(fallbackRoot) != "" {
			return fallbackRoot, nil
		}
		if err != nil {
			return "", fmt.Errorf("semindex: resolving worktree root for %s: %w", dir, err)
		}
		return "", errors.New("semindex: git rev-parse returned an empty root and no fallback root was given")
	}
	return root, nil
}
