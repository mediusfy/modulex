package adapters_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	modulexgrpc "github.com/mediusfy/modulex/grpc"

	"github.com/mediusfy/modulex/examples/deployment/notification/adapters"
	"github.com/mediusfy/modulex/examples/deployment/notification/notificationpb"
	"github.com/mediusfy/modulex/examples/deployment/notification/service"
)

// grpcErrorMapping mirrors notification.grpcErrorMapping (unexported in the
// notification package) so this test exercises the same mapping the real
// composition root uses, without creating an import cycle.
func grpcErrorMapping(err error) codes.Code {
	if errors.Is(err, service.ErrEmptyMessage) {
		return codes.InvalidArgument
	}
	return modulexgrpc.DefaultErrorMapping(err)
}

// startGRPCNotification wires GRPCServer (backed by a real service.New) and
// GRPCClient over an in-memory bufconn connection, validating the generated
// notificationpb code actually round-trips a Send call end-to-end — not
// just that the .proto looks plausible.
func startGRPCNotification(t *testing.T) *adapters.GRPCClient {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(logger)

	lis := bufconn.Listen(1024 * 1024)
	grpcServer := googlegrpc.NewServer(modulexgrpc.ServerOptions(grpcErrorMapping)...)
	notificationpb.RegisterNotificationServer(grpcServer, adapters.NewGRPCServer(svc))

	go func() {
		_ = grpcServer.Serve(lis)
	}()
	t.Cleanup(grpcServer.Stop)

	conn, err := googlegrpc.NewClient("passthrough:///bufconn",
		googlegrpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		googlegrpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return adapters.NewGRPCClient(conn)
}

func TestGRPCClientServerRoundTrip(t *testing.T) {
	client := startGRPCNotification(t)

	err := client.Send(context.Background(), "hello over grpc")
	assert.NoError(t, err)
}

func TestGRPCClientServerRoundTripEmptyMessageMapsToInvalidInput(t *testing.T) {
	client := startGRPCNotification(t)

	err := client.Send(context.Background(), "")
	require.Error(t, err)
	assert.ErrorIs(t, err, modulexgrpc.ErrInvalidInput)
}
