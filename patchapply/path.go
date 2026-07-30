package patchapply

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrEmptyPath is returned (wrapped) when a FileChange.Path is empty.
var ErrEmptyPath = errors.New("patchapply: path must not be empty")

// ErrAbsolutePath is returned (wrapped) when a FileChange.Path is absolute.
// Every path this package accepts is relative to targetDir; an absolute
// path is always rejected outright, never silently reinterpreted as
// relative.
var ErrAbsolutePath = errors.New("patchapply: path must be relative to targetDir, not absolute")

// ErrPathTraversal is returned (wrapped) when a FileChange.Path would
// escape targetDir — via a literal ".." component or via a symlink planted
// somewhere in the existing portion of the path. This is the same class of
// bug as a shell-injection vulnerability, for the filesystem instead of a
// shell, and is treated with the same seriousness: rejected outright, never
// clamped or sanitized into something "safe."
var ErrPathTraversal = errors.New("patchapply: path escapes targetDir")

// resolvedPath validates that rel is a safe path relative to
// realTargetDir (which the caller must already have resolved to its real,
// symlink-free absolute form via filepath.EvalSymlinks) and returns the
// absolute, joined path it refers to.
//
// It rejects — never silently clamps or sanitizes — any of:
//
//   - an empty path;
//   - an absolute path;
//   - a path that is, or resolves under filepath.Clean to, ".." or
//     anything beginning with "../" (after Clean, any remaining ".."
//     element is guaranteed to be a leading component, since Clean fully
//     resolves any ".." that has a preceding real component to cancel
//     against — so checking the cleaned path's prefix is sufficient to
//     catch every traversal shape);
//   - a path that, after cleaning and joining, is not realTargetDir
//     itself or a path preceded by realTargetDir plus a separator (defense
//     in depth in case some future change to this function's logic above
//     ever let a traversal slip through uncaught);
//   - a path whose nearest EXISTING ancestor directory resolves (via
//     filepath.EvalSymlinks, which follows every symlink in that
//     directory's own chain) outside realTargetDir — catching a symlink
//     planted inside targetDir that points elsewhere on disk, even though
//     the target file itself may not exist yet;
//   - a path that cleans to "." — referring to targetDir itself, which is
//     never a valid file target.
func resolvedPath(realTargetDir, rel string) (string, error) {
	if rel == "" {
		return "", ErrEmptyPath
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %q", ErrAbsolutePath, rel)
	}

	cleaned := filepath.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrPathTraversal, rel)
	}
	if cleaned == "." {
		return "", fmt.Errorf("%w: %q refers to the target directory itself", ErrPathTraversal, rel)
	}

	full := filepath.Join(realTargetDir, cleaned)
	if !isWithin(realTargetDir, full) {
		return "", fmt.Errorf("%w: %q", ErrPathTraversal, rel)
	}

	if err := verifyNoSymlinkEscape(realTargetDir, full); err != nil {
		return "", err
	}

	return full, nil
}

// isWithin reports whether target is base itself or a descendant of base.
// Both arguments must already be cleaned, absolute paths.
func isWithin(base, target string) bool {
	if target == base {
		return true
	}
	return strings.HasPrefix(target, base+string(filepath.Separator))
}

// verifyNoSymlinkEscape walks up from full's parent directory to the
// nearest existing ancestor, resolves that ancestor's real path (following
// every symlink along the way, exactly like filepath.EvalSymlinks does for
// any existing path), and confirms the result is still within
// realTargetDir. This catches a symlink placed anywhere in the existing
// portion of full's directory chain that would otherwise let a write
// escape targetDir, even though full itself may not exist yet — full's own
// existence is never assumed, since this package's whole purpose is
// writing files that often do not exist yet.
func verifyNoSymlinkEscape(realTargetDir, full string) error {
	dir := filepath.Dir(full)
	for {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			if !isWithin(realTargetDir, resolved) {
				return fmt.Errorf("%w: resolves outside targetDir via a symlink", ErrPathTraversal)
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("patchapply: resolving %q: %w", dir, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root without finding an existing
			// ancestor. realTargetDir itself is required to exist (Apply
			// checks this before calling resolvedPath), so in practice
			// this loop always finds at least realTargetDir along the
			// way; this is a defensive termination, not an expected path.
			return nil
		}
		dir = parent
	}
}
