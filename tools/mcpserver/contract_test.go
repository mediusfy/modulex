package mcpserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mediusfy/modulex/contract"
)

func writeContractFile(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, contract.FileName), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestReadContract(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "no contract file present",
			run: func(t *testing.T) {
				dir := t.TempDir()

				out, err := readContract(dir)
				if err != nil {
					t.Fatalf("readContract() error = %v", err)
				}
				if out.Present {
					t.Error("Present = true, want false (no modulex.agent.yaml written)")
				}
				if out.Contract != nil {
					t.Error("Contract is non-nil, want nil when Present is false")
				}
			},
		},
		{
			name: "present and valid",
			run: func(t *testing.T) {
				dir := t.TempDir()
				writeContractFile(t, dir, `
schema_version: "1.0.0"
projects:
  - name: example
    path: .
`)

				out, err := readContract(dir)
				if err != nil {
					t.Fatalf("readContract() error = %v", err)
				}
				if !out.Present {
					t.Fatal("Present = false, want true")
				}
				if out.Contract == nil {
					t.Fatal("Contract is nil, want a parsed contract")
				}
				if len(out.ValidationErrors) != 0 {
					t.Errorf("ValidationErrors = %v, want none", out.ValidationErrors)
				}
			},
		},
		{
			name: "present but missing required field",
			run: func(t *testing.T) {
				dir := t.TempDir()
				writeContractFile(t, dir, `
schema_version: "1.0.0"
`)

				out, err := readContract(dir)
				if err != nil {
					t.Fatalf("readContract() error = %v", err)
				}
				if !out.Present {
					t.Fatal("Present = false, want true")
				}
				if out.Contract == nil {
					t.Fatal("Contract is nil, want a parsed (if invalid) contract")
				}
				if len(out.ValidationErrors) == 0 {
					t.Error("ValidationErrors is empty, want at least one (no projects declared)")
				}
			},
		},
		{
			name: "present but malformed YAML",
			run: func(t *testing.T) {
				dir := t.TempDir()
				writeContractFile(t, dir, "schema_version: [this is not valid: yaml")

				out, err := readContract(dir)
				if err != nil {
					t.Fatalf("readContract() error = %v", err)
				}
				if !out.Present {
					t.Fatal("Present = false, want true")
				}
				if out.Contract != nil {
					t.Error("Contract is non-nil, want nil for malformed YAML")
				}
				if len(out.ValidationErrors) != 1 {
					t.Errorf("ValidationErrors = %v, want exactly one parse-error string", out.ValidationErrors)
				}
			},
		},
		{
			name: "nonexistent root is an error, not present=false",
			run: func(t *testing.T) {
				_, err := readContract("/does/not/exist/at/all")
				if err == nil {
					t.Fatal("readContract() error = nil, want an error for a root that does not exist itself (distinct from a valid root with no contract file)")
				}
			},
		},
		{
			name: "root that is a file, not a directory, is an error",
			run: func(t *testing.T) {
				dir := t.TempDir()
				filePath := filepath.Join(dir, "not-a-dir")
				if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}

				_, err := readContract(filePath)
				if err == nil {
					t.Fatal("readContract() error = nil, want an error when root is a file, not a directory")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
