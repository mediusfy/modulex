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
	tests := []struct {
		name    string
		changed []string
	}{
		{name: "no changed files", changed: nil},
		{name: "go package file", changed: []string{"httpx/httpx.go"}},
		{name: "workflow file", changed: []string{".github/workflows/ci.yml"}},
		{name: "unmapped docs file", changed: []string{"docs/some-unmapped-file.md"}},
		{name: "go.mod", changed: []string{"go.mod"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanFor(tt.changed)
			if len(plan.FullGates) == 0 {
				t.Fatalf("PlanFor(%v).FullGates is empty; full gates must always be present", tt.changed)
			}
			if len(plan.FullGates) != len(FullGates) {
				t.Fatalf("PlanFor(%v).FullGates has %d entries, want %d (the complete fixed list)", tt.changed, len(plan.FullGates), len(FullGates))
			}
			for _, g := range plan.FullGates {
				if g.Category != provenance.VerificationFull {
					t.Errorf("PlanFor(%v).FullGates entry %q has Category %q, want %q", tt.changed, g.Name, g.Category, provenance.VerificationFull)
				}
			}
		})
	}
}

// assertFocusedChecks verifies plan.FocusedChecks against the expectations of
// a TestPlanFor_ChangedFileCategories row.
func assertFocusedChecks(t *testing.T, changedFiles []string, plan Plan, wantEmpty bool, wantCommands []string, wantFallback bool) {
	t.Helper()
	if wantEmpty {
		if len(plan.FocusedChecks) != 0 {
			t.Fatalf("FocusedChecks = %+v, want empty", plan.FocusedChecks)
		}
		return
	}

	if len(plan.FocusedChecks) == 0 {
		t.Fatalf("FocusedChecks is empty for changedFiles %v; an unmapped or mixed change must never produce zero recommendations", changedFiles)
	}

	for _, want := range wantCommands {
		if !containsCommand(plan.FocusedChecks, want) {
			t.Errorf("FocusedChecks missing command %q; got %+v", want, plan.FocusedChecks)
		}
	}

	if wantFallback {
		assertFallbackChecks(t, plan)
	}
}

// assertFallbackChecks verifies plan.FocusedChecks is the full gate set
// recommended as a focused-check fallback.
func assertFallbackChecks(t *testing.T, plan Plan) {
	t.Helper()
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

			assertFocusedChecks(t, tc.changedFiles, plan, tc.wantEmpty, tc.wantCommands, tc.wantFallback)
		})
	}
}

func TestPlanFor_DeterministicRegardlessOfInputOrder(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "PlanFor DeterministicRegardlessOfInputOrder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
		})
	}
}

func TestPlanFor_NoDuplicateChecks(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "PlanFor NoDuplicateChecks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
		})
	}
}

// TestPlanFor_RejectsShellMetacharactersInPath is a regression test for a
// command-injection vulnerability: a changed-file path containing shell
// metacharacters (e.g. from an untrusted diff, such as a maliciously named
// file in an external contribution) must never end up embedded in a
// CheckSpec.Command, since Run executes commands via "sh -c". Every
// resulting Command must come verbatim from the fixed FullGates list (the
// fallback path), never contain the raw injected path.
func TestPlanFor_RejectsShellMetacharactersInPath(t *testing.T) {
	injectionPaths := []string{
		"$(touch pwned)/evil.go",
		"scripts/check-x;touch pwned;.sh",
		"httpx/`touch pwned`.go",
		"examples/foo && touch pwned/bar.go",
		"go.mod; touch pwned",
		"../../../etc/passwd",
	}

	for _, p := range injectionPaths {
		t.Run(p, func(t *testing.T) {
			plan := PlanFor([]string{p})

			if len(plan.FocusedChecks) == 0 {
				t.Fatalf("PlanFor(%q): expected fallback-to-full-gates checks, got none", p)
			}

			fullGateCommands := make(map[string]bool, len(FullGates))
			for _, g := range FullGates {
				fullGateCommands[g.Command] = true
			}

			for _, c := range plan.FocusedChecks {
				if strings.Contains(c.Command, "touch") || strings.Contains(c.Command, "pwned") {
					t.Fatalf("PlanFor(%q): Command %q embeds the injected path; want a fixed FullGates command", p, c.Command)
				}
				if !fullGateCommands[c.Command] {
					t.Fatalf("PlanFor(%q): Command %q is not one of the fixed FullGates commands", p, c.Command)
				}
			}
		})
	}
}

