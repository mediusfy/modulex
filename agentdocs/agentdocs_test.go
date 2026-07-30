package agentdocs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mediusfy/modulex/contract"
	"gopkg.in/yaml.v3"
)

// allTargets lists every known Target, for table-driven tests that must
// cover all four.
var allTargets = []Target{TargetAGENTS, TargetCLAUDE, TargetKimi, TargetCodex}

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

func TestGenerate_AllTargets(t *testing.T) {
	c := loadExampleContract(t)

	for _, target := range allTargets {
		t.Run(string(target), func(t *testing.T) {
			out, err := Generate(c, target)
			if err != nil {
				t.Fatalf("Generate(%s): %v", target, err)
			}
			if strings.TrimSpace(out) == "" {
				t.Fatalf("Generate(%s) returned empty output", target)
			}

			if !strings.Contains(out, c.SchemaVersion) {
				t.Errorf("Generate(%s) output missing source schema version %q", target, c.SchemaVersion)
			}

			if len(c.Commands) == 0 {
				t.Fatalf("fixture has no commands to check against")
			}
			wantCommand := c.Commands[0].Command
			if !strings.Contains(out, wantCommand) {
				t.Errorf("Generate(%s) output missing command matrix entry %q", target, wantCommand)
			}

			if len(c.ProtectedPaths) == 0 {
				t.Fatalf("fixture has no protected paths to check against")
			}
			wantPath := c.ProtectedPaths[0]
			if !strings.Contains(out, wantPath) {
				t.Errorf("Generate(%s) output missing protected path %q", target, wantPath)
			}

			if c.HandoffFormat != "" && !strings.Contains(out, c.HandoffFormat) {
				t.Errorf("Generate(%s) output missing handoff format %q", target, c.HandoffFormat)
			}
		})
	}
}

func TestGenerate_Deterministic(t *testing.T) {
	c := loadExampleContract(t)

	for _, target := range []Target{TargetAGENTS, TargetCLAUDE} {
		t.Run(string(target), func(t *testing.T) {
			first, err := Generate(c, target)
			if err != nil {
				t.Fatalf("Generate(%s) #1: %v", target, err)
			}
			second, err := Generate(c, target)
			if err != nil {
				t.Fatalf("Generate(%s) #2: %v", target, err)
			}
			if first != second {
				t.Errorf("Generate(%s) is not deterministic:\n--- first ---\n%s\n--- second ---\n%s", target, first, second)
			}
		})
	}
}

func TestGenerate_DoesNotMutateCaller(t *testing.T) {
	// Generate sorts a copy of every unordered contract slice, never the
	// caller's own slice (see the package doc comment's "Deterministic
	// regeneration" section) — this regresses that guarantee for Commands.
	c := loadExampleContract(t)
	before := append([]contract.CommandDecl(nil), c.Commands...)

	if _, err := Generate(c, TargetAGENTS); err != nil {
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
}

func TestGenerate_UnknownTarget(t *testing.T) {
	c := loadExampleContract(t)

	out, err := Generate(c, Target("bogus"))
	if err == nil {
		t.Fatalf("Generate(bogus) returned nil error, want error")
	}
	if out != "" {
		t.Errorf("Generate(bogus) output = %q, want empty string", out)
	}
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

	// Every pair of targets must produce different output — otherwise
	// the "one contract generates consistent [but not identical]
	// provider-specific guidance" acceptance criterion isn't met.
	for i, a := range allTargets {
		for _, b := range allTargets[i+1:] {
			if outputs[a] == outputs[b] {
				t.Errorf("Generate(%s) and Generate(%s) produced identical output", a, b)
			}
		}
	}

	// Each target's own framing text must appear only in its own output.
	framingText := map[Target]string{
		TargetAGENTS: "AGENTS.md — Repository Agent Instructions",
		TargetCLAUDE: "CLAUDE.md — Claude Code Instructions",
		TargetKimi:   "Kimi Code CLI — Project Guidance",
		TargetCodex:  "Codex / Repository-Aware Agent Instructions",
	}
	for _, owner := range allTargets {
		text := framingText[owner]
		for _, other := range allTargets {
			contains := strings.Contains(outputs[other], text)
			if other == owner && !contains {
				t.Errorf("Generate(%s) output missing its own framing text %q", other, text)
			}
			if other != owner && contains {
				t.Errorf("Generate(%s) output unexpectedly contains %s's framing text %q", other, owner, text)
			}
		}
	}
}

func TestDrift(t *testing.T) {
	c := loadExampleContract(t)

	current, err := Generate(c, TargetAGENTS)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	drifted, err := Drift(c, TargetAGENTS, current)
	if err != nil {
		t.Fatalf("Drift(current): %v", err)
	}
	if drifted {
		t.Errorf("Drift(current content) = true, want false")
	}

	drifted, err = Drift(c, TargetAGENTS, "stale content")
	if err != nil {
		t.Fatalf("Drift(stale): %v", err)
	}
	if !drifted {
		t.Errorf("Drift(stale content) = false, want true")
	}
}

func TestDrift_UnknownTarget(t *testing.T) {
	c := loadExampleContract(t)

	if _, err := Drift(c, Target("bogus"), "anything"); err == nil {
		t.Fatalf("Drift(bogus) returned nil error, want error")
	}
}

// goldenPath returns the path to target's golden fixture file.
func goldenPath(target Target) string {
	return filepath.Join("testdata", "golden", string(target)+".golden")
}

func TestGenerate_Golden(t *testing.T) {
	c := loadExampleContract(t)

	for _, target := range allTargets {
		t.Run(string(target), func(t *testing.T) {
			got, err := Generate(c, target)
			if err != nil {
				t.Fatalf("Generate(%s): %v", target, err)
			}

			want, err := os.ReadFile(goldenPath(target))
			if err != nil {
				t.Fatalf("os.ReadFile(%s): %v (run the generator in the package doc "+
					"comment's usage example to (re)create golden files after an "+
					"intentional template change)", goldenPath(target), err)
			}

			if got != string(want) {
				t.Errorf("Generate(%s) does not match %s\n--- got ---\n%s\n--- want ---\n%s",
					target, goldenPath(target), got, string(want))
			}
		})
	}
}
