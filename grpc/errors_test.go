package grpc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	grpcadapter "github.com/mediusfy/modulex/grpc"
)

var errDomainNotFound = errors.New("widget not found")
var errDomainInvalid = errors.New("widget name must not be empty")

func widgetMapping(err error) codes.Code {
	switch {
	case errors.Is(err, errDomainNotFound):
		return codes.NotFound
	case errors.Is(err, errDomainInvalid):
		return codes.InvalidArgument
	default:
		return grpcadapter.DefaultErrorMapping(err)
	}
}

func TestUnaryServerErrorInterceptorMapsDomainErrors(t *testing.T) {
	tests := []struct {
		name       string
		handlerErr error
		wantCode   codes.Code
	}{
		{"not found", errDomainNotFound, codes.NotFound},
		{"invalid input", errDomainInvalid, codes.InvalidArgument},
		{"context canceled falls through default mapping", context.Canceled, codes.Canceled},
		{"unmapped error becomes internal", errors.New("boom"), codes.Internal},
		{"nil error passes through", nil, codes.OK},
	}

	interceptor := grpcadapter.UnaryServerErrorInterceptor(widgetMapping)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := func(ctx context.Context, req any) (any, error) {
				return "resp", tt.handlerErr
			}
			resp, err := interceptor(context.Background(), nil, &googlegrpc.UnaryServerInfo{}, handler)

			if tt.wantCode == codes.OK {
				require.NoError(t, err)
				assert.Equal(t, "resp", resp)
				return
			}
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok, "error should carry a gRPC status")
			assert.Equal(t, tt.wantCode, st.Code())
		})
	}
}

func TestUnaryServerErrorInterceptorPassesThroughExistingStatus(t *testing.T) {
	interceptor := grpcadapter.UnaryServerErrorInterceptor(widgetMapping)
	original := status.Error(codes.PermissionDenied, "already a status error")

	handler := func(ctx context.Context, req any) (any, error) {
		return nil, original
	}
	_, err := interceptor(context.Background(), nil, &googlegrpc.UnaryServerInfo{}, handler)

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	// The mapping function must not be consulted for an error that already
	// carries a status: its code must be preserved exactly.
	assert.Equal(t, codes.PermissionDenied, st.Code())
}

// TestUnaryServerErrorInterceptorHidesInternalErrorDetailByDefault proves
// the fix for M9: a handler error that maps to codes.Internal (the usual
// catch-all for an error ErrorMapping doesn't specifically recognize, and
// so exactly where a wrapped driver/library error commonly ends up) is not
// sent to the caller verbatim by default. The real text is logged
// server-side instead.
func TestUnaryServerErrorInterceptorHidesInternalErrorDetailByDefault(t *testing.T) {
	interceptor := grpcadapter.UnaryServerErrorInterceptor(widgetMapping)
	sensitive := "dial tcp 10.0.0.5:5432: connection refused (dsn=postgres://user:pw@10.0.0.5/db)"
	handler := func(ctx context.Context, req any) (any, error) {
		return nil, errors.New(sensitive)
	}
	_, err := interceptor(context.Background(), nil, &googlegrpc.UnaryServerInfo{}, handler)

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
	assert.NotContains(t, st.Message(), sensitive)
	assert.Equal(t, "internal error", st.Message())
}

// TestUnaryServerErrorInterceptorWithExposeInternalErrors proves the
// opt-in escape hatch added alongside the M9 fix: WithExposeInternalErrors
// restores the previous behavior of sending the handler error's real text
// even for codes.Internal.
func TestUnaryServerErrorInterceptorWithExposeInternalErrors(t *testing.T) {
	interceptor := grpcadapter.UnaryServerErrorInterceptor(widgetMapping, grpcadapter.WithExposeInternalErrors())
	handler := func(ctx context.Context, req any) (any, error) {
		return nil, errors.New("boom")
	}
	_, err := interceptor(context.Background(), nil, &googlegrpc.UnaryServerInfo{}, handler)

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
	assert.Equal(t, "boom", st.Message())
}

// TestUnaryServerErrorInterceptorKeepsClientSafeErrorDetail proves M9's
// fix is scoped to codes.Internal/Unknown only: a domain error mapped to a
// genuinely client-actionable code (e.g. codes.NotFound, deliberately
// classified by the service's own ErrorMapping rather than falling back
// to "anything else") still carries its real message by default.
func TestUnaryServerErrorInterceptorKeepsClientSafeErrorDetail(t *testing.T) {
	interceptor := grpcadapter.UnaryServerErrorInterceptor(widgetMapping)
	handler := func(ctx context.Context, req any) (any, error) {
		return nil, errDomainNotFound
	}
	_, err := interceptor(context.Background(), nil, &googlegrpc.UnaryServerInfo{}, handler)

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
	assert.Equal(t, errDomainNotFound.Error(), st.Message())
}

