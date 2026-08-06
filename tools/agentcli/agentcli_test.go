package agentcli

import (
	"os"
	"testing"
)

func TestLoadContract_RepositoryRoot(t *testing.T) {
	c, err := LoadContract("../..")
	if err != nil {
		t.Fatalf("LoadContract(\"../..\") = %v, want nil", err)
	}
	if len(c.Projects) == 0 || c.Projects[0].ModulePath != "github.com/mediusfy/modulex" {
		t.Errorf("loaded contract does not describe this repository's module")
	}
}

// TestGeneratedFiles_MatchCheckedIn is the CI-enforceable form of
// agentdocs.Drift: it regenerates AGENTS.md and CLAUDE.md from the
// repository's real modulex.agent.yaml and fails if either checked-in file
// doesn't match byte-for-byte. A failure here means modulex.agent.yaml was
// edited without re-running `go run ./tools/agentcli/cmd/modulex agent
// generate -root ../..` (from tools/agentcli) afterward.
func TestGeneratedFiles_MatchCheckedIn(t *testing.T) {
	c, err := LoadContract("../..")
	if err != nil {
		t.Fatalf("LoadContract(\"../..\") = %v", err)
	}

	generated, err := GeneratedFiles(c)
	if err != nil {
		t.Fatalf("GeneratedFiles() = %v", err)
	}

	for _, f := range OutputFiles {
		checkedIn, err := os.ReadFile("../../" + f.name)
		if err != nil {
			t.Fatalf("reading ../../%s: %v", f.name, err)
		}
		if string(checkedIn) != generated[f.name] {
			t.Errorf("../../%s is stale relative to modulex.agent.yaml; "+
				"regenerate with `go run ./cmd/modulex agent generate -root ../..` from tools/agentcli", f.name)
		}
	}
}
