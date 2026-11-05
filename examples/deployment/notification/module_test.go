package notification_test

import (
	"net/http"
	"testing"

	"github.com/mediusfy/modulex/examples/deployment/notification"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRemoteModule(t *testing.T) {
	tests := []struct {
		name        string
		baseURL     string
		client      *http.Client
		wantErr     bool
		wantErrText string
	}{
		{name: "rejects empty base URL", baseURL: "", wantErr: true, wantErrText: "baseURL"},
		{name: "rejects whitespace base URL", baseURL: "   ", wantErr: true},
		{name: "accepts valid base URL", baseURL: "http://localhost:8080", client: http.DefaultClient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod, err := notification.NewRemoteModule(tt.baseURL, tt.client)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrText)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, mod)
		})
	}
}
