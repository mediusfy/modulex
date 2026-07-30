package adapters

import (
	"context"

	"github.com/mediusfy/modulex/examples/deployment/notification/notificationpb"
	"github.com/mediusfy/modulex/examples/deployment/notification/ports"
)

// GRPCServer exposes the notification service over gRPC. It keeps gRPC
// concerns (the generated request/response types) separate from business
// logic, mirroring HTTPServer's role for the HTTP transport.
//
// GRPCServer returns the domain error from ports.Sender.Send unchanged; it
// does not convert it to a gRPC status itself. Consistent error-to-status
// mapping is applied once, centrally, by the grpc.UnaryServerErrorInterceptor
// configured on the *grpc.Server that hosts this service (see
// grpcErrorMapping in ../grpc_module.go) rather than repeated in every
// handler.
type GRPCServer struct {
	notificationpb.UnimplementedNotificationServer

	svc ports.Sender
}

// NewGRPCServer creates a gRPC adapter for the notification service.
func NewGRPCServer(svc ports.Sender) *GRPCServer {
	return &GRPCServer{svc: svc}
}

// Send implements notificationpb.NotificationServer by forwarding the
// request to the wrapped ports.Sender.
func (s *GRPCServer) Send(ctx context.Context, req *notificationpb.SendRequest) (*notificationpb.SendResponse, error) {
	if err := s.svc.Send(ctx, req.GetMessage()); err != nil {
		return nil, err
	}
	return &notificationpb.SendResponse{}, nil
}

var _ notificationpb.NotificationServer = (*GRPCServer)(nil)
