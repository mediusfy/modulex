//go:build unix

package contract

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const validContractYAML = "schema_version: \"1.0.0\"\nprotected_paths:\n  - go.mod\n"

// TestLoadRejectsIrregularFiles covers the paths Load must refuse to read
// rather than follow: a symlink (whatever it points at), a device node
// reached through one, and a FIFO. The device and FIFO rows are the
// regression for the OOM/hang reported in issue #184 — before Load stat'ed
// the path, the /dev/zero row read bytes until the process died and the
// FIFO row blocked forever. Each row is set up by a func so the rows can
// create different kinds of entry at the same FileName path.
func TestLoadRejectsIrregularFiles(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{
			name: "symlink to /dev/zero is refused, not read",
			setup: func(t *testing.T, path string) {
				requireCharDevice(t, "/dev/zero")
				symlink(t, "/dev/zero", path)
			},
		},
		{
			name: "symlink to a valid contract file is refused",
			setup: func(t *testing.T, path string) {
				target := filepath.Join(t.TempDir(), "real-"+FileName)
				writeFile(t, target, validContractYAML)
				symlink(t, target, path)
			},
		},
		{
			name: "dangling symlink is refused",
			setup: func(t *testing.T, path string) {
				symlink(t, filepath.Join(t.TempDir(), "gone"), path)
			},
		},
		{
			name: "FIFO is refused, not read",
			setup: func(t *testing.T, path string) {
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Skipf("Mkfifo: %v", err)
				}
			},
		},
		{
			name: "directory is refused",
			setup: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatalf("Mkdir: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(t, filepath.Join(root, FileName))

			// A Load that follows the entry instead of refusing it either
			// never returns or exhausts memory, so run it with a deadline
			// and fail loudly rather than letting the suite hang.
			c, present, err := loadWithDeadline(t, root)

			if !present {
				t.Errorf("present = false, want true: the entry does exist")
			}
			if !errors.Is(err, ErrNotRegularFile) {
				t.Errorf("err = %v, want ErrNotRegularFile", err)
			}
			if c != nil {
				t.Errorf("c = %+v, want nil", c)
			}
		})
	}
}

// TestReadCappedOnDevice checks the descriptor-level guard directly: even
// handed a path that is itself a character device, readCapped must refuse
// it rather than read an endless stream. Load's Lstat already rejects the
// symlink case above; this covers the window where the path is swapped
// between that stat and the open.
func TestReadCappedOnDevice(t *testing.T) {
	requireCharDevice(t, "/dev/zero")

	done := make(chan error, 1)
	go func() {
		_, err := readCapped("/dev/zero")
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegularFile) {
			t.Errorf("readCapped(/dev/zero) err = %v, want ErrNotRegularFile", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("readCapped(/dev/zero) did not return within 30s; the size cap is not bounding the read")
	}
}

// TestLoadThroughSymlinkedRoot pins the documented limit of the symlink
// refusal: only the final FileName component must be a real file. A
// repository checked out under a symlinked directory still loads, so the
// fix changes nothing for normal contract files.
func TestLoadThroughSymlinkedRoot(t *testing.T) {
	realRoot := t.TempDir()
	writeFile(t, filepath.Join(realRoot, FileName), validContractYAML)

	linkedRoot := filepath.Join(t.TempDir(), "linked-root")
	symlink(t, realRoot, linkedRoot)

	c, present, err := Load(linkedRoot)
	checkLoadResult(t, c, present, err, true, false, []string{"go.mod"})
}

func loadWithDeadline(t *testing.T, root string) (*Contract, bool, error) {
	t.Helper()

	type result struct {
		c       *Contract
		present bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		c, present, err := Load(root)
		done <- result{c, present, err}
	}()

	select {
	case r := <-done:
		return r.c, r.present, r.err
	case <-time.After(30 * time.Second):
		t.Fatal("Load did not return within 30s; it is following the entry instead of refusing it")
		return nil, false, nil
	}
}

func symlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("Symlink: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func requireCharDevice(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Skipf("Stat %s: %v", path, err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		t.Skipf("%s is not a character device on this system", path)
	}
}
