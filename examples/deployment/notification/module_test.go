package notification_test

import (
	"net/http"
	"testing"

	"github.com/mediusfy/modulex/examples/deployment/notification"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRemoteModuleRejectsEmptyBaseURL(t *testing.T) {
	_, err := notification.NewRemoteModule("", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "baseURL")
}

func TestNewRemoteModuleRejectsWhitespaceBaseURL(t *testing.T) {
	_, err := notification.NewRemoteModule("   ", nil)
	require.Error(t, err)
}

func TestNewRemoteModuleAcceptsValidBaseURL(t *testing.T) {
	mod, err := notification.NewRemoteModule("http://localhost:8080", http.DefaultClient)
	require.NoError(t, err)
	assert.NotNil(t, mod)
}
