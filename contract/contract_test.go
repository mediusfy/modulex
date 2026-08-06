package contract

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/mediusfy/modulex/provenance"
	"gopkg.in/yaml.v3"
)

// validContract returns a fully-populated Contract that Validate should
// accept, for reuse across tests that only want to perturb one field.
func validContract() Contract {
	return Contract{
		SchemaVersion: SchemaVersion,
		Projects: []Project{
			{
				Name:        "modulex",
				Path:        ".",
				ModulePath:  "github.com/mediusfy/modulex",
				Description: "core lifecycle library",
				CompositionRoots: []string{
					"examples/bootstrap",
					"examples/quickstart",
				},
			},
		},
		Instructions: InstructionPrecedence{
			Files: []InstructionFile{
				{Path: "AGENTS.md", Priority: 1, Notes: "repository-wide instructions"},
			},
			Rule: "AGENTS.md defers to agent-safety-policy.md for safety rules",
		},
		Boundaries: []Boundary{
			{
				Name:        "core-no-adapter-deps",
				Description: "core package must not import adapter packages",
				Paths:       []string{"modulex.go"},
				Rule:        "enforced by make check-consumer-boundary",
			},
		},
		Commands: []CommandDecl{
			{Name: "go-test", Command: "go test ./...", Class: provenance.ClassSafe, Reason: "read-only"},
			{Name: "gofmt-w", Command: "gofmt -s -w .", Class: provenance.ClassMutating, Reason: "rewrites files"},
			{Name: "go-mod-download", Command: "go mod download", Class: provenance.ClassNetworked, Reason: "fetches modules"},
			{Name: "git-clean-f", Command: "git clean -f", Class: provenance.ClassDestructive, Reason: "deletes untracked files"},
			{Name: "make-release", Command: "make release VERSION=vX.Y.Z", Class: provenance.ClassApprovalRequired, Reason: "tags and pushes a release"},
		},
		Verification: VerificationDecl{
			Focused: []CheckDecl{
				{Name: "gofmt-check", Command: "gofmt -s -l .", Reason: "fast format check", RequiredTool: "gofmt"},
			},
			Full: []CheckDecl{
				{Name: "build", Command: "make build", Reason: "compiles everything", RequiredTool: "go"},
				{Name: "test", Command: "make test", Reason: "runs the full suite", RequiredTool: "go"},
			},
		},
		ProtectedPaths:   []string{".github/workflows/*.yml", "go.mod"},
		GeneratedPaths:   []string{"coverage.out"},
		RequiredTools:    []string{"go", "git", "golangci-lint"},
		OptionalServices: []OptionalService{{Name: "SonarCloud", Description: "static analysis"}},
		RequiredCredentials: []string{
			"GITHUB_TOKEN",
		},
		HandoffFormat: "provenance.Envelope v1.0.0",
	}
}

func TestContract_RoundTripYAML(t *testing.T) {
	original := validContract()

	data, err := yaml.Marshal(&original)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}

	var roundTripped Contract
	if err := yaml.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	if roundTripped.SchemaVersion != original.SchemaVersion {
		t.Errorf("SchemaVersion = %q, want %q", roundTripped.SchemaVersion, original.SchemaVersion)
	}
	if len(roundTripped.Projects) != len(original.Projects) {
		t.Fatalf("Projects length = %d, want %d", len(roundTripped.Projects), len(original.Projects))
	}
	if !reflect.DeepEqual(roundTripped.Projects[0], original.Projects[0]) {
		t.Errorf("Projects[0] = %+v, want %+v", roundTripped.Projects[0], original.Projects[0])
	}
	if len(roundTripped.Commands) != len(original.Commands) {
		t.Fatalf("Commands length = %d, want %d", len(roundTripped.Commands), len(original.Commands))
	}
	for i := range original.Commands {
		if !reflect.DeepEqual(roundTripped.Commands[i], original.Commands[i]) {
			t.Errorf("Commands[%d] = %+v, want %+v", i, roundTripped.Commands[i], original.Commands[i])
		}
	}
	if len(roundTripped.Verification.Full) != len(original.Verification.Full) {
		t.Fatalf("Verification.Full length = %d, want %d", len(roundTripped.Verification.Full), len(original.Verification.Full))
	}
	if roundTripped.HandoffFormat != original.HandoffFormat {
		t.Errorf("HandoffFormat = %q, want %q", roundTripped.HandoffFormat, original.HandoffFormat)
	}
	if len(roundTripped.RequiredCredentials) != len(original.RequiredCredentials) {
		t.Fatalf("RequiredCredentials length = %d, want %d", len(roundTripped.RequiredCredentials), len(original.RequiredCredentials))
	}
}

