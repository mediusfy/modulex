package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mediusfy/modulex/provenance"
)

func results() []provenance.VerificationResult {
	return []provenance.VerificationResult{
		{Name: "check-secrets", Category: "secret_scan", Status: provenance.StatusPass},
	}
}

// TestDispatchOpenAICompatibleProviders drives openai, deepseek, and
// ollama against a local httptest server and asserts each one hits the
// documented endpoint path and auth convention for that provider.
func TestDispatchOpenAICompatibleProviders(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		baseURLMode string // "server" (use httptest URL as BaseURL), "default" (leave BaseURL empty)
		apiKey      string
		wantPath    string
		wantAuth    string // "" means no Authorization header expected
	}{
		{
			name:        "openai default endpoint gets Bearer auth",
			provider:    "openai",
			baseURLMode: "server",
			apiKey:      "sk-test",
			wantPath:    "/chat/completions",
			wantAuth:    "Bearer sk-test",
		},
		{
			name:        "deepseek default endpoint gets Bearer auth",
			provider:    "deepseek",
			baseURLMode: "server",
			apiKey:      "sk-deepseek",
			wantPath:    "/chat/completions",
			wantAuth:    "Bearer sk-deepseek",
		},
		{
			name:        "ollama uses /v1/chat/completions and allows no auth",
			provider:    "ollama",
			baseURLMode: "server",
			apiKey:      "",
			wantPath:    "/v1/chat/completions",
			wantAuth:    "",
		},
		{
			name:        "ollama sends Bearer when a key is configured",
			provider:    "ollama",
			baseURLMode: "server",
			apiKey:      "proxy-secret",
			wantPath:    "/v1/chat/completions",
			wantAuth:    "Bearer proxy-secret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotAuth string
			var gotBody openAIChatRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"looks fine"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
			}))
			defer srv.Close()

			cfg := Config{Provider: tt.provider, APIKey: tt.apiKey, Model: "test-model"}
			if tt.baseURLMode == "server" {
				cfg.BaseURL = srv.URL
			}
			text, tokens, err := Dispatch{}.Comment(context.Background(), cfg, results(), "diff --git a b")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if text != "looks fine" {
				t.Fatalf("text = %q, want %q", text, "looks fine")
			}
			if tokens != 15 {
				t.Fatalf("tokens = %d, want 15", tokens)
			}
			if gotPath != tt.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, tt.wantPath)
			}
			if gotAuth != tt.wantAuth {
				t.Fatalf("auth header = %q, want %q", gotAuth, tt.wantAuth)
			}
			if gotBody.Model != "test-model" {
				t.Fatalf("request model = %q, want test-model", gotBody.Model)
			}
			if len(gotBody.Messages) != 2 || gotBody.Messages[0].Role != "system" || gotBody.Messages[1].Role != "user" {
				t.Fatalf("unexpected messages: %+v", gotBody.Messages)
			}
		})
	}
}

func TestDispatchErrorPaths(t *testing.T) {
	tests := []struct {
		name      string
		cfg       Config
		serverErr bool // when true, the fake server returns a 401
		wantErr   string
	}{
		{
			name:    "unsupported provider",
			cfg:     Config{Provider: "watsonx", Model: "m"},
			wantErr: `unsupported AI provider "watsonx"`,
		},
		{
			name:    "ollama without base_url",
			cfg:     Config{Provider: "ollama", Model: "m"},
			wantErr: "ollama requires base_url",
		},
		{
			name:    "openai-compatible without model",
			cfg:     Config{Provider: "deepseek", BaseURL: "http://example.invalid"},
			wantErr: "requires a model",
		},
		{
			name:      "openai-compatible surfaces upstream error status",
			cfg:       Config{Provider: "openai", Model: "m"},
			serverErr: true,
			wantErr:   "status 401",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			if tt.serverErr {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
				}))
				defer srv.Close()
				cfg.BaseURL = srv.URL
			}
			_, _, err := Dispatch{}.Comment(context.Background(), cfg, results(), "diff")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDispatchAnthropicNoKeyIsSilentNoOp(t *testing.T) {
	tests := []struct{ name string }{{name: "empty anthropic key returns no text and no error"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, tokens, err := Dispatch{}.Comment(context.Background(), Config{Provider: "anthropic"}, results(), "diff")
			if err != nil || text != "" || tokens != 0 {
				t.Fatalf("got (%q, %d, %v), want (\"\", 0, nil)", text, tokens, err)
			}
		})
	}
}

func TestDispatchDefaultsToAnthropicWhenProviderEmpty(t *testing.T) {
	tests := []struct{ name string }{{name: "empty provider behaves like anthropic (back-compat)"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// No API key -> anthropicComment's early return, proving the
			// empty Provider routed to "anthropic" and not "unsupported".
			text, tokens, err := Dispatch{}.Comment(context.Background(), Config{}, results(), "diff")
			if err != nil || text != "" || tokens != 0 {
				t.Fatalf("got (%q, %d, %v), want (\"\", 0, nil)", text, tokens, err)
			}
		})
	}
}

func TestNoneCommentaryAlwaysDeclines(t *testing.T) {
	tests := []struct{ name string }{{name: "None never errors and never produces text"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, tokens, err := None{}.Comment(context.Background(), Config{Provider: "openai", APIKey: "x", Model: "m"}, results(), "diff")
			if err != nil || text != "" || tokens != 0 {
				t.Fatalf("got (%q, %d, %v), want (\"\", 0, nil)", text, tokens, err)
			}
		})
	}
}
