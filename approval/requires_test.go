package approval

import (
	"errors"
	"testing"

	"github.com/mediusfy/modulex/contract"
	"github.com/mediusfy/modulex/provenance"
)

func testContract() contract.Contract {
	return contract.Contract{
		SchemaVersion: "1.0.0",
		Projects: []contract.Project{
			{Name: "modulex", Path: "."},
		},
		Commands: []contract.CommandDecl{
			{Name: "go-test", Command: "go test ./...", Class: provenance.ClassSafe},
			{Name: "gofmt-w", Command: "gofmt -w .", Class: provenance.ClassMutating},
			{Name: "go-mod-download", Command: "go mod download", Class: provenance.ClassNetworked},
			{Name: "git-reset-hard", Command: "git reset --hard HEAD~1", Class: provenance.ClassDestructive},
			{Name: "release", Command: "make release", Class: provenance.ClassApprovalRequired},
		},
	}
}

func TestRequiresApproval(t *testing.T) {
	c := testContract()

	tests := []struct {
		name        string
		commandName string
		want        bool
		wantErr     bool
	}{
		{"safe", "go-test", false, false},
		{"mutating", "gofmt-w", false, false},
		{"networked", "go-mod-download", false, false},
		{"destructive", "git-reset-hard", true, false},
		{"approval_required", "release", true, false},
		{"unknown command", "does-not-exist", false, true},
		{"empty command name", "", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RequiresApproval(c, tt.commandName)
			if (err != nil) != tt.wantErr {
				t.Fatalf("RequiresApproval(%q) error = %v, wantErr %v", tt.commandName, err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, ErrCommandNotFound) {
				t.Errorf("RequiresApproval(%q) error = %v, want errors.Is(err, ErrCommandNotFound)", tt.commandName, err)
			}
			if got != tt.want {
				t.Errorf("RequiresApproval(%q) = %v, want %v", tt.commandName, got, tt.want)
			}
			// An unknown command must never be reported as *not* requiring
			// approval alongside a nil error — that combination would be
			// the "missing approval fails open" defect this package exists
			// to prevent. The caller is responsible for treating a non-nil
			// error as fail-closed (see RequiresApproval's doc comment),
			// but this assertion guards the contract RequiresApproval
			// itself must uphold: never (false, nil) for a command that
			// isn't actually in the contract.
			if tt.wantErr && err == nil {
				t.Fatalf("RequiresApproval(%q): expected a non-nil error for an unknown command", tt.commandName)
			}
		})
	}
}

func TestRequiresApproval_EmptyContractAlwaysErrors(t *testing.T) {
	var c contract.Contract
	if _, err := RequiresApproval(c, "anything"); !errors.Is(err, ErrCommandNotFound) {
		t.Errorf("RequiresApproval on a zero-value Contract = %v, want ErrCommandNotFound", err)
	}
}
