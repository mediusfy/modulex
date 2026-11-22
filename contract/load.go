package contract

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// FileName is the well-known repository contract file name, per
// docs/planning/agent-repository-contract-guide.md. Every adapter that
// reads modulex.agent.yaml (the `modulex agent` CLI, the MCP server, the
// hosted PR-review worker) uses this constant and Load below, so a future
// change to how the contract is located or parsed has exactly one place to
// land instead of drifting across independently hand-rolled copies.
const FileName = "modulex.agent.yaml"

// maxFileSize caps how many bytes Load will read from FileName. Without a
// cap, a contract path that is (or is a symlink to) an unbounded source
// like /dev/zero can OOM the reading process. 1 MiB is far larger than any
// legitimate contract file.
const maxFileSize = 1 << 20 // 1 MiB

// Load reads <root>/FileName and unmarshals it into a Contract. It never
// calls Validate — schema validation is a separate, caller-chosen step
// (some callers want a hard failure on an invalid contract, some want to
// report validation errors softly; see Contract.Validate), and some
// callers don't want it at all (a contract that fails validation for some
// unrelated reason can still declare protected paths that must be
// enforced).
//
// present is false only when the file does not exist at all — that is
// always a normal, error-free outcome. This includes the case where root
// itself doesn't exist: os.ReadFile reports the same ENOENT for a missing
// parent directory as for a missing file, so Load cannot and does not
// distinguish "root is valid but has no FileName" from "root itself
// doesn't exist" — both report present=false, err=nil. A caller that must
// tell them apart (e.g. mcpserver's readContract) stats root itself
// before calling Load. Every other combination sets err:
//
//   - present=false, err!=nil: the file could not even be read for a
//     reason other than ENOENT (e.g. a permission error, or the path is a
//     symlink/FIFO/device rather than a regular file, or its contents
//     exceed maxFileSize) — not the normal "no contract" state.
//   - present=true, err!=nil: the file exists but failed to unmarshal as
//     YAML into Contract's shape. c is nil.
//   - present=true, err==nil: the file was read and parsed; c is non-nil.
func Load(root string) (c *Contract, present bool, err error) {
	path := filepath.Join(root, FileName)

	// Lstat, not Stat: a symlink's own mode is never IsRegular, so this
	// rejects a symlink to /dev/zero, a FIFO, or any other special file
	// without following it — the read below never touches the target.
	info, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return nil, false, nil
	}
	if statErr != nil {
		return nil, false, fmt.Errorf("reading %s: %w", FileName, statErr)
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("reading %s: refusing to read non-regular file (mode %v)", FileName, info.Mode())
	}

	f, openErr := os.Open(path)
	if errors.Is(openErr, os.ErrNotExist) {
		return nil, false, nil
	}
	if openErr != nil {
		return nil, false, fmt.Errorf("reading %s: %w", FileName, openErr)
	}
	defer func() { _ = f.Close() }()

	data, readErr := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if readErr != nil {
		return nil, false, fmt.Errorf("reading %s: %w", FileName, readErr)
	}
	if len(data) > maxFileSize {
		return nil, false, fmt.Errorf("reading %s: exceeds %d byte limit", FileName, maxFileSize)
	}

	parsed, unmarshalErr := Unmarshal(data)
	if unmarshalErr != nil {
		return nil, true, unmarshalErr
	}
	return parsed, true, nil
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
