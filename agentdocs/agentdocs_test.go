package agentdocs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/mediusfy/modulex/contract"
)

// allTargets lists every known Target, for table-driven tests that must
// cover all four.
var allTargets = []Target{TargetAGENTS, TargetCLAUDE, TargetKimi, TargetCodex}

// targetRow is one table row naming a single Target.
type targetRow struct {
	name   string
	target Target
}

// targetRows turns targets into table rows named after each target.
func targetRows(targets []Target) []targetRow {
	rows := make([]targetRow, len(targets))
	for i, target := range targets {
		rows[i] = targetRow{name: string(target), target: target}
	}
	return rows
}

// loadExampleContract loads and unmarshals the shared example contract
// fixture used by contract's own tests (contract/testdata/modulex.agent.example.yaml),
// so this package's tests exercise the exact same worked example rather
// than a hand-rolled duplicate that could drift from it.
func loadExampleContract(t *testing.T) contract.Contract {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "contract", "testdata", "modulex.agent.example.yaml"))
	if err != nil {
		t.Fatalf("os.ReadFile: %v", err)
	}

	var c contract.Contract
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("fixture contract failed Validate(): %v", err)
	}
	return c
}

// assertGeneratedContent checks the content every target's output must carry
// from the contract it was generated from.
func assertGeneratedContent(t *testing.T, c contract.Contract, target Target, out string) {
	t.Helper()

	if strings.TrimSpace(out) == "" {
		t.Fatalf("Generate(%s) returned empty output", target)
	}
	if !strings.Contains(out, c.SchemaVersion) {
		t.Errorf("Generate(%s) output missing source schema version %q", target, c.SchemaVersion)
	}

	if len(c.Commands) == 0 {
		t.Fatalf("fixture has no commands to check against")
	}
	if wantCommand := c.Commands[0].Command; !strings.Contains(out, wantCommand) {
		t.Errorf("Generate(%s) output missing command matrix entry %q", target, wantCommand)
	}

	if len(c.ProtectedPaths) == 0 {
		t.Fatalf("fixture has no protected paths to check against")
	}
	if wantPath := c.ProtectedPaths[0]; !strings.Contains(out, wantPath) {
		t.Errorf("Generate(%s) output missing protected path %q", target, wantPath)
	}

	if c.HandoffFormat != "" && !strings.Contains(out, c.HandoffFormat) {
		t.Errorf("Generate(%s) output missing handoff format %q", target, c.HandoffFormat)
	}
}

func TestGenerate_AllTargets(t *testing.T) {
	c := loadExampleContract(t)
	tests := targetRows(allTargets)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Generate(c, tt.target)
			if err != nil {
				t.Fatalf("Generate(%s): %v", tt.target, err)
			}
			assertGeneratedContent(t, c, tt.target, out)
		})
	}
}

func TestGenerate_Deterministic(t *testing.T) {
	c := loadExampleContract(t)
	tests := targetRows([]Target{TargetAGENTS, TargetCLAUDE})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, err := Generate(c, tt.target)
			if err != nil {
				t.Fatalf("Generate(%s) #1: %v", tt.target, err)
			}
			second, err := Generate(c, tt.target)
			if err != nil {
				t.Fatalf("Generate(%s) #2: %v", tt.target, err)
			}
			if first != second {
				t.Errorf("Generate(%s) is not deterministic:\n--- first ---\n%s\n--- second ---\n%s", tt.target, first, second)
			}
		})
	}
}

func TestGenerate_DoesNotMutateCaller(t *testing.T) {
	// Generate sorts a copy of every unordered contract slice, never the
	// caller's own slice (see the package doc comment's "Deterministic
	// regeneration" section) — this regresses that guarantee for Commands.
	tests := targetRows([]Target{TargetAGENTS})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := loadExampleContract(t)
			before := append([]contract.CommandDecl(nil), c.Commands...)

			if _, err := Generate(c, tt.target); err != nil {
				t.Fatalf("Generate: %v", err)
			}

			if len(c.Commands) != len(before) {
				t.Fatalf("Commands length changed: got %d, want %d", len(c.Commands), len(before))
			}
			for i := range before {
				if c.Commands[i] != before[i] {
					t.Errorf("Commands[%d] mutated by Generate: got %+v, want %+v", i, c.Commands[i], before[i])
				}
			}
		})
	}
}

