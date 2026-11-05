package provenanceci_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mediusfy/modulex/provenance"
	"github.com/mediusfy/modulex/tools/provenanceci"
)

func baseConfig() provenanceci.Config {
	return provenanceci.Config{
		RepoPath: ".",
		Commit:   "abc1234",
		Branch:   "main",
		Jobs: []provenanceci.JobResult{
			{Name: "lint", Result: "success"},
		},
	}
}

func TestBuildEnvelope_MapsJobResults(t *testing.T) {
	tests := []struct {
		name       string
		result     string
		wantStatus provenance.Status
		wantReason bool
	}{
		{name: "success", result: "success", wantStatus: provenance.StatusPass, wantReason: false},
		{name: "failure", result: "failure", wantStatus: provenance.StatusFail, wantReason: false},
		{name: "cancelled", result: "cancelled", wantStatus: provenance.StatusFail, wantReason: true},
		{name: "skipped", result: "skipped", wantStatus: provenance.StatusSkipped, wantReason: true},
		{name: "unrecognized value fails safe", result: "some-future-value", wantStatus: provenance.StatusFail, wantReason: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.Jobs = []provenanceci.JobResult{{Name: "job-under-test", Result: tt.result}}

			env, err := provenanceci.BuildEnvelope(cfg)
			if err != nil {
				t.Fatalf("BuildEnvelope() error = %v", err)
			}
			if len(env.Verification) != 1 {
				t.Fatalf("len(Verification) = %d, want 1", len(env.Verification))
			}
			got := env.Verification[0]
			if got.Category != provenance.VerificationFull {
				t.Errorf("Category = %q, want %q", got.Category, provenance.VerificationFull)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if tt.wantReason && got.Reason == "" {
				t.Errorf("Reason is empty, want a non-empty explanation for result %q", tt.result)
			}
			if !tt.wantReason && got.Reason != "" {
				t.Errorf("Reason = %q, want empty for result %q", got.Reason, tt.result)
			}
		})
	}
}

func TestBuildEnvelope_SortsJobsByName(t *testing.T) {
	tests := []struct {
		name string
		jobs []provenanceci.JobResult
		want []string
	}{
		{
			name: "sorted regardless of input order",
			jobs: []provenanceci.JobResult{
				{Name: "zeta", Result: "success"},
				{Name: "alpha", Result: "success"},
				{Name: "mid", Result: "success"},
			},
			want: []string{"alpha", "mid", "zeta"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.Jobs = tt.jobs

			env, err := provenanceci.BuildEnvelope(cfg)
			if err != nil {
				t.Fatalf("BuildEnvelope() error = %v", err)
			}

			var names []string
			for _, v := range env.Verification {
				names = append(names, v.Name)
			}
			if strings.Join(names, ",") != strings.Join(tt.want, ",") {
				t.Errorf("Verification names = %v, want %v (sorted regardless of input order)", names, tt.want)
			}
		})
	}
}

func TestBuildEnvelope_RequiresCommit(t *testing.T) {
	tests := []struct {
		name   string
		commit string
	}{
		{name: "empty commit is rejected", commit: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.Commit = tt.commit

			if _, err := provenanceci.BuildEnvelope(cfg); err == nil {
				t.Fatal("BuildEnvelope() error = nil, want an error (Repository.Commit is required)")
			}
		})
	}
}

func TestBuildEnvelope_DefaultsCreatedAt(t *testing.T) {
	tests := []struct {
		name string
		cfg  provenanceci.Config
	}{
		{name: "unset CreatedAt defaults to now", cfg: baseConfig()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now().UTC()
			env, err := provenanceci.BuildEnvelope(tt.cfg)
			after := time.Now().UTC()
			if err != nil {
				t.Fatalf("BuildEnvelope() error = %v", err)
			}

			if env.CreatedAt.Before(before) || env.CreatedAt.After(after) {
				t.Errorf("CreatedAt = %v, want between %v and %v", env.CreatedAt, before, after)
			}
		})
	}
}

func TestBuildEnvelope_RepositoryStateAndAgentAreRecorded(t *testing.T) {
	tests := []struct {
		name      string
		dirty     bool
		wantAgent string
	}{
		{name: "dirty repository with ci agent", dirty: true, wantAgent: "ci"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.Dirty = tt.dirty

			env, err := provenanceci.BuildEnvelope(cfg)
			if err != nil {
				t.Fatalf("BuildEnvelope() error = %v", err)
			}

			if env.Repository.Commit != cfg.Commit {
				t.Errorf("Repository.Commit = %q, want %q", env.Repository.Commit, cfg.Commit)
			}
			if env.Repository.Branch != cfg.Branch {
				t.Errorf("Repository.Branch = %q, want %q", env.Repository.Branch, cfg.Branch)
			}
			if env.Repository.Dirty != tt.dirty {
				t.Errorf("Repository.Dirty = %v, want %v", env.Repository.Dirty, tt.dirty)
			}
			if env.Agent.Name != tt.wantAgent {
				t.Errorf("Agent.Name = %q, want %q", env.Agent.Name, tt.wantAgent)
			}
		})
	}
}

func TestBuildEnvelope_ProducesValidMarshalableJSON(t *testing.T) {
	tests := []struct {
		name string
		cfg  provenanceci.Config
	}{
		{name: "base config round-trips and validates", cfg: baseConfig()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := provenanceci.BuildEnvelope(tt.cfg)
			if err != nil {
				t.Fatalf("BuildEnvelope() error = %v", err)
			}

			b, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("json.Marshal(env) error = %v", err)
			}

			var roundTripped provenance.Envelope
			if err := json.Unmarshal(b, &roundTripped); err != nil {
				t.Fatalf("json.Unmarshal error = %v", err)
			}
			if err := roundTripped.Validate(); err != nil {
				t.Fatalf("round-tripped envelope failed Validate(): %v", err)
			}
		})
	}
}
