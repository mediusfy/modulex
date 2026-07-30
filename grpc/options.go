package grpc

import (
	googlegrpc "google.golang.org/grpc"
)

// DialOptions returns the reusable grpc.DialOption set every client dial
// created for a Modulex-composed service should apply: trace-context
// propagation (unary and streaming) and consistent error translation
// (unary). Callers append their own transport credentials and any
// service-specific options — DialOptions never sets credentials, since that
// is a security-sensitive choice the caller must make explicitly (e.g.
// insecure.NewCredentials() for a private network, or a real TLS
// configuration otherwise):
//
//	conn, err := grpc.NewClient(target, append(
//		[]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
//		modulexgrpc.DialOptions()...,
//	)...)
func DialOptions() []googlegrpc.DialOption {
	return []googlegrpc.DialOption{
		googlegrpc.WithChainUnaryInterceptor(TraceUnaryClientInterceptor(), UnaryClientErrorInterceptor()),
		googlegrpc.WithChainStreamInterceptor(TraceStreamClientInterceptor()),
	}
}

// ServerOptions returns the reusable grpc.ServerOption set every server
// hosting a Modulex-composed service should apply: trace-context extraction
// (unary and streaming) and consistent error mapping (unary), using mapping
// to convert domain errors to status codes. If mapping is nil,
// DefaultErrorMapping is used.
//
//	grpcServer := grpc.NewServer(modulexgrpc.ServerOptions(myErrorMapping)...)
func ServerOptions(mapping ErrorMapping) []googlegrpc.ServerOption {
	return []googlegrpc.ServerOption{
		googlegrpc.ChainUnaryInterceptor(TraceUnaryServerInterceptor(), UnaryServerErrorInterceptor(mapping)),
		googlegrpc.ChainStreamInterceptor(TraceStreamServerInterceptor()),
	}
}
