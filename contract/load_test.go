package contract

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestLoad covers the three outcomes Load distinguishes via (present, err)
// — absent file, present-but-unparseable, and present-and-parsed — plus the
// bad-root case, as rows of the same write-then-Load shape.
func TestLoad(t *testing.T) {
	tests := []struct {
		name         string
		yaml         string // written as root/FileName; skipped if writeFile is false
		writeFile    bool
		badRoot      bool // root itself does not exist
		wantPresent  bool
		wantErr      bool
		wantProtPath []string
	}{
		{
			name:        "absent file is not an error",
			writeFile:   false,
			wantPresent: false,
			wantErr:     false,
		},
		{
			name:        "present but unparseable is an error",
			writeFile:   true,
			yaml:        "protected_paths: [oops\n",
			wantPresent: true,
			wantErr:     true,
		},
		{
			name:         "present and parsed returns the contract",
			writeFile:    true,
			yaml:         "schema_version: \"1.0.0\"\nprotected_paths:\n  - go.mod\n",
			wantPresent:  true,
			wantErr:      false,
			wantProtPath: []string{"go.mod"},
		},
		{
			// Load cannot tell "root is valid but has no FileName" from
			// "root itself doesn't exist" — both are ENOENT on the same
			// ReadFile call — so it reports both as the ordinary absent
			// case. A caller that must distinguish them (e.g. mcpserver's
			// readContract) stats root itself before calling Load.
			name:        "root itself missing is reported as absent, not an error",
			badRoot:     true,
			wantPresent: false,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.badRoot {
				root = filepath.Join(root, "does-not-exist")
			} else if tt.writeFile {
				if err := os.WriteFile(filepath.Join(root, FileName), []byte(tt.yaml), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}

			c, present, err := Load(root)
			checkLoadResult(t, c, present, err, tt.wantPresent, tt.wantErr, tt.wantProtPath)
		})
	}
}

// TestLoad_SymlinkToUnboundedSource guards against the OOM in issue #184:
// a contract path that is a symlink to an unbounded device like /dev/zero
// must be rejected via the Lstat regular-file check before any read is
// attempted, rather than followed and read without a size cap.
func TestLoad_SymlinkToUnboundedSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/dev/zero is not available on windows")
	}
	if _, err := os.Stat("/dev/zero"); err != nil {
		t.Skipf("/dev/zero not available: %v", err)
	}

	root := t.TempDir()
	if err := os.Symlink("/dev/zero", filepath.Join(root, FileName)); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	c, present, err := Load(root)
	if c != nil {
		t.Errorf("c = %+v, want nil", c)
	}
	if present {
		t.Error("present = true, want false")
	}
	if err == nil {
		t.Fatal("err = nil, want a non-regular-file error")
	}
	if !strings.Contains(err.Error(), "non-regular file") {
		t.Errorf("err = %v, want it to mention the file is not regular", err)
	}
}

// TestLoad_OversizedFileIsRejected guards against the size half of issue
// #184: even a plain regular file that exceeds maxFileSize must be
// rejected rather than fully buffered into memory.
func TestLoad_OversizedFileIsRejected(t *testing.T) {
	root := t.TempDir()
	oversized := strings.Repeat("a", maxFileSize+1)
	if err := os.WriteFile(filepath.Join(root, FileName), []byte(oversized), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	c, present, err := Load(root)
	if c != nil {
		t.Errorf("c = %+v, want nil", c)
	}
	if present {
		t.Error("present = true, want false")
	}
	if err == nil {
		t.Fatal("err = nil, want a size-limit error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("err = %v, want it to mention the size limit", err)
	}
}

func checkLoadResult(t *testing.T, c *Contract, present bool, err error, wantPresent, wantErr bool, wantProtPath []string) {
	t.Helper()
	if present != wantPresent {
		t.Errorf("present = %v, want %v", present, wantPresent)
	}
	if (err != nil) != wantErr {
		t.Errorf("err = %v, wantErr %v", err, wantErr)
	}
	if wantErr || !wantPresent {
		if c != nil {
			t.Errorf("c = %+v, want nil", c)
		}
		return
	}
	if c == nil {
		t.Fatal("c = nil, want a parsed contract")
	}
	if len(c.ProtectedPaths) != len(wantProtPath) {
		t.Fatalf("ProtectedPaths = %v, want %v", c.ProtectedPaths, wantProtPath)
	}
	for i, p := range wantProtPath {
		if c.ProtectedPaths[i] != p {
			t.Errorf("ProtectedPaths[%d] = %q, want %q", i, c.ProtectedPaths[i], p)
		}
	}
}
