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

// TestUnaryServerErrorInterceptorStatusAndMessage covers the status code and
// message sent to the caller:
//   - an error that already carries a status is passed through unchanged
//     (the mapping must not be consulted);
//   - a handler error mapping to codes.Internal is hidden by default (M9),
//     with the real text logged server-side instead;
//   - WithExposeInternalErrors restores sending the real text;
//   - a client-safe code such as codes.NotFound keeps its real message;
//   - a nil mapping falls back to DefaultErrorMapping.
func TestUnaryServerErrorInterceptorStatusAndMessage(t *testing.T) {
	const sensitive = "dial tcp 10.0.0.5:5432: connection refused (dsn=postgres://user:pw@10.0.0.5/db)"

	tests := []struct {
		name           string
		mapping        grpcadapter.ErrorMapping
		opts           []grpcadapter.ServerErrorInterceptorOption
		handlerErr     error
		wantCode       codes.Code
		wantMessage    string // checked only when non-empty
		wantNotContain string // checked only when non-empty
	}{
		{
			name:       "passes through existing status",
			mapping:    widgetMapping,
			handlerErr: status.Error(codes.PermissionDenied, "already a status error"),
			wantCode:   codes.PermissionDenied,
		},
		{
			name:           "hides internal error detail by default",
			mapping:        widgetMapping,
			handlerErr:     errors.New(sensitive),
			wantCode:       codes.Internal,
			wantMessage:    "internal error",
			wantNotContain: sensitive,
		},
		{
			name:        "WithExposeInternalErrors sends real text",
			mapping:     widgetMapping,
			opts:        []grpcadapter.ServerErrorInterceptorOption{grpcadapter.WithExposeInternalErrors()},
			handlerErr:  errors.New("boom"),
			wantCode:    codes.Internal,
			wantMessage: "boom",
		},
		{
			name:        "keeps client-safe error detail",
			mapping:     widgetMapping,
			handlerErr:  errDomainNotFound,
			wantCode:    codes.NotFound,
			wantMessage: errDomainNotFound.Error(),
		},
		{
			name:       "defaults mapping when nil",
			mapping:    nil,
			handlerErr: errors.New("boom"),
			wantCode:   codes.Internal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interceptor := grpcadapter.UnaryServerErrorInterceptor(tt.mapping, tt.opts...)
			handler := func(ctx context.Context, req any) (any, error) {
				return nil, tt.handlerErr
			}
			_, err := interceptor(context.Background(), nil, &googlegrpc.UnaryServerInfo{}, handler)

			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, tt.wantCode, st.Code())
			if tt.wantNotContain != "" {
				assert.NotContains(t, st.Message(), tt.wantNotContain)
			}
			if tt.wantMessage != "" {
				assert.Equal(t, tt.wantMessage, st.Message())
			}
		})
	}
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

// TestTranslateErrorPreservesStatus proves the fix for L6: the error
// TranslateError returns must still report its original status code via
// status.Code/status.FromError (not codes.Unknown, what a plain
// fmt.Errorf("%w")-wrapped error would report), and any status Details
// attached to the original error remain reachable.
func TestTranslateErrorPreservesStatus(t *testing.T) {
	withDetails, err := status.New(codes.InvalidArgument, "bad name").
		WithDetails(&durationpb.Duration{Seconds: 5})
	require.NoError(t, err)

	tests := []struct {
		name         string
		original     error
		wantSentinel error
		wantCode     codes.Code
		wantMessage  string
		wantSeconds  []int64 // expected durationpb details, in order
	}{
		{
			name:         "status code and message preserved",
			original:     status.Error(codes.NotFound, "widget missing"),
			wantSentinel: grpcadapter.ErrNotFound,
			wantCode:     codes.NotFound,
			wantMessage:  "widget missing",
		},
		{
			name:         "status details preserved",
			original:     withDetails.Err(),
			wantSentinel: grpcadapter.ErrInvalidInput,
			wantCode:     codes.InvalidArgument,
			wantMessage:  "bad name",
			wantSeconds:  []int64{5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grpcadapter.TranslateError(tt.original)

			require.Error(t, got)
			assert.ErrorIs(t, got, tt.wantSentinel)
			assert.Equal(t, tt.wantCode, status.Code(got))

			st, ok := status.FromError(got)
			require.True(t, ok, "status.FromError must still recognize the translated error")
			assert.Equal(t, tt.wantCode, st.Code())
			assert.Equal(t, tt.wantMessage, st.Message())

			details := st.Details()
			require.Len(t, details, len(tt.wantSeconds))
			for i, want := range tt.wantSeconds {
				d, ok := details[i].(*durationpb.Duration)
				require.True(t, ok)
				assert.Equal(t, want, d.GetSeconds())
			}
		})
	}
}

func TestUnaryClientErrorInterceptorTranslatesError(t *testing.T) {
	tests := []struct {
		name       string
		invokerErr error
		wantIs     error
	}{
		{name: "not found status is translated", invokerErr: status.Error(codes.NotFound, "widget missing"), wantIs: grpcadapter.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interceptor := grpcadapter.UnaryClientErrorInterceptor()
			invoker := func(ctx context.Context, method string, req, reply any, cc *googlegrpc.ClientConn, opts ...googlegrpc.CallOption) error {
				return tt.invokerErr
			}
			err := interceptor(context.Background(), "/svc/Method", nil, nil, nil, invoker)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantIs)
		})
	}
}
