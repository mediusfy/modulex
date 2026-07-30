package adapters

import (
	"context"

	googlegrpc "google.golang.org/grpc"

	modulexgrpc "github.com/mediusfy/modulex/grpc"

	"github.com/mediusfy/modulex/examples/deployment/notification/notificationpb"
	"github.com/mediusfy/modulex/examples/deployment/notification/ports"
)

// GRPCClient implements ports.Sender by calling a remote notification
// service over gRPC. It demonstrates how a standalone process can consume a
// feature without importing its implementation — the gRPC counterpart of
// HTTPClient.
type GRPCClient struct {
	client notificationpb.NotificationClient
}

// NewGRPCClient creates a remote notification client backed by conn. conn
// must not be nil; the caller owns dialing it (typically with
// googlegrpc.NewClient plus modulexgrpc.DialOptions(), as
// notification.GRPCRemoteModule does) and closing it during shutdown.
func NewGRPCClient(conn *googlegrpc.ClientConn) *GRPCClient {
	return &GRPCClient{client: notificationpb.NewNotificationClient(conn)}
}

// Send implements ports.Sender by forwarding the message to the remote gRPC
// service. Any error returned by the RPC is translated via
// modulexgrpc.TranslateError into one of that package's sentinel errors, so
// a caller can use errors.Is(err, modulexgrpc.ErrInvalidInput) instead of
// inspecting the gRPC status directly.
func (c *GRPCClient) Send(ctx context.Context, message string) error {
	_, err := c.client.Send(ctx, &notificationpb.SendRequest{Message: message})
	return modulexgrpc.TranslateError(err)
}

var _ ports.Sender = (*GRPCClient)(nil)
