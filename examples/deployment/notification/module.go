// Package notification is the composition root for the notification feature.
// It wires the in-process service implementation and exposes it through typed
// service keys and optional HTTP routes.
package notification

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/mediusfy/modulex"
	modulexchi "github.com/mediusfy/modulex/chi"
	"github.com/mediusfy/modulex/examples/deployment/notification/adapters"
	"github.com/mediusfy/modulex/examples/deployment/notification/ports"
	"github.com/mediusfy/modulex/examples/deployment/notification/service"
)

// Module wires the notification service and HTTP handlers.
type Module struct{}

// NewModule creates a notification module.
func NewModule() *Module {
	return &Module{}
}

func (m *Module) Name() string { return "notification" }

func (m *Module) DependsOn() []string { return nil }

// Init registers the notification service and, if a Chi router is available,
// mounts the HTTP endpoint.
func (m *Module) Init(ctx context.Context, reg modulex.Registry) error {
	svc := ports.Service(service.New(reg.Logger()))
	if err := modulex.Provide(reg, ports.ServiceKey, svc); err != nil {
		return err
	}

	router, err := modulexchi.ResolveRouter(reg)
	if err != nil {
		if !errors.Is(err, modulex.ErrServiceNotFound) {
			return err
		}
		// No router registered; the module works purely as a service.
		return nil
	}

	server := adapters.NewHTTPServer(svc, reg.Logger())
	router.Post("/notify", server.SendHandler())
	return nil
}

// RemoteModule registers a remote HTTP client adapter as the notification
// service. It is used in standalone deployments where the real notification
// service runs in a separate process.
type RemoteModule struct {
	baseURL string
	client  *http.Client
}

// NewRemoteModule creates a notification module that proxies to a remote
// service over HTTP.
func NewRemoteModule(baseURL string, client *http.Client) (*RemoteModule, error) {
	if _, err := adapters.NewHTTPClient(baseURL, client); err != nil {
		return nil, fmt.Errorf("invalid remote notification module: %w", err)
	}
	return &RemoteModule{baseURL: baseURL, client: client}, nil
}

func (m *RemoteModule) Name() string { return "notification" }

func (m *RemoteModule) DependsOn() []string { return nil }

// Init registers the remote client adapter under the same typed key the local
// module uses.
func (m *RemoteModule) Init(_ context.Context, reg modulex.Registry) error {
	client, err := adapters.NewHTTPClient(m.baseURL, m.client)
	if err != nil {
		return fmt.Errorf("failed to create remote notification client: %w", err)
	}
	remoteClient := ports.Service(client)
	return modulex.Provide(reg, ports.ServiceKey, remoteClient)
}
