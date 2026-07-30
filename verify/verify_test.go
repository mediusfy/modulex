package verify

import (
	"strings"
	"testing"

	"github.com/mediusfy/modulex/provenance"
)

// containsCommand reports whether checks contains a CheckSpec whose Command
// equals want.
func containsCommand(checks []CheckSpec, want string) bool {
	for _, c := range checks {
		if c.Command == want {
			return true
		}
	}
	return false
}

func TestPlanFor_FullGatesAlwaysPresent(t *testing.T) {
	cases := [][]string{
		nil,
		{"httpx/httpx.go"},
		{".github/workflows/ci.yml"},
		{"docs/some-unmapped-file.md"},
		{"go.mod"},
	}
	for _, changed := range cases {
		plan := PlanFor(changed)
		if len(plan.FullGates) == 0 {
			t.Fatalf("PlanFor(%v).FullGates is empty; full gates must always be present", changed)
		}
		if len(plan.FullGates) != len(FullGates) {
			t.Fatalf("PlanFor(%v).FullGates has %d entries, want %d (the complete fixed list)", changed, len(plan.FullGates), len(FullGates))
		}
		for _, g := range plan.FullGates {
			if g.Category != provenance.VerificationFull {
				t.Errorf("PlanFor(%v).FullGates entry %q has Category %q, want %q", changed, g.Name, g.Category, provenance.VerificationFull)
			}
		}
	}
}

func TestPlanFor_ChangedFileCategories(t *testing.T) {
	tests := []struct {
		name         string
		changedFiles []string
		// wantCommands are Commands that must be present in FocusedChecks.
		wantCommands []string
		// wantEmpty asserts FocusedChecks has zero entries.
		wantEmpty bool
		// wantFallback asserts FocusedChecks equals the full gate set
		// (as a focused-check fallback), one-for-one by Command.
		wantFallback bool
	}{
		{
			name:         "go package directory change",
			changedFiles: []string{"httpx/httpx.go"},
			wantCommands: []string{"go test ./httpx/...", "go vet ./httpx/..."},
		},
		{
			name:         "root package file change",
			changedFiles: []string{"modulex.go"},
			wantCommands: []string{"go test .", "go vet ."},
		},
		{
			name:         "examples change with specific example",
			changedFiles: []string{"examples/quickstart/main.go"},
			wantCommands: []string{"go build ./examples/...", "go test ./examples/quickstart/..."},
		},
		{
			name:         "examples change with no specific example",
			changedFiles: []string{"examples/README.md"},
			wantCommands: []string{"go build ./examples/..."},
		},
		{
			name:         "check script change",
			changedFiles: []string{"scripts/check-changelog.sh"},
			wantCommands: []string{"./scripts/check-changelog.sh"},
		},
		{
			name:         "go.mod change",
			changedFiles: []string{"go.mod"},
			wantCommands: []string{"make check-api-compat", "go mod verify"},
		},
		{
			name:         "go.sum change",
			changedFiles: []string{"go.sum"},
			wantCommands: []string{"make check-api-compat", "go mod verify"},
		},
		{
			name:         "changelog change",
			changedFiles: []string{"CHANGELOG.md"},
			wantCommands: []string{"make check-changelog"},
		},
		{
			name:         "github workflow change has no focused shortcut",
			changedFiles: []string{".github/workflows/ci.yml"},
			wantEmpty:    true,
		},
		{
			name:         "unmapped path falls back to full gate set",
			changedFiles: []string{"docs/some-random-file.md"},
			wantFallback: true,
		},
		{
			name:         "ticket example: mixed mapped and unmapped",
			changedFiles: []string{"httpx/httpx.go", "modulex.go", "docs/README.md"},
			wantCommands: []string{"go test ./httpx/...", "go vet ./httpx/...", "go test .", "go vet ."},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := PlanFor(tc.changedFiles)

			if tc.wantEmpty {
				if len(plan.FocusedChecks) != 0 {
					t.Fatalf("FocusedChecks = %+v, want empty", plan.FocusedChecks)
				}
				return
			}

			if len(plan.FocusedChecks) == 0 {
				t.Fatalf("FocusedChecks is empty for changedFiles %v; an unmapped or mixed change must never produce zero recommendations", tc.changedFiles)
			}

			for _, want := range tc.wantCommands {
				if !containsCommand(plan.FocusedChecks, want) {
					t.Errorf("FocusedChecks missing command %q; got %+v", want, plan.FocusedChecks)
				}
			}

			if tc.wantFallback {
				if len(plan.FocusedChecks) != len(FullGates) {
					t.Fatalf("fallback FocusedChecks has %d entries, want %d (one per full gate)", len(plan.FocusedChecks), len(FullGates))
				}
				for _, c := range plan.FocusedChecks {
					if !containsCommand(FullGates, c.Command) {
						t.Errorf("fallback FocusedChecks entry %q does not correspond to any FullGates command", c.Command)
					}
					if !strings.Contains(c.Reason, "no focused-check rule matched") {
						t.Errorf("fallback FocusedChecks entry %q has Reason %q, want it to explain the fallback", c.Name, c.Reason)
					}
				}
			}
		})
	}
}

func TestPlanFor_DeterministicRegardlessOfInputOrder(t *testing.T) {
	a := PlanFor([]string{"httpx/httpx.go", "otel/otel.go"})
	b := PlanFor([]string{"otel/otel.go", "httpx/httpx.go"})

	if len(a.FocusedChecks) != len(b.FocusedChecks) {
		t.Fatalf("FocusedChecks length differs by input order: %d vs %d", len(a.FocusedChecks), len(b.FocusedChecks))
	}
	for i := range a.FocusedChecks {
		if a.FocusedChecks[i].Command != b.FocusedChecks[i].Command {
			t.Errorf("FocusedChecks[%d].Command = %q, want %q (order should not depend on changedFiles order)", i, b.FocusedChecks[i].Command, a.FocusedChecks[i].Command)
		}
	}
}

func TestPlanFor_NoDuplicateChecks(t *testing.T) {
	// httpx.go and another file in the same package must not produce
	// duplicate CheckSpecs for the same package.
	plan := PlanFor([]string{"httpx/httpx.go", "httpx/handlers.go"})

	seen := make(map[string]bool)
	for _, c := range plan.FocusedChecks {
		key := c.Name + "|" + c.Command
		if seen[key] {
			t.Fatalf("duplicate FocusedChecks entry: %s / %s", c.Name, c.Command)
		}
		seen[key] = true
	}
}
