package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/mediusfy/modulex/provenance"
)

// TestMain disables the production SSRF guard (validateBaseURL) for every
// test in this file except TestValidateBaseURL* below, which exercise it
// directly: every other test here points cfg.BaseURL at a local httptest
// server (loopback) to simulate a provider's real endpoint, which
// validateBaseURL must reject in production (see its doc comment) but
// which these wire-format/error-path tests need to keep working.
func TestMain(m *testing.M) {
	validateBaseURLHook = func(context.Context, string) error { return nil }
	os.Exit(m.Run())
}

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
			cfg:       Config{Provider: "openai", APIKey: "sk-test", Model: "m"},
			serverErr: true,
			wantErr:   "status 401",
		},
		{
			name:    "openai without api_key",
			cfg:     Config{Provider: "openai", Model: "m", BaseURL: "http://example.invalid"},
			wantErr: "openai requires api_key",
		},
		{
			name:    "deepseek without api_key",
			cfg:     Config{Provider: "deepseek", Model: "m", BaseURL: "http://example.invalid"},
			wantErr: "deepseek requires api_key",
		},
		{
			name:    "empty provider with base_url is ambiguous, not a silent anthropic fallback",
			cfg:     Config{Model: "m", BaseURL: "http://example.invalid"},
			wantErr: "provider is required when base_url is set",
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

// TestValidateBaseURLBlocksSSRFTargets is the L10(c) regression test:
// base_url is a tenant-supplied value (see tenants.AIConfig/SecretManager),
// and the hosted worker — not the tenant's own network — is what dials it.
// It must never be usable to reach loopback, link-local (including the
// cloud metadata address), private, or otherwise internal addresses.
func TestValidateBaseURLBlocksSSRFTargets(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "loopback", baseURL: "http://127.0.0.1:11434"},
		{name: "localhost", baseURL: "http://localhost:11434"},
		{name: "cloud metadata IP", baseURL: "http://169.254.169.254/computeMetadata/v1/"},
		{name: "cloud metadata hostname", baseURL: "http://metadata.google.internal/computeMetadata/v1/"},
		{name: "RFC1918 private", baseURL: "http://10.0.0.5:11434"},
		{name: "RFC1918 private 192.168", baseURL: "http://192.168.1.5:11434"},
		{name: "unspecified", baseURL: "http://0.0.0.0:11434"},
		{name: "non-http(s) scheme", baseURL: "ftp://example.com"},
		{name: "no host", baseURL: "http:///path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateBaseURL(context.Background(), tt.baseURL); err == nil {
				t.Fatalf("validateBaseURL(%q) = nil, want a rejection error", tt.baseURL)
			}
		})
	}
}

// TestValidateBaseURLAllowsOrdinaryPublicAddresses ensures the SSRF guard
// does not also break the legitimate case it exists alongside: an
// installation's genuinely public-reachable Ollama server or an
// openai/deepseek-compatible override.
func TestValidateBaseURLAllowsOrdinaryPublicAddresses(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "public IP literal", baseURL: "https://8.8.8.8:11434"},
		{name: "https scheme with ordinary hostname", baseURL: "https://example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateBaseURL(context.Background(), tt.baseURL); err != nil {
				t.Fatalf("validateBaseURL(%q) = %v, want nil", tt.baseURL, err)
			}
		})
	}
}

// TestDispatchRejectsSSRFBaseURL proves the guard is actually wired into
// Dispatch.Comment's outbound path, bypassing TestMain's override to call
// the real validateBaseURL for this one test.
func TestDispatchRejectsSSRFBaseURL(t *testing.T) {
	old := validateBaseURLHook
	validateBaseURLHook = validateBaseURL
	defer func() { validateBaseURLHook = old }()

	cfg := Config{Provider: "ollama", Model: "m", BaseURL: "http://169.254.169.254/computeMetadata/v1/"}
	_, _, err := Dispatch{}.Comment(context.Background(), cfg, results(), "diff")
	if err == nil || !strings.Contains(err.Error(), "base_url rejected") {
		t.Fatalf("err = %v, want a base_url rejection", err)
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
