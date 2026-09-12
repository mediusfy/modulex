package tenants

import "testing"

func TestParseAIConfigPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    AIConfig
	}{
		{
			name:    "legacy JSON with no provider defaults to anthropic",
			payload: `{"api_key": "sk-ant-api03-x", "model": "claude-opus-5"}`,
			want:    AIConfig{Provider: "anthropic", APIKey: "sk-ant-api03-x", Model: "claude-opus-5"},
		},
		{
			name:    "bare-string legacy secret defaults to anthropic",
			payload: "sk-ant-api03-raw-key\n",
			want:    AIConfig{Provider: "anthropic", APIKey: "sk-ant-api03-raw-key"},
		},
		{
			name:    "explicit provider is preserved",
			payload: `{"provider": "deepseek", "api_key": "sk-deepseek", "model": "deepseek-chat"}`,
			want:    AIConfig{Provider: "deepseek", APIKey: "sk-deepseek", Model: "deepseek-chat"},
		},
		{
			name:    "openai with base_url override",
			payload: `{"provider": "openai", "api_key": "sk-oa", "model": "gpt-4o", "base_url": "https://proxy.internal/v1"}`,
			want:    AIConfig{Provider: "openai", APIKey: "sk-oa", Model: "gpt-4o", BaseURL: "https://proxy.internal/v1"},
		},
		{
			name:    "ollama with no api_key is still enabled via base_url",
			payload: `{"provider": "ollama", "base_url": "https://my-ollama:11434", "model": "llama3.1"}`,
			want:    AIConfig{Provider: "ollama", BaseURL: "https://my-ollama:11434", Model: "llama3.1"},
		},
		{
			name:    "empty payload disables AI commentary",
			payload: "",
			want:    AIConfig{},
		},
		{
			name:    "JSON with neither api_key nor base_url disables AI commentary",
			payload: `{"model": "claude-opus-5"}`,
			want:    AIConfig{},
		},
		{
			name:    "whitespace-only bare string disables AI commentary",
			payload: "   \n",
			want:    AIConfig{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAIConfigPayload([]byte(tt.payload))
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAIConfigEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  AIConfig
		want bool
	}{
		{name: "zero value is disabled", cfg: AIConfig{}, want: false},
		{name: "api key alone is enabled", cfg: AIConfig{APIKey: "x"}, want: true},
		{name: "base_url alone is enabled (self-hosted ollama)", cfg: AIConfig{BaseURL: "http://x"}, want: true},
		{name: "provider alone with neither is disabled", cfg: AIConfig{Provider: "anthropic"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Enabled(); got != tt.want {
				t.Fatalf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
