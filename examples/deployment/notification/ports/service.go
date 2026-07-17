// Package ports defines the inbound and outbound contracts for the notification
// feature. Keeping interfaces in a dedicated package lets both local and remote
// compositions depend on the same contract without importing implementation
// details.
package ports

import (
	"context"

	"github.com/mediusfy/modulex"
)

// Service is the primary inbound port for sending notifications.
type Service interface {
	// Send delivers a notification message. The implementation may be a local
	// in-process service or a remote client adapter.
	Send(ctx context.Context, message string) error
}

// ServiceKey is the typed registry key for Service. Keeping the key with the
// port lets consumers depend only on the contract package rather than the full
// notification module.
var ServiceKey = modulex.NewKey[Service]("notification.Service")

