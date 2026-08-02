package provenance

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func intPtr(v int) *int { return &v }

// validEnvelope returns a fully-populated envelope with no secret-shaped
// values, suitable as a baseline for tests that mutate a copy of it.
func validEnvelope() Envelope {
	return Envelope{
		SchemaVersion: SchemaVersion,
		Repository: RepoState{
			Path:   "/repo/modulex",
			Branch: "MOD-66-provenance-handoff-json",
			Commit: "abc1234def5678",
			Dirty:  false,
		},
		Agent: AgentInfo{
			Name:        "claude",
			Version:     "sonnet-5",
			Tool:        "claude-code",
			ToolVersion: "1.2.3",
			CLIVersion:  "0.1.0",
		},
		Changes: []FileChange{
			{Path: "provenance/provenance.go", Type: ChangeAdded, Hash: "sha256:deadbeef"},
		},
		Commands: []CommandResult{
			{
				Name:           "make test",
				Args:           []string{"test"},
				Classification: ClassSafe,
				Status:         StatusPass,
				ExitCode:       intPtr(0),
				Duration:       2 * time.Second,
				Output:         "ok",
			},
			{
				Name:           "make vuln",
				Classification: ClassNetworked,
				Status:         StatusUnavailable,
				Reason:         "no network access in this sandbox",
			},
		},
		Verification: []VerificationResult{
			{
				Name:     "gofmt",
				Category: VerificationFocused,
				Status:   StatusPass,
				Duration: 100 * time.Millisecond,
			},
			{
				Name:     "secret-scan",
				Category: VerificationSecretScan,
				Status:   StatusSkipped,
				Reason:   "no external scanner configured for this environment",
			},
		},
		Approvals: []Approval{
			{
				Action:     "push",
				ApprovedBy: "drew@jocham.io",
				ApprovedAt: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC),
				Notes:      "reviewed diff manually",
			},
		},
		Rollback: &RollbackStatus{
			Available: true,
			Applied:   false,
			Method:    "git revert <sha>",
		},
		CreatedAt: time.Date(2026, 7, 29, 12, 5, 0, 0, time.UTC),
	}
}

func TestValidate_FullyPopulatedEnvelope(t *testing.T) {
	env := validEnvelope()
	if err := env.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestValidate_MissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Envelope)
		wantErr string
	}{
		{
			name:    "missing schema version",
			mutate:  func(e *Envelope) { e.SchemaVersion = "" },
			wantErr: "schema_version is required",
		},
		{
			name:    "missing repository path",
			mutate:  func(e *Envelope) { e.Repository.Path = "" },
			wantErr: "repository.path is required",
		},
		{
			name:    "missing repository commit",
			mutate:  func(e *Envelope) { e.Repository.Commit = "" },
			wantErr: "repository.commit is required",
		},
		{
			name:    "missing created_at",
			mutate:  func(e *Envelope) { e.CreatedAt = time.Time{} },
			wantErr: "created_at is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnvelope()
			tt.mutate(&env)
			err := env.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %q, want error containing %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidate_RejectsUnredactedSecrets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Envelope)
	}{
		{
			name: "AWS secret env var",
			mutate: func(e *Envelope) {
				e.Commands[0].Output = "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
			},
		},
		{
			name: "PEM private key block",
			mutate: func(e *Envelope) {
				e.Commands[0].Output = "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA...\n-----END RSA PRIVATE KEY-----"
			},
		},
		{
			name: "GitHub personal access token",
			mutate: func(e *Envelope) {
				e.Commands[0].Output = "using token ghp_1234567890abcdefghijklmnopqrstuvwxyz"
			},
		},
		{
			name: "generic key= assignment",
			mutate: func(e *Envelope) {
				e.Commands[0].Output = "api_key=supersecretvalue123"
			},
		},
		{
			name: "JWT-shaped string",
			mutate: func(e *Envelope) {
				e.Commands[0].Output = "authorization: bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnvelope()
			tt.mutate(&env)
			if err := env.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want error for unredacted secret-shaped value")
			}
		})
	}
}

func TestRedact_RemovesSecretAndValidatePasses(t *testing.T) {
	env := validEnvelope()
	env.Commands[0].Output = "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY, ghp_1234567890abcdefghijklmnopqrstuvwxyz"

	if err := env.Validate(); err == nil {
		t.Fatalf("Validate() = nil before Redact(), want error")
	}

	env.Redact()

	if strings.Contains(env.Commands[0].Output, "wJalrXUtnFEMI") || strings.Contains(env.Commands[0].Output, "ghp_1234567890") {
		t.Fatalf("Redact() left secret material in Output: %q", env.Commands[0].Output)
	}
	if !strings.Contains(env.Commands[0].Output, redactionMarker) {
		t.Fatalf("Redact() output does not contain redaction marker: %q", env.Commands[0].Output)
	}

	if err := env.Validate(); err != nil {
		t.Fatalf("Validate() after Redact() = %v, want nil", err)
	}
}

