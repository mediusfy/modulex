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
// (MOD-86): one secret per installation named
// prreview-ai-<installationID>, payload either a bare API key or JSON
// {"api_key": "...", "model": "..."}. A missing secret simply means the
// installation has not enabled AI commentary — engine-only reviews, no
// error. The worker's service account holds accessor permission only on
// the prreview-ai-* name pattern, nothing else in the project.
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
	payload := resp.GetPayload().GetData()
	var cfg struct {
		APIKey string `json:"api_key"`
		Model  string `json:"model"`
	}
	if json.Unmarshal(payload, &cfg) == nil && cfg.APIKey != "" {
		return AIConfig{APIKey: cfg.APIKey, Model: cfg.Model}, nil
	}
	return AIConfig{APIKey: strings.TrimSpace(string(payload))}, nil
}
