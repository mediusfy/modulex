package tenants

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SecretManager resolves installation AI keys from Secret Manager
// (MOD-86): one secret per installation named prreview-ai-<installationID>,
// payload either a bare API key (legacy: implies provider "anthropic") or
// JSON {"provider": "...", "api_key": "...", "model": "...", "base_url":
// "..."}. Omitting "provider" defaults to "anthropic" for backward
// compatibility with secrets created before multi-provider support. A
// missing secret simply means the installation has not enabled AI
// commentary — engine-only reviews, no error. The worker's service
// account holds accessor permission only on the prreview-ai-* name
// pattern, nothing else in the project.
type SecretManager struct {
	Client *secretmanager.Client
	// ProjectID hosts the per-installation secrets.
	ProjectID string
}

func (s *SecretManager) AIConfig(ctx context.Context, installationID int64) (AIConfig, error) {
	name := fmt.Sprintf("projects/%s/secrets/prreview-ai-%d/versions/latest", s.ProjectID, installationID)
	resp, err := s.Client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{Name: name})
	if status.Code(err) == codes.NotFound {
		return AIConfig{}, nil
	}
	if err != nil {
		return AIConfig{}, fmt.Errorf("resolving AI key for installation %d: %w", installationID, err)
	}
	return parseAIConfigPayload(resp.GetPayload().GetData()), nil
}

// parseAIConfigPayload is the pure parsing logic behind AIConfig, factored
// out from the live Secret Manager call so its format-compatibility rules
// (JSON with/without "provider", bare-string legacy) are table-testable
// without a GCP client.
func parseAIConfigPayload(payload []byte) AIConfig {
	var cfg struct {
		Provider string `json:"provider"`
		APIKey   string `json:"api_key"`
		Model    string `json:"model"`
		BaseURL  string `json:"base_url"`
	}
	if json.Unmarshal(payload, &cfg) == nil {
		// Valid JSON: trust its fields as they are, even if that leaves
		// the config disabled (e.g. {"model": "..."} with no key is a
		// malformed installation config, never a bare-string secret —
		// falling through here would treat the whole JSON blob as a
		// literal API key).
		if cfg.APIKey == "" && cfg.BaseURL == "" {
			return AIConfig{}
		}
		if cfg.Provider == "" {
			cfg.Provider = "anthropic"
		}
		return AIConfig{Provider: cfg.Provider, APIKey: cfg.APIKey, Model: cfg.Model, BaseURL: cfg.BaseURL}
	}
	// Not valid JSON at all: a bare-key legacy secret, from before this
	// format existed. Always anthropic, since that predates
	// multi-provider support entirely.
	if raw := strings.TrimSpace(string(payload)); raw != "" {
		return AIConfig{Provider: "anthropic", APIKey: raw}
	}
	return AIConfig{}
}