func TestRedactSecrets(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantFound bool
		wantGone  string // substring that must not survive redaction; ignored if ""
	}{
		{
			name:      "no secret",
			input:     "just an ordinary log line",
			wantFound: false,
		},
		{
			name:      "GitHub token",
			input:     "using token ghp_1234567890abcdefghijklmnopqrstuvwxyz",
			wantFound: true,
			wantGone:  "ghp_1234567890",
		},
		{
			name:      "generic key assignment",
			input:     "api_key=supersecretvalue123",
			wantFound: true,
			wantGone:  "supersecretvalue123",
		},
		{
			name:      "empty string",
			input:     "",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := RedactSecrets(tt.input)
			if found != tt.wantFound {
				t.Fatalf("RedactSecrets(%q) found = %v, want %v", tt.input, found, tt.wantFound)
			}
			if tt.wantGone != "" && strings.Contains(got, tt.wantGone) {
				t.Fatalf("RedactSecrets(%q) = %q, still contains %q", tt.input, got, tt.wantGone)
			}
			if tt.wantFound && !strings.Contains(got, redactionMarker) {
				t.Fatalf("RedactSecrets(%q) = %q, want it to contain the redaction marker", tt.input, got)
			}
		})
	}
}

func TestRedactHighConfidenceSecrets(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantFound bool
		wantGone  string // substring that must not survive redaction; ignored if ""
	}{
		{
			name:      "GitHub token still matches",
			input:     "using token ghp_1234567890abcdefghijklmnopqrstuvwxyz",
			wantFound: true,
			wantGone:  "ghp_1234567890",
		},
		{
			name:      "AWS secret env var still matches",
			input:     "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			wantFound: true,
			wantGone:  "wJalrXUtnFEMI",
		},
		{
			name:      "JWT-shaped string still matches",
			input:     "authorization: bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
			wantFound: true,
			wantGone:  "dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		},
		{
			// This is the exact behavior difference from RedactSecrets:
			// the loose generic key/token/password/secret catch-all is
			// excluded, so a plain code assignment is no longer flagged.
			name:      "generic key assignment does NOT match (unlike RedactSecrets)",
			input:     "api_key=supersecretvalue123",
			wantFound: false,
		},
		{
			name:      "no secret",
			input:     "just an ordinary log line",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := RedactHighConfidenceSecrets(tt.input)
			if found != tt.wantFound {
				t.Fatalf("RedactHighConfidenceSecrets(%q) found = %v, want %v", tt.input, found, tt.wantFound)
			}
			if tt.wantGone != "" && strings.Contains(got, tt.wantGone) {
				t.Fatalf("RedactHighConfidenceSecrets(%q) = %q, still contains %q", tt.input, got, tt.wantGone)
			}
			if tt.wantFound && !strings.Contains(got, redactionMarker) {
				t.Fatalf("RedactHighConfidenceSecrets(%q) = %q, want it to contain the redaction marker", tt.input, got)
			}
		})
	}
}

func TestValidate_SkippedOrUnavailableRequiresReason(t *testing.T) {
	tests := []struct {
		name    string
		status  Status
		reason  string
		wantErr bool
	}{
		{name: "skipped without reason", status: StatusSkipped, reason: "", wantErr: true},
		{name: "skipped with reason", status: StatusSkipped, reason: "out of scope", wantErr: false},
		{name: "unavailable without reason", status: StatusUnavailable, reason: "", wantErr: true},
		{name: "unavailable with reason", status: StatusUnavailable, reason: "tool not installed", wantErr: false},
		{name: "pass without reason is fine", status: StatusPass, reason: "", wantErr: false},
	}

	for _, tt := range tests {
		t.Run("verification/"+tt.name, func(t *testing.T) {
			env := validEnvelope()
			env.Verification = []VerificationResult{
				{Name: "check", Category: VerificationFocused, Status: tt.status, Reason: tt.reason},
			}
			err := env.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want error for status %q with empty reason", tt.status)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})

		t.Run("command/"+tt.name, func(t *testing.T) {
			env := validEnvelope()
			env.Commands = []CommandResult{
				{Name: "cmd", Classification: ClassSafe, Status: tt.status, Reason: tt.reason},
			}
			err := env.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want error for status %q with empty reason", tt.status)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestEnvelope_JSONRoundTrip(t *testing.T) {
	env := validEnvelope()

	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var got Envelope
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal(round-tripped) error = %v", err)
	}
	wantJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(original) error = %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("round-trip mismatch:\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}

func TestEnvelope_MarshalDeterministic(t *testing.T) {
	env := validEnvelope()

	first, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	second, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("json.Marshal() not deterministic:\n first: %s\nsecond: %s", first, second)
	}
}

func TestSampleHandoff_ParsesAndValidates(t *testing.T) {
	b, err := os.ReadFile("testdata/sample-handoff.json")
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}

	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if err := env.Validate(); err != nil {
		t.Fatalf("Validate() on sample fixture = %v, want nil", err)
	}
}
