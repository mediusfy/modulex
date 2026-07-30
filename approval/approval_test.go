package approval

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
)

// looksLikeSerialCounter is a best-effort sanity check that a generated
// token is not suspiciously low-entropy (e.g. "1", "2", "3", or a
// fixed-width zero-padded counter). It is not a security boundary — it
// exists so a regression that accidentally swapped crypto/rand for a
// counter or a fixed-seed math/rand generator would be caught by a test
// rather than silently shipped.
func looksLikeSerialCounter(token string) bool {
	return strings.TrimLeft(token, "0123456789") == ""
}

func TestGenerateToken_UniqueAndWellFormed(t *testing.T) {
	const n = 50
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		token, hash, err := generateToken()
		if err != nil {
			t.Fatalf("generateToken() error = %v", err)
		}
		if token == "" {
			t.Fatal("generateToken() returned an empty token")
		}
		if len(token) < 32 {
			t.Errorf("generateToken() token %q is only %d chars; want at least 32 (16+ bytes hex-encoded)", token, len(token))
		}
		if looksLikeSerialCounter(token) {
			t.Errorf("generateToken() token %q looks like a serial counter, not random output", token)
		}
		if seen[token] {
			t.Fatalf("generateToken() produced a duplicate token %q after %d calls", token, i)
		}
		seen[token] = true

		sum := sha256.Sum256([]byte(token))
		wantHash := hex.EncodeToString(sum[:])
		if hash != wantHash {
			t.Errorf("generateToken() hash = %q, want sha256(token) = %q", hash, wantHash)
		}
	}
}

func TestGrant_StringNeverIncludesRawToken(t *testing.T) {
	g := Grant{
		Token:      "super-secret-raw-token-value",
		TokenHash:  "abc123",
		Scope:      Scope{Action: "push", Resource: "main"},
		ApprovedBy: "drew@jocham.io",
		ApprovedAt: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC),
		ExpiresAt:  time.Date(2026, 7, 29, 12, 10, 0, 0, time.UTC),
	}

	for _, rendered := range []string{
		g.String(),
		fmt.Sprintf("%v", g),
		fmt.Sprintf("%+v", g),
	} {
		if strings.Contains(rendered, g.Token) {
			t.Errorf("rendered Grant contains the raw token: %q", rendered)
		}
		if !strings.Contains(rendered, g.TokenHash) {
			t.Errorf("rendered Grant should contain TokenHash %q, got %q", g.TokenHash, rendered)
		}
	}
}

func TestGrant_ExpiredTreatsZeroExpiresAtAsExpired(t *testing.T) {
	// A Grant that somehow ended up with a zero ExpiresAt (bypassing
	// Broker.Grant, which never allows this) must be treated as already
	// expired, never as "no expiry" / "never expires". This is a
	// defense-in-depth check independent of Broker.Grant's own validation.
	g := Grant{Scope: Scope{Action: "push"}}
	if !g.expired(time.Now()) {
		t.Error("Grant with zero ExpiresAt must be treated as expired, got not-expired")
	}
}

func TestGrant_MatchesScope(t *testing.T) {
	g := Grant{Scope: Scope{Action: "push", Resource: "branch-a"}}

	tests := []struct {
		name  string
		scope Scope
		want  bool
	}{
		{"exact match", Scope{Action: "push", Resource: "branch-a"}, true},
		{"different resource", Scope{Action: "push", Resource: "branch-b"}, false},
		{"different action, same resource", Scope{Action: "delete", Resource: "branch-a"}, false},
		{"different action and resource", Scope{Action: "delete", Resource: "branch-b"}, false},
		{"empty scope", Scope{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := g.matchesScope(tt.scope); got != tt.want {
				t.Errorf("matchesScope(%+v) = %v, want %v", tt.scope, got, tt.want)
			}
		})
	}
}

func TestGrant_ToProvenanceApproval(t *testing.T) {
	approvedAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	g := Grant{
		Token:      "raw-token-should-not-appear",
		TokenHash:  "deadbeef",
		Scope:      Scope{Action: "release", Resource: "v1.2.0"},
		ApprovedBy: "drew@jocham.io",
		ApprovedAt: approvedAt,
	}

	pa := g.ToProvenanceApproval()
	if pa.Action != "release" {
		t.Errorf("Action = %q, want %q", pa.Action, "release")
	}
	if pa.ApprovedBy != "drew@jocham.io" {
		t.Errorf("ApprovedBy = %q, want %q", pa.ApprovedBy, "drew@jocham.io")
	}
	if !pa.ApprovedAt.Equal(approvedAt) {
		t.Errorf("ApprovedAt = %v, want %v", pa.ApprovedAt, approvedAt)
	}
	if strings.Contains(pa.Notes, g.Token) {
		t.Errorf("Notes must never contain the raw token, got %q", pa.Notes)
	}
	if !strings.Contains(pa.Notes, g.TokenHash) {
		t.Errorf("Notes should reference TokenHash %q, got %q", g.TokenHash, pa.Notes)
	}
	if !strings.Contains(pa.Notes, "v1.2.0") {
		t.Errorf("Notes should reference the resource, got %q", pa.Notes)
	}
}
