package notification

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/mediusfy/modulex"
	modulexgrpc "github.com/mediusfy/modulex/grpc"

	"github.com/mediusfy/modulex/examples/deployment/notification/adapters"
	"github.com/mediusfy/modulex/examples/deployment/notification/notificationpb"
	"github.com/mediusfy/modulex/examples/deployment/notification/ports"
	"github.com/mediusfy/modulex/examples/deployment/notification/service"
)

// grpcErrorMapping maps the notification service's domain errors to gRPC
// status codes, falling back to modulexgrpc.DefaultErrorMapping for anything
// it does not recognize. It is applied once, centrally, via
// modulexgrpc.ServerOptions rather than in GRPCServer.Send itself.
//
//	service.ErrEmptyMessage -> codes.InvalidArgument
//	(anything else)         -> modulexgrpc.DefaultErrorMapping(err)
func grpcErrorMapping(err error) codes.Code {
	if errors.Is(err, service.ErrEmptyMessage) {
		return codes.InvalidArgument
	}
	return modulexgrpc.DefaultErrorMapping(err)
}

// GRPCServerModule hosts the notification service over gRPC. It depends on
// the "notification" module (either Module or RemoteModule providing a local
// implementation would be unusual, but any module providing ports.ServiceKey
// works) having already registered ports.ServiceKey, and registers a
// modulexgrpc.Server that the Manager starts and gracefully stops as part of
// its own Start/Stop lifecycle — this is the "server lifecycle ownership"
// half of the gRPC example: the Manager, not main(), owns starting and
// stopping the gRPC listener.
type GRPCServerModule struct {
	addr string

	grpcServer *googlegrpc.Server
	server     *modulexgrpc.Server
	listenAddr net.Addr
}

// NewGRPCServerModule creates a module that serves the notification service
// over gRPC on addr (e.g. ":50051").
func NewGRPCServerModule(addr string) *GRPCServerModule {
	return &GRPCServerModule{addr: addr}
}

func (m *GRPCServerModule) Name() string { return "notification-grpc-server" }

func (m *GRPCServerModule) DependsOn() []string { return []string{"notification"} }

// Init resolves the notification service, builds the *grpc.Server (wiring
// trace propagation, consistent error mapping, and health integration), and
// binds the listener. The server is not started here — Start does that —
// but binding the listener in Init means a bad address fails fast during
// InitModules rather than silently during StartModules.
func (m *GRPCServerModule) Init(_ context.Context, reg modulex.Registry) error {
	svc, err := modulex.Resolve(reg, ports.ServiceKey)
	if err != nil {
		return err
	}

	m.grpcServer = googlegrpc.NewServer(modulexgrpc.ServerOptions(grpcErrorMapping)...)
	notificationpb.RegisterNotificationServer(m.grpcServer, adapters.NewGRPCServer(svc))
	healthpb.RegisterHealthServer(m.grpcServer, modulexgrpc.NewHealthServer(reg))

	listener, err := net.Listen("tcp", m.addr)
	if err != nil {
		return fmt.Errorf("notification-grpc-server: failed to listen on %s: %w", m.addr, err)
	}
	m.listenAddr = listener.Addr()

	server, err := modulexgrpc.NewServer(m.grpcServer, listener, modulexgrpc.WithServerLogger(reg.Logger()))
	if err != nil {
		return fmt.Errorf("notification-grpc-server: %w", err)
	}
	m.server = server
	return nil
}

// Start implements modulex.Starter, delegating to the wrapped
// modulexgrpc.Server.
func (m *GRPCServerModule) Start(ctx context.Context) error {
	return m.server.Start(ctx)
}

// Stop implements modulex.Stopper, delegating to the wrapped
// modulexgrpc.Server. This is what performs the bounded graceful shutdown
// documented on modulexgrpc.Server when Manager.StopModules runs.
func (m *GRPCServerModule) Stop(ctx context.Context) error {
	return m.server.Stop(ctx)
}

// Addr returns the address the gRPC listener is bound to. It is only valid
// after InitModules has run (e.g. useful when addr was given as ":0" or
// "127.0.0.1:0" and the actual ephemeral port must be discovered, such as in
// tests); it returns nil beforehand.
func (m *GRPCServerModule) Addr() net.Addr {
	return m.listenAddr
}

// GRPCRemoteModule registers a remote gRPC client adapter as the
// notification service, dialing target. It is the gRPC counterpart of
// RemoteModule (which does the same over HTTP): both register
// ports.ServiceKey so a dependent module (e.g. consumer) does not change
// depending on which transport backs the port.
type GRPCRemoteModule struct {
	target string
	conn   *googlegrpc.ClientConn
}

// NewGRPCRemoteModule creates a notification module that proxies to a remote
// gRPC service at target (e.g. "localhost:50051" or "dns:///notify:50051").
func NewGRPCRemoteModule(target string) (*GRPCRemoteModule, error) {
	if strings.TrimSpace(target) == "" {
		return nil, errors.New("target must not be empty")
	}
	return &GRPCRemoteModule{target: target}, nil
}

func (m *GRPCRemoteModule) Name() string { return "notification" }

func (m *GRPCRemoteModule) DependsOn() []string { return nil }

// Init dials the remote gRPC service and registers the client adapter under
// the same typed key the local module uses. grpc.NewClient does not connect
// eagerly, so a dial failure here would only be a local configuration error
// (e.g. an invalid target string); actual connectivity problems surface from
// the first RPC.
func (m *GRPCRemoteModule) Init(_ context.Context, reg modulex.Registry) error {
	dialOpts := append([]googlegrpc.DialOption{
		googlegrpc.WithTransportCredentials(insecure.NewCredentials()),
	}, modulexgrpc.DialOptions()...)

	conn, err := googlegrpc.NewClient(m.target, dialOpts...)
	if err != nil {
		return fmt.Errorf("failed to create remote notification grpc client: %w", err)
	}
	m.conn = conn

	client := ports.Sender(adapters.NewGRPCClient(conn))
	return modulex.Provide(reg, ports.ServiceKey, client)
}

// Stop implements modulex.Stopper, closing the dialed connection this module
// created — the resource-ownership convention the lifecycle guide
// describes: a module that creates its own resources releases them in Stop.
func (m *GRPCRemoteModule) Stop(_ context.Context) error {
	if m.conn == nil {
		return nil
	}
	return m.conn.Close()
}
