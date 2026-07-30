package grpc

import (
	"context"
	"errors"
	"fmt"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Sentinel errors a client can compare against with errors.Is after calling
// TranslateError on an error returned by a gRPC call. Each corresponds to a
// commonly used gRPC status code; see TranslateError's doc comment for the
// full code table.
var (
	// ErrInvalidInput corresponds to codes.InvalidArgument.
	ErrInvalidInput = errors.New("grpc: invalid input")
	// ErrNotFound corresponds to codes.NotFound.
	ErrNotFound = errors.New("grpc: not found")
	// ErrAlreadyExists corresponds to codes.AlreadyExists.
	ErrAlreadyExists = errors.New("grpc: already exists")
	// ErrPermissionDenied corresponds to codes.PermissionDenied.
	ErrPermissionDenied = errors.New("grpc: permission denied")
	// ErrUnauthenticated corresponds to codes.Unauthenticated.
	ErrUnauthenticated = errors.New("grpc: unauthenticated")
	// ErrUnavailable corresponds to codes.Unavailable.
	ErrUnavailable = errors.New("grpc: unavailable")
	// ErrDeadlineExceeded corresponds to codes.DeadlineExceeded.
	ErrDeadlineExceeded = errors.New("grpc: deadline exceeded")
	// ErrCanceled corresponds to codes.Canceled.
	ErrCanceled = errors.New("grpc: canceled")
	// ErrInternal is returned for any other non-OK code, including
	// codes.Internal itself and codes.Unknown.
	ErrInternal = errors.New("grpc: internal error")
)

// codeSentinels maps a gRPC status code to the sentinel error TranslateError
// returns for it. Codes not present here fall back to ErrInternal.
var codeSentinels = map[codes.Code]error{
	codes.InvalidArgument:  ErrInvalidInput,
	codes.NotFound:         ErrNotFound,
	codes.AlreadyExists:    ErrAlreadyExists,
	codes.PermissionDenied: ErrPermissionDenied,
	codes.Unauthenticated:  ErrUnauthenticated,
	codes.Unavailable:      ErrUnavailable,
	codes.DeadlineExceeded: ErrDeadlineExceeded,
	codes.Canceled:         ErrCanceled,
}

// ErrorMapping maps a domain error to the gRPC status code that best
// describes it. A caller implements this once per service, checking its own
// domain sentinel errors with errors.Is/As, and passes it to
// UnaryServerErrorInterceptor or ServerOptions.
//
// See docs/planning/grpc-adapter-guide.md for the mapping table used by the
// notification example (service.ErrEmptyMessage -> codes.InvalidArgument,
// falling back to DefaultErrorMapping for everything else).
type ErrorMapping func(err error) codes.Code

// DefaultErrorMapping is a conservative, domain-agnostic fallback: it maps
// context cancellation and deadline errors to their gRPC equivalents, and
// everything else to codes.Internal. This package cannot see a specific
// service's domain errors (e.g. "not found", "invalid input"), so a service
// should supply its own ErrorMapping that checks its own sentinel errors
// first and falls back to DefaultErrorMapping — see the notification
// example's grpcErrorMapping in
// examples/deployment/notification/grpc_module.go.
func DefaultErrorMapping(err error) codes.Code {
	switch {
	case errors.Is(err, context.Canceled):
		return codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return codes.DeadlineExceeded
	default:
		return codes.Internal
	}
}

// UnaryServerErrorInterceptor converts a non-nil error returned by a unary
// handler into a status.Error using mapping, so every RPC on the server
// returns the same consistent error shape instead of a bespoke internal
// error. If mapping is nil, DefaultErrorMapping is used.
//
// An error that already carries a gRPC status (for example, one returned by
// a nested gRPC client call the handler made and passed straight through) is
// left unchanged rather than re-wrapped, so its original code is preserved.
func UnaryServerErrorInterceptor(mapping ErrorMapping) googlegrpc.UnaryServerInterceptor {
	if mapping == nil {
		mapping = DefaultErrorMapping
	}
	return func(ctx context.Context, req any, info *googlegrpc.UnaryServerInfo, handler googlegrpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if err == nil {
			return resp, nil
		}
		if _, ok := status.FromError(err); ok {
			return resp, err
		}
		return resp, status.Error(mapping(err), err.Error())
	}
}

// TranslateError converts an error returned by a gRPC client call into one of
// this package's sentinel errors, so a caller can write
// errors.Is(err, grpc.ErrNotFound) instead of inspecting status.Code
// directly. The original status message is preserved via %w-wrapping.
//
// Code table:
//
//	codes.InvalidArgument  -> ErrInvalidInput
//	codes.NotFound         -> ErrNotFound
//	codes.AlreadyExists    -> ErrAlreadyExists
//	codes.PermissionDenied -> ErrPermissionDenied
//	codes.Unauthenticated  -> ErrUnauthenticated
//	codes.Unavailable      -> ErrUnavailable
//	codes.DeadlineExceeded -> ErrDeadlineExceeded
//	codes.Canceled         -> ErrCanceled
//	anything else          -> ErrInternal
//
// TranslateError returns nil for a nil error or a status with codes.OK, and
// returns err unchanged if it does not carry a gRPC status at all (e.g. a
// local dial error).
func TranslateError(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	if st.Code() == codes.OK {
		return nil
	}
	sentinel, known := codeSentinels[st.Code()]
	if !known {
		sentinel = ErrInternal
	}
	return fmt.Errorf("%s: %w", st.Message(), sentinel)
}

// UnaryClientErrorInterceptor applies TranslateError to the error returned by
// every unary RPC, so a caller using this interceptor gets sentinel-error
// translation automatically instead of having to call TranslateError at every
// call site.
func UnaryClientErrorInterceptor() googlegrpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *googlegrpc.ClientConn, invoker googlegrpc.UnaryInvoker, opts ...googlegrpc.CallOption) error {
		return TranslateError(invoker(ctx, method, req, reply, cc, opts...))
	}
}
