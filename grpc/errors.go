package grpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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

// genericInternalErrorMessage is the status message
// UnaryServerErrorInterceptor sends for codes.Internal and codes.Unknown
// by default, instead of the handler error's real text. Those two codes
// are DefaultErrorMapping's (and most custom ErrorMappings') catch-all
// fallback for errors the service doesn't specifically recognize, which is
// exactly where a wrapped driver/library error (a DSN, a hostname, a file
// path, a SQL fragment) most often ends up — and this interceptor's
// callers are frequently unauthenticated RPC clients.
const genericInternalErrorMessage = "internal error"

// clientSafeStatusCodes are status codes whose associated error message is
// safe to send back to an RPC caller verbatim, because they correspond to
// expected, caller-actionable domain conditions that a service's
// ErrorMapping deliberately classified (not merely a fallback for
// "anything else"). codes.Internal and codes.Unknown are intentionally
// excluded — see genericInternalErrorMessage.
var clientSafeStatusCodes = map[codes.Code]bool{
	codes.InvalidArgument:  true,
	codes.NotFound:         true,
	codes.AlreadyExists:    true,
	codes.PermissionDenied: true,
	codes.Unauthenticated:  true,
	codes.Unavailable:      true,
	codes.DeadlineExceeded: true,
	codes.Canceled:         true,
}

// serverErrorInterceptorConfig holds UnaryServerErrorInterceptor's options.
type serverErrorInterceptorConfig struct {
	exposeInternal bool
	logger         *slog.Logger
}

// ServerErrorInterceptorOption configures UnaryServerErrorInterceptor.
type ServerErrorInterceptorOption func(*serverErrorInterceptorConfig)

// WithExposeInternalErrors makes UnaryServerErrorInterceptor send the
// handler error's real message to the RPC caller even when it maps to
// codes.Internal or codes.Unknown, instead of genericInternalErrorMessage.
// Do not enable this for a service exposed to untrusted or unauthenticated
// callers: those two codes are exactly where a wrapped driver/library
// error (DSNs, hostnames, file paths, SQL fragments) most often ends up.
func WithExposeInternalErrors() ServerErrorInterceptorOption {
	return func(c *serverErrorInterceptorConfig) { c.exposeInternal = true }
}

// WithServerErrorLogger sets the logger UnaryServerErrorInterceptor uses to
// record a handler error's real text when it maps to codes.Internal or
// codes.Unknown and is therefore not sent to the caller. If not provided,
// or nil, slog.Default() is used.
func WithServerErrorLogger(logger *slog.Logger) ServerErrorInterceptorOption {
	return func(c *serverErrorInterceptorConfig) { c.logger = logger }
}

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
//
// By default, the status message sent to the caller is the handler error's
// real text only for status codes in clientSafeStatusCodes. For
// codes.Internal and codes.Unknown — the usual fallback for an error the
// mapping doesn't specifically recognize, and so the place a wrapped
// driver/library error (a DSN, a hostname, a file path, a SQL fragment)
// most often ends up — the real error is logged server-side instead, and
// the caller gets genericInternalErrorMessage. Pass
// WithExposeInternalErrors to restore the previous behavior of always
// sending the real message.
func UnaryServerErrorInterceptor(mapping ErrorMapping, opts ...ServerErrorInterceptorOption) googlegrpc.UnaryServerInterceptor {
	if mapping == nil {
		mapping = DefaultErrorMapping
	}
	cfg := &serverErrorInterceptorConfig{logger: slog.Default()}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	return func(ctx context.Context, req any, info *googlegrpc.UnaryServerInfo, handler googlegrpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if err == nil {
			return resp, nil
		}
		if _, ok := status.FromError(err); ok {
			return resp, err
		}

		code := mapping(err)
		msg := err.Error()
		if !cfg.exposeInternal && !clientSafeStatusCodes[code] {
			cfg.logger.ErrorContext(ctx, "grpc: unary handler returned an internal error",
				slog.String("method", info.FullMethod),
				slog.String("code", code.String()),
				slog.Any("error", err))
			msg = genericInternalErrorMessage
		}
		return resp, status.Error(code, msg)
	}
}

// translatedError is TranslateError's return type for a status-carrying
// input error. It wraps the matched sentinel so errors.Is/errors.As keep
// working on the translated error (via Unwrap), and it also implements
// GRPCStatus so status.Code and status.FromError keep reporting the
// original status — not codes.Unknown — on the translated error too.
// Before this type existed, TranslateError's result was a plain
// fmt.Errorf("%w")-wrapped error with no GRPCStatus method, so
// status.Code(TranslateError(err)) always reported codes.Unknown and any
// status Details on the original error were unreachable from the result.
type translatedError struct {
	st       *status.Status
	sentinel error
}

func (e *translatedError) Error() string {
	return fmt.Sprintf("%s: %s", e.st.Message(), e.sentinel)
}

// GRPCStatus implements the interface status.FromError (and so
// status.Code) checks for, returning the original, unmodified status.
func (e *translatedError) GRPCStatus() *status.Status {
	return e.st
}

// Unwrap exposes the matched sentinel error so errors.Is(err,
// grpc.ErrNotFound) (etc.) and errors.As keep working on the translated
// error, matching this package's documented contract.
func (e *translatedError) Unwrap() error {
	return e.sentinel
}

// TranslateError converts an error returned by a gRPC client call into one of
// this package's sentinel errors, so a caller can write
// errors.Is(err, grpc.ErrNotFound) instead of inspecting status.Code
// directly. The original status message is preserved, and the returned
// error also implements GRPCStatus so status.Code(result) and
// status.FromError(result) keep reporting the original status code (and
// any status Details) instead of codes.Unknown.
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
	return &translatedError{st: st, sentinel: sentinel}
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
