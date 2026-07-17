package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mediusfy/modulex/examples/deployment/notification/ports"
)

// HTTPClient implements ports.Service by calling a remote notification service
// over HTTP. It demonstrates how a standalone process can consume a feature
// without importing its implementation.
type HTTPClient struct {
	baseURL string
	client  *http.Client
}

// NewHTTPClient creates a remote notification client.
func NewHTTPClient(baseURL string, client *http.Client) *HTTPClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPClient{baseURL: baseURL, client: client}
}

// Send implements ports.Service by forwarding the message to the remote service.
func (c *HTTPClient) Send(ctx context.Context, message string) error {
	reqBody, err := json.Marshal(SendRequest{Message: message})
	if err != nil {
		return fmt.Errorf("failed to marshal notification request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/notify", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create notification request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("notification request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("notification service returned %d", resp.StatusCode)
	}
	return nil
}

var _ ports.Service = (*HTTPClient)(nil)
