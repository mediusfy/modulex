package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	secret := []byte("s3cret")
	body := []byte(`{"zen":"ok"}`)
	tests := []struct {
		name    string
		header  string
		wantErr bool
	}{
		{name: "valid signature", header: sign(secret, body), wantErr: false},
		{name: "wrong secret", header: sign([]byte("other"), body), wantErr: true},
		{name: "signature over different body", header: sign(secret, []byte("tampered")), wantErr: true},
		{name: "missing prefix", header: "deadbeef", wantErr: true},
		{name: "not hex", header: "sha256=zz", wantErr: true},
		{name: "empty header", header: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifySignature(secret, body, tt.header)
			if tt.wantErr && !errors.Is(err, ErrBadSignature) {
				t.Fatalf("want ErrBadSignature, got %v", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
		})
	}
}

const validPayload = `{
  "action": "synchronize",
  "installation": {"id": 42},
  "repository": {"name": "modulex", "clone_url": "https://github.com/mediusfy/modulex.git", "owner": {"login": "mediusfy"}},
  "pull_request": {"number": 7, "head": {"sha": "abc123"}, "base": {"ref": "main"}}
}`

func TestParsePullRequestEvent(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		body      string
		want      ReviewRequest
		wantErr   string
		ignore    bool
	}{
		{
			name:      "reviewed action produces a scoped request",
			eventType: "pull_request",
			body:      validPayload,
			want: ReviewRequest{
				InstallationID: 42,
				Owner:          "mediusfy",
				Repo:           "modulex",
				PRNumber:       7,
				HeadSHA:        "abc123",
				BaseRef:        "main",
				CloneURL:       "https://github.com/mediusfy/modulex.git",
				DeliveryID:     "d-1",
			},
		},
		{
			name:      "non-pull_request events are ignored",
			eventType: "issues",
			body:      validPayload,
			ignore:    true,
		},
		{
			name:      "unreviewed actions are ignored",
			eventType: "pull_request",
			body:      `{"action": "closed"}`,
			ignore:    true,
		},
		{
			name:      "missing installation id is an error, not a guess",
			eventType: "pull_request",
			body:      `{"action":"opened","repository":{"name":"r","owner":{"login":"o"}},"pull_request":{"number":1,"head":{"sha":"a"},"base":{"ref":"main"}}}`,
			wantErr:   "missing installation id",
		},
		{
			name:      "missing repository identity is an error",
			eventType: "pull_request",
			body:      `{"action":"opened","installation":{"id":1},"pull_request":{"number":1,"head":{"sha":"a"},"base":{"ref":"main"}}}`,
			wantErr:   "missing repository identity",
		},
		{
			name:      "missing PR identity is an error",
			eventType: "pull_request",
			body:      `{"action":"opened","installation":{"id":1},"repository":{"name":"r","owner":{"login":"o"}},"pull_request":{"number":0}}`,
			wantErr:   "missing PR identity",
		},
		{
			name:      "malformed JSON is an error",
			eventType: "pull_request",
			body:      `{`,
			wantErr:   "parsing pull_request payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePullRequestEvent(tt.eventType, "d-1", []byte(tt.body))
			if tt.ignore {
				if !errors.Is(err, ErrIgnore) {
					t.Fatalf("want ErrIgnore, got %v", err)
				}
				return
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
