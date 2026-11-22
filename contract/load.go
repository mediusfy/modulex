package contract

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/mediusfy/modulex/apperrors"
)

// FileName is the well-known repository contract file name, per
// docs/planning/agent-repository-contract-guide.md. Every adapter that
// reads modulex.agent.yaml (the `modulex agent` CLI, the MCP server, the
// hosted PR-review worker) uses this constant and Load below, so a future
// change to how the contract is located or parsed has exactly one place to
// land instead of drifting across independently hand-rolled copies.
const FileName = "modulex.agent.yaml"

// maxFileSize caps how many bytes Load reads from a FileName file. A
// repository contract is a short hand-written declaration — the one in this
// repository is a few kilobytes — so 1 MiB is far more than any real
// contract needs while still bounding the memory one Load can consume. The
// cap matters because Load runs inside long-lived shared processes (the MCP
// server, the hosted PR-review worker) against working trees those
// processes do not own: without it a single oversized FileName takes the
// whole process down instead of failing one request.
const maxFileSize = 1 << 20

// ErrNotRegularFile is reported by Load when FileName exists but is not a
// regular file — a symlink, a FIFO, a device node or a directory. Load
// refuses to follow a symlink and refuses to read a special file rather
// than treating its bytes as contract content: reading one is either
// unbounded (a device such as /dev/zero yields bytes forever) or blocking
// (a FIFO with no writer never returns), and the path is attacker-
// controlled relative to the process doing the reading.
const ErrNotRegularFile apperrors.Error = "not a regular file"

// ErrTooLarge is reported by Load when FileName is a regular file holding
// more than maxFileSize bytes. It is a separate sentinel from
// ErrNotRegularFile so a caller can tell "this is not a contract file at
// all" from "this is a contract file far larger than a contract may be".
const ErrTooLarge apperrors.Error = "file is too large"

// Load reads <root>/FileName and unmarshals it into a Contract. It never
// calls Validate — schema validation is a separate, caller-chosen step
// (some callers want a hard failure on an invalid contract, some want to
// report validation errors softly; see Contract.Validate), and some
// callers don't want it at all (a contract that fails validation for some
// unrelated reason can still declare protected paths that must be
// enforced).
//
// FileName must be a regular file of at most maxFileSize bytes. Anything
// else — a symlink, a FIFO, a device node, a directory, or an oversized
// regular file — sets present=true and err (ErrNotRegularFile or
// ErrTooLarge); Load reads no content in those cases. Normal contract
// files, including ones reached because root itself is a symlinked
// directory, are unaffected: only the final FileName component is
// required not to be a link.
//
// present is false only when the file does not exist at all — that is
// always a normal, error-free outcome. This includes the case where root
// itself doesn't exist: os.Lstat reports the same ENOENT for a missing
// parent directory as for a missing file, so Load cannot and does not
// distinguish "root is valid but has no FileName" from "root itself
// doesn't exist" — both report present=false, err=nil. A caller that must
// tell them apart (e.g. mcpserver's readContract) stats root itself
// before calling Load. Every other combination sets err:
//
//   - present=false, err!=nil: the file could not even be stat'ed for a
//     reason other than ENOENT (e.g. a permission error) — not the
//     normal "no contract" state.
//   - present=true, err!=nil: the file exists but is not a readable
//     regular contract file, or failed to unmarshal as YAML into
//     Contract's shape. c is nil.
//   - present=true, err==nil: the file was read and parsed; c is non-nil.
func Load(root string) (c *Contract, present bool, err error) {
	path := filepath.Join(root, FileName)

	// Lstat, not Stat: Stat follows a symlink and would report the mode of
	// whatever the link points at, so a link to a regular file would pass
	// and a link to /dev/zero would be read below. Lstat describes the
	// path entry itself, which is what must be a regular file.
	info, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return nil, false, nil
	}
	if statErr != nil {
		return nil, false, fmt.Errorf("reading %s: %w", FileName, statErr)
	}
	if !info.Mode().IsRegular() {
		return nil, true, fmt.Errorf("reading %s: %w (mode %s)", FileName, ErrNotRegularFile, info.Mode().Type())
	}

	data, readErr := readCapped(path)
	if readErr != nil {
		return nil, true, fmt.Errorf("reading %s: %w", FileName, readErr)
	}

	parsed, unmarshalErr := Unmarshal(data)
	if unmarshalErr != nil {
		return nil, true, unmarshalErr
	}
	return parsed, true, nil
}

// readCapped opens path and returns its contents, refusing to read more
// than maxFileSize bytes. It re-checks the opened file's mode through the
// descriptor rather than trusting Load's earlier Lstat, which closes the
// window in which the path is swapped for a symlink or a device node
// between the stat and the open. The read itself goes through a
// LimitReader so that a file reporting size 0 while yielding bytes forever
// — which is exactly what a character device such as /dev/zero does — is
// still bounded; the stat size alone cannot be trusted for that reason.
func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w (mode %s)", ErrNotRegularFile, info.Mode().Type())
	}

	// maxFileSize+1 so that a file of exactly the cap reads fully and
	// anything beyond it is detectable as "there was more".
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("%w (limit %d bytes)", ErrTooLarge, maxFileSize)
	}
	return data, nil
}

// Unmarshal parses data — the contents of a FileName file — into a
// Contract. Load uses this after reading the file from the working tree;
// a caller that obtains the file's bytes some other way (e.g. `git show
// <ref>:<FileName>`, to read the contract as it existed at a specific git
// ref rather than the current working tree) can call this directly.
func Unmarshal(data []byte) (*Contract, error) {
	var parsed Contract
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", FileName, err)
	}
	return &parsed, nil
}