func TestGenerate_UnknownTarget(t *testing.T) {
	tests := targetRows([]Target{Target("bogus")})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := loadExampleContract(t)

			out, err := Generate(c, tt.target)
			if err == nil {
				t.Fatalf("Generate(%s) returned nil error, want error", tt.target)
			}
			if out != "" {
				t.Errorf("Generate(%s) output = %q, want empty string", tt.target, out)
			}
		})
	}
}

// framingText is each target's own framing text, which must appear only in
// its own output.
var framingText = map[Target]string{
	TargetAGENTS: "AGENTS.md — Repository Agent Instructions",
	TargetCLAUDE: "CLAUDE.md — Claude Code Instructions",
	TargetKimi:   "Kimi Code CLI — Project Guidance",
	TargetCodex:  "Codex / Repository-Aware Agent Instructions",
}

func TestGenerate_TargetsAreDistinct(t *testing.T) {
	c := loadExampleContract(t)

	outputs := make(map[Target]string, len(allTargets))
	for _, target := range allTargets {
		out, err := Generate(c, target)
		if err != nil {
			t.Fatalf("Generate(%s): %v", target, err)
		}
		outputs[target] = out
	}

	tests := targetRows(allTargets)
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Every pair of targets must produce different output —
			// otherwise the "one contract generates consistent [but not
			// identical] provider-specific guidance" acceptance criterion
			// isn't met.
			for _, b := range allTargets[i+1:] {
				if outputs[tt.target] == outputs[b] {
					t.Errorf("Generate(%s) and Generate(%s) produced identical output", tt.target, b)
				}
			}

			// This target's own framing text must appear only in its own
			// output.
			text := framingText[tt.target]
			for _, other := range allTargets {
				contains := strings.Contains(outputs[other], text)
				if other == tt.target && !contains {
					t.Errorf("Generate(%s) output missing its own framing text %q", other, text)
				}
				if other != tt.target && contains {
					t.Errorf("Generate(%s) output unexpectedly contains %s's framing text %q", other, tt.target, text)
				}
			}
		})
	}
}

func TestDrift(t *testing.T) {
	tests := []struct {
		name        string
		target      Target
		useCurrent  bool
		content     string
		wantDrifted bool
	}{
		{name: "current content has no drift", target: TargetAGENTS, useCurrent: true, wantDrifted: false},
		{name: "stale content drifts", target: TargetAGENTS, content: "stale content", wantDrifted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := loadExampleContract(t)

			content := tt.content
			if tt.useCurrent {
				current, err := Generate(c, tt.target)
				if err != nil {
					t.Fatalf("Generate: %v", err)
				}
				content = current
			}

			drifted, err := Drift(c, tt.target, content)
			if err != nil {
				t.Fatalf("Drift: %v", err)
			}
			if drifted != tt.wantDrifted {
				t.Errorf("Drift(%s) = %v, want %v", tt.name, drifted, tt.wantDrifted)
			}
		})
	}
}

func TestDrift_UnknownTarget(t *testing.T) {
	tests := []struct {
		name    string
		target  Target
		content string
	}{
		{name: "bogus target", target: Target("bogus"), content: "anything"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := loadExampleContract(t)

			if _, err := Drift(c, tt.target, tt.content); err == nil {
				t.Fatalf("Drift(%s) returned nil error, want error", tt.target)
			}
		})
	}
}

// goldenPath returns the path to target's golden fixture file.
func goldenPath(target Target) string {
	return filepath.Join("testdata", "golden", string(target)+".golden")
}

func TestGenerate_Golden(t *testing.T) {
	c := loadExampleContract(t)
	tests := targetRows(allTargets)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Generate(c, tt.target)
			if err != nil {
				t.Fatalf("Generate(%s): %v", tt.target, err)
			}

			want, err := os.ReadFile(goldenPath(tt.target))
			if err != nil {
				t.Fatalf("os.ReadFile(%s): %v (run the generator in the package doc "+
					"comment's usage example to (re)create golden files after an "+
					"intentional template change)", goldenPath(tt.target), err)
			}

			if got != string(want) {
				t.Errorf("Generate(%s) does not match %s\n--- got ---\n%s\n--- want ---\n%s",
					tt.target, goldenPath(tt.target), got, string(want))
			}
		})
	}
}