func TestContract_Validate_ValidContractPasses(t *testing.T) {
	c := validContract()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestContract_Validate_MissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Contract)
		wantErr string
	}{
		{
			name: "missing schema version",
			mutate: func(c *Contract) {
				c.SchemaVersion = ""
			},
			wantErr: "schema_version is required",
		},
		{
			name: "zero projects",
			mutate: func(c *Contract) {
				c.Projects = nil
			},
			wantErr: "projects: at least one project is required",
		},
		{
			name: "project with empty name",
			mutate: func(c *Contract) {
				c.Projects[0].Name = ""
			},
			wantErr: "projects[0]: name is required",
		},
		{
			name: "project with empty path",
			mutate: func(c *Contract) {
				c.Projects[0].Path = ""
			},
			wantErr: "path is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validContract()
			tt.mutate(&c)

			err := c.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestContract_Validate_UnknownCommandClass(t *testing.T) {
	c := validContract()
	c.Commands = []CommandDecl{
		{Name: "mystery-command", Command: "do-something-risky", Class: provenance.CommandClass("yolo")},
	}

	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for unknown command class")
	}

	msg := err.Error()
	if !strings.Contains(msg, "commands[0]") {
		t.Errorf("error %q does not name the offending command index", msg)
	}
	if !strings.Contains(msg, `"mystery-command"`) {
		t.Errorf("error %q does not name the offending command", msg)
	}
	if !strings.Contains(msg, `"yolo"`) {
		t.Errorf("error %q does not name the bad class value", msg)
	}
	if !strings.Contains(msg, "safe, mutating, networked, destructive, approval_required") {
		t.Errorf("error %q does not enumerate the valid classes", msg)
	}
}

func TestContract_Validate_BadProtectedPathGlob(t *testing.T) {
	c := validContract()
	c.ProtectedPaths = []string{"go.mod", ".github/workflows/[.yml"}

	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for a malformed protected_paths glob")
	}

	msg := err.Error()
	if !strings.Contains(msg, "protected_paths[1]") {
		t.Errorf("error %q does not name the offending index", msg)
	}
	if !strings.Contains(msg, `".github/workflows/[.yml"`) {
		t.Errorf("error %q does not name the offending pattern", msg)
	}
}

func TestContract_Validate_SecretRejection(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Contract)
		wantErr string
	}{
		{
			name: "github token in instruction file notes",
			mutate: func(c *Contract) {
				c.Instructions.Files[0].Notes = "auth with ghp_1234567890abcdefghij1234567890abcdEF"
			},
			wantErr: "instructions.files[0].notes: contains a value that looks like a secret (GitHub token)",
		},
		{
			name: "generic key=value assignment in command reason",
			mutate: func(c *Contract) {
				c.Commands[0].Reason = "requires api_key=sk_live_abcdef123456 to run"
			},
			wantErr: "commands[0].reason: contains a value that looks like a secret (generic key/token/password/secret assignment)",
		},
		{
			name: "PEM private key block in project description",
			mutate: func(c *Contract) {
				c.Projects[0].Description = "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----"
			},
			wantErr: "projects[0].description: contains a value that looks like a secret (PEM private key)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validContract()
			tt.mutate(&c)

			err := c.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestExampleContract_IsValid(t *testing.T) {
	data, err := os.ReadFile("testdata/modulex.agent.example.yaml")
	if err != nil {
		t.Fatalf("reading example contract: %v", err)
	}

	var c Contract
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	if c.SchemaVersion != SchemaVersion {
		t.Errorf("example SchemaVersion = %q, want %q (keep the example fixture in sync with the schema)", c.SchemaVersion, SchemaVersion)
	}
	if len(c.Projects) == 0 || c.Projects[0].ModulePath != "github.com/mediusfy/modulex" {
		t.Errorf("example contract does not describe this repository's module")
	}
}

// TestRootContract_MatchesExample guards against
// testdata/modulex.agent.example.yaml (loaded by TestExampleContract_IsValid
// above, and by agentdocs' own tests) silently drifting from the real,
// canonical modulex.agent.yaml checked in at the repository root: the two
// files intentionally carry different header comments (see each file's own
// comment), so this compares parsed Contract values, not raw file bytes.
func TestRootContract_MatchesExample(t *testing.T) {
	rootData, err := os.ReadFile("../modulex.agent.yaml")
	if err != nil {
		t.Fatalf("reading root modulex.agent.yaml: %v", err)
	}
	exampleData, err := os.ReadFile("testdata/modulex.agent.example.yaml")
	if err != nil {
		t.Fatalf("reading testdata/modulex.agent.example.yaml: %v", err)
	}

	var root, example Contract
	if err := yaml.Unmarshal(rootData, &root); err != nil {
		t.Fatalf("yaml.Unmarshal(root): %v", err)
	}
	if err := yaml.Unmarshal(exampleData, &example); err != nil {
		t.Fatalf("yaml.Unmarshal(example): %v", err)
	}

	if !reflect.DeepEqual(root, example) {
		t.Errorf("modulex.agent.yaml and contract/testdata/modulex.agent.example.yaml have drifted apart; " +
			"edit the root file and copy the change into testdata, or vice versa, so both stay identical")
	}
}
