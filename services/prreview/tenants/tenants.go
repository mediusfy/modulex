// Package tenants resolves per-installation configuration (ADR-0035
// decision 4, Jira MOD-86): the installation's own AI provider, key, and
// model choice. Keys live in Secret Manager under a per-installation
// resource name — never in Firestore, never in a shared env var — and are
// resolved just-in-time per job under the worker's least-privilege service
// account.
package tenants

import "context"

// AIConfig is one installation's caller-keyed AI settings. A zero value
// means the installation has not enabled AI commentary; reviews then post
// engine-only.
type AIConfig struct {
	// Provider selects the backend: "anthropic" (default when empty),
	// "openai", "deepseek", or "ollama".
	Provider string
	APIKey   string
	Model    string
	// BaseURL is required for ollama (the installation's own server
	// address) and an optional override of the default endpoint for
	// openai/deepseek.
	BaseURL string
}

// Enabled reports whether this config carries enough to attempt a call.
// Most providers need an API key; a self-hosted Ollama server may have
// none but must supply BaseURL instead — so neither field alone is a
// reliable "configured" signal on its own.
func (c AIConfig) Enabled() bool {
	return c.APIKey != "" || c.BaseURL != ""
}

// Resolver looks up an installation's AI configuration.
type Resolver interface {
	AIConfig(ctx context.Context, installationID int64) (AIConfig, error)
}

// Static is a fixed-map Resolver for tests and local development.
type Static map[int64]AIConfig

func (s Static) AIConfig(_ context.Context, installationID int64) (AIConfig, error) {
	return s[installationID], nil
}
