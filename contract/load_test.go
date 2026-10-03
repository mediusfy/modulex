package contract

import (
	"os"
	"path/filepath"
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

			if present != tt.wantPresent {
				t.Errorf("present = %v, want %v", present, tt.wantPresent)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr || !tt.wantPresent {
				if c != nil {
					t.Errorf("c = %+v, want nil", c)
				}
				return
			}
			if c == nil {
				t.Fatal("c = nil, want a parsed contract")
			}
			if len(c.ProtectedPaths) != len(tt.wantProtPath) {
				t.Fatalf("ProtectedPaths = %v, want %v", c.ProtectedPaths, tt.wantProtPath)
			}
			for i, p := range tt.wantProtPath {
				if c.ProtectedPaths[i] != p {
					t.Errorf("ProtectedPaths[%d] = %q, want %q", i, c.ProtectedPaths[i], p)
				}
			}
		})
	}
}