func TestUnaryServerErrorInterceptorDefaultsMappingWhenNil(t *testing.T) {
	interceptor := grpcadapter.UnaryServerErrorInterceptor(nil)
	handler := func(ctx context.Context, req any) (any, error) {
		return nil, errors.New("boom")
	}
	_, err := interceptor(context.Background(), nil, &googlegrpc.UnaryServerInfo{}, handler)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
}

func TestTranslateError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantIs   error
		wantNil  bool
		wantSame bool // err has no status; TranslateError should return it unchanged
	}{
		{name: "nil", err: nil, wantNil: true},
		{name: "ok status", err: status.Error(codes.OK, "fine"), wantNil: true},
		{name: "not found", err: status.Error(codes.NotFound, "widget missing"), wantIs: grpcadapter.ErrNotFound},
		{name: "invalid argument", err: status.Error(codes.InvalidArgument, "bad name"), wantIs: grpcadapter.ErrInvalidInput},
		{name: "already exists", err: status.Error(codes.AlreadyExists, "dup"), wantIs: grpcadapter.ErrAlreadyExists},
		{name: "permission denied", err: status.Error(codes.PermissionDenied, "nope"), wantIs: grpcadapter.ErrPermissionDenied},
		{name: "unauthenticated", err: status.Error(codes.Unauthenticated, "who"), wantIs: grpcadapter.ErrUnauthenticated},
		{name: "unavailable", err: status.Error(codes.Unavailable, "down"), wantIs: grpcadapter.ErrUnavailable},
		{name: "deadline exceeded", err: status.Error(codes.DeadlineExceeded, "slow"), wantIs: grpcadapter.ErrDeadlineExceeded},
		{name: "canceled", err: status.Error(codes.Canceled, "stop"), wantIs: grpcadapter.ErrCanceled},
		{name: "unmapped code becomes internal", err: status.Error(codes.Unknown, "???"), wantIs: grpcadapter.ErrInternal},
		{name: "no status is returned unchanged", err: errors.New("dial tcp: connection refused"), wantSame: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grpcadapter.TranslateError(tt.err)
			switch {
			case tt.wantNil:
				assert.NoError(t, got)
			case tt.wantSame:
				assert.Equal(t, tt.err, got)
			default:
				require.Error(t, got)
				assert.ErrorIs(t, got, tt.wantIs)
			}
		})
	}
}

// TestTranslateErrorPreservesStatusCode proves the fix for L6: the error
// TranslateError returns must still report its original status code via
// status.Code/status.FromError, not codes.Unknown (what a plain
// fmt.Errorf("%w")-wrapped error with no GRPCStatus method would report).
func TestTranslateErrorPreservesStatusCode(t *testing.T) {
	original := status.Error(codes.NotFound, "widget missing")
	got := grpcadapter.TranslateError(original)

	require.Error(t, got)
	assert.ErrorIs(t, got, grpcadapter.ErrNotFound)

	// status.Code must report the original code, not codes.Unknown.
	assert.Equal(t, codes.NotFound, status.Code(got))

	st, ok := status.FromError(got)
	require.True(t, ok, "status.FromError must still recognize the translated error")
	assert.Equal(t, codes.NotFound, st.Code())
	assert.Equal(t, "widget missing", st.Message())
}

// TestTranslateErrorPreservesStatusDetails proves the fix for L6: any
// status Details attached to the original error survive translation and
// remain reachable via status.FromError on the result.
func TestTranslateErrorPreservesStatusDetails(t *testing.T) {
	st := status.New(codes.InvalidArgument, "bad name")
	stWithDetails, err := st.WithDetails(&durationpb.Duration{Seconds: 5})
	require.NoError(t, err)

	got := grpcadapter.TranslateError(stWithDetails.Err())

	require.Error(t, got)
	assert.ErrorIs(t, got, grpcadapter.ErrInvalidInput)

	gotSt, ok := status.FromError(got)
	require.True(t, ok)
	details := gotSt.Details()
	require.Len(t, details, 1)
	gotDuration, ok := details[0].(*durationpb.Duration)
	require.True(t, ok)
	assert.Equal(t, int64(5), gotDuration.GetSeconds())
}

func TestUnaryClientErrorInterceptorTranslatesError(t *testing.T) {
	interceptor := grpcadapter.UnaryClientErrorInterceptor()
	invoker := func(ctx context.Context, method string, req, reply any, cc *googlegrpc.ClientConn, opts ...googlegrpc.CallOption) error {
		return status.Error(codes.NotFound, "widget missing")
	}
	err := interceptor(context.Background(), "/svc/Method", nil, nil, nil, invoker)
	require.Error(t, err)
	assert.ErrorIs(t, err, grpcadapter.ErrNotFound)
}
