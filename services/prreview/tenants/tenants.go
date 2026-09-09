// Package tenants resolves per-installation configuration (ADR-0035
// decision 4, Jira MOD-86): the installation's own AI provider key and
// model choice. Keys live in Secret Manager under a per-installation
// resource name — never in Firestore, never in a shared env var — and are
// resolved just-in-time per job under the worker's least-privilege service
// account.
package tenants

import "context"

// AIConfig is one installation's caller-keyed AI settings. A zero APIKey
// means the installation has not enabled AI commentary; reviews then post
// engine-only.
type AIConfig struct {
	APIKey string
	Model  string
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