func TestIsPathSafeForCommand(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "package file", path: "httpx/httpx.go", want: true},
		{name: "root file", path: "modulex.go", want: true},
		{name: "nested example file", path: "examples/deployment/module.go", want: true},
		{name: "script", path: "scripts/check-changelog.sh", want: true},
		{name: "go.mod", path: "go.mod", want: true},
		{name: "changelog", path: "CHANGELOG.md", want: true},
		{name: "docs file", path: "docs/planning/agent-verification-guide.md", want: true},
		{name: "empty", path: "", want: false},
		{name: "command substitution", path: "$(touch pwned)/evil.go", want: false},
		{name: "semicolon", path: "scripts/check-x;touch pwned;.sh", want: false},
		{name: "backticks", path: "httpx/`touch pwned`.go", want: false},
		{name: "and-and", path: "a && b.go", want: false},
		{name: "pipe", path: "a | b.go", want: false},
		{name: "newline", path: "a\nb.go", want: false},
		{name: "parent traversal", path: "../../../etc/passwd", want: false},
		{name: "embedded dotdot", path: "foo/../bar.go", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPathSafeForCommand(tt.path); got != tt.want {
				t.Errorf("isPathSafeForCommand(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestPlanFor_NestedModulePathsUseModuleLocalCommands(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantDir string
	}{
		{name: "tools agentcli go file", path: "tools/agentcli/agentcli.go", wantDir: "tools/agentcli"},
		{name: "tools mcpserver go file", path: "tools/mcpserver/review.go", wantDir: "tools/mcpserver"},
		{name: "external consumer go file", path: "examples/external-consumer/main.go", wantDir: "examples/external-consumer"},
		// Non-.go files in a nested module identify that module too: its
		// go.mod/go.sum or scripts must not fall back to root-only full
		// gates that never exercise the module.
		{name: "nested go.mod", path: "tools/mcpserver/go.mod", wantDir: "tools/mcpserver"},
		{name: "nested go.sum", path: "examples/external-consumer/go.sum", wantDir: "examples/external-consumer"},
		// services/* children follow the same nested-module convention as
		// tools/* (regression: services/prreview initially planned as a
		// root-module ./services/... pattern that matched no packages).
		{name: "services go file", path: "services/prreview/worker/worker.go", wantDir: "services/prreview"},
		{name: "services go.mod", path: "services/prreview/go.mod", wantDir: "services/prreview"},
		{name: "services Dockerfile", path: "services/prreview/Dockerfile", wantDir: "services/prreview"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := PlanFor([]string{tc.path})
			wantCmd := "go -C " + tc.wantDir + " test ./..."
			var found bool
			for _, c := range plan.FocusedChecks {
				if c.Command == wantCmd {
					found = true
				}
				if c.Command == "go test ./tools/..." || c.Command == "go test ./examples/external-consumer/..." {
					t.Errorf("%s: focused check %q uses a root-module package pattern that matches no packages (nested module)", tc.path, c.Command)
				}
			}
			if !found {
				var got []string
				for _, c := range plan.FocusedChecks {
					got = append(got, c.Command)
				}
				t.Errorf("%s: no focused check with command %q; got %v", tc.path, wantCmd, got)
			}
		})
	}
}

// TestPlanFor_ToolsFileOutsideNestedModuleFallsBackToFullGates: a .go file
// directly under tools/ belongs to no root-module package (every tools/*
// child is its own module), so no root-module `./tools/...` pattern — which
// would match no packages and always fail — may be recommended; the full
// gates are the correct fallback.
func TestPlanFor_ToolsFileOutsideNestedModuleFallsBackToFullGates(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "PlanFor ToolsFileOutsideNestedModuleFallsBackToFullGates"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanFor([]string{"tools/gen.go"})
			if len(plan.FocusedChecks) == 0 {
				t.Fatal("want the full gate set as focused fallback, got no checks")
			}
			for _, c := range plan.FocusedChecks {
				if strings.Contains(c.Command, "./tools/...") {
					t.Errorf("focused check %q uses a root-module ./tools/... pattern that matches no packages", c.Command)
				}
			}
		})
	}
}
