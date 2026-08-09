package httpx_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/httpx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func newTestManager(t *testing.T) *modulex.Manager {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr, err := modulex.NewManager(modulex.WithLogger(logger))
	require.NoError(t, err)
	return mgr
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

// checkHandlerCase is a table-driven case shared by TestHealthHandler and
// TestReadinessHandler, which exercise HealthHandler and ReadinessHandler
// through the same request/assert shape and differ only in which checks
// they register and which status strings the handler reports.
type checkHandlerCase struct {
	name       string
	setup      func(t *testing.T, manager *modulex.Manager)
	wantCode   int
	wantStatus string
	wantChecks map[string]string
}

func runCheckHandlerTests(t *testing.T, path string, handler func(*modulex.Manager) http.HandlerFunc, tests []checkHandlerCase) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := newTestManager(t)
			tt.setup(t, manager)

			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()

			handler(manager)(rec, req)

			assert.Equal(t, tt.wantCode, rec.Code)
			body := decodeBody(t, rec)
			assert.Equal(t, tt.wantStatus, body["status"])

			gotChecks, ok := body["checks"].(map[string]any)
			require.True(t, ok)
			assert.Len(t, gotChecks, len(tt.wantChecks))
			for name, want := range tt.wantChecks {
				assert.Equal(t, want, gotChecks[name])
			}
		})
	}
}

func TestHealthHandler(t *testing.T) {
	runCheckHandlerTests(t, "/healthz", func(m *modulex.Manager) http.HandlerFunc { return httpx.HealthHandler(m) }, []checkHandlerCase{
		{
			name:       "no checks registered",
			setup:      func(t *testing.T, manager *modulex.Manager) {},
			wantCode:   http.StatusOK,
			wantStatus: "ok",
			wantChecks: map[string]string{},
		},
		{
			name: "all checks pass",
			setup: func(t *testing.T, manager *modulex.Manager) {
				require.NoError(t, manager.RegisterHealthCheck("db", func(context.Context) error { return nil }))
				require.NoError(t, manager.RegisterHealthCheck("cache", func(context.Context) error { return nil }))
			},
			wantCode:   http.StatusOK,
			wantStatus: "ok",
			wantChecks: map[string]string{"db": "ok", "cache": "ok"},
		},
		{
			name: "one check fails",
			setup: func(t *testing.T, manager *modulex.Manager) {
				require.NoError(t, manager.RegisterHealthCheck("db", func(context.Context) error { return nil }))
				require.NoError(t, manager.RegisterHealthCheck("cache", func(context.Context) error {
					return errors.New("connection refused")
				}))
			},
			wantCode:   http.StatusServiceUnavailable,
			wantStatus: "unhealthy",
			wantChecks: map[string]string{"db": "ok", "cache": "connection refused"},
		},
	})
}

func TestReadinessHandler(t *testing.T) {
	runCheckHandlerTests(t, "/readyz", func(m *modulex.Manager) http.HandlerFunc { return httpx.ReadinessHandler(m) }, []checkHandlerCase{
		{
			name:       "no checks registered",
			setup:      func(t *testing.T, manager *modulex.Manager) {},
			wantCode:   http.StatusOK,
			wantStatus: "ready",
			wantChecks: map[string]string{},
		},
		{
			name: "all checks pass",
			setup: func(t *testing.T, manager *modulex.Manager) {
				require.NoError(t, manager.RegisterReadinessCheck("db-pool-warm", func(context.Context) error { return nil }))
			},
			wantCode:   http.StatusOK,
			wantStatus: "ready",
			wantChecks: map[string]string{"db-pool-warm": "ok"},
		},
		{
			name: "one check fails",
			setup: func(t *testing.T, manager *modulex.Manager) {
				require.NoError(t, manager.RegisterReadinessCheck("db-pool-warm", func(context.Context) error {
					return errors.New("pool not warm")
				}))
			},
			wantCode:   http.StatusServiceUnavailable,
			wantStatus: "not-ready",
			wantChecks: map[string]string{"db-pool-warm": "pool not warm"},
		},
	})
}

func TestHealthHandlerRespectsCallerDeadline(t *testing.T) {
	manager := newTestManager(t)
	started := make(chan struct{})
	require.NoError(t, manager.RegisterHealthCheck("slow", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	ctx, cancel := context.WithTimeout(req.Context(), 20*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	httpx.HealthHandler(manager)(rec, req)
	<-started

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, "unhealthy", body["status"])
}

func TestServe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	server := &http.Server{Addr: addr, Handler: mux}

	manager := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handle, err := httpx.Serve(ctx, manager, "http-server", server, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, "http-server", handle.Name())

	client := &http.Client{Timeout: time.Second}
	require.Eventually(t, func() bool {
		resp, err := client.Get("http://" + addr + "/ping")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == http.StatusOK
	}, 2*time.Second, 10*time.Millisecond, "server did not become reachable")

	cancel()

	waitErr := make(chan error, 1)
	go func() { waitErr <- handle.Wait() }()

	select {
	case err := <-waitErr:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not shut down within the expected time")
	}
}

func TestServePreservesCallerContextValues(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	server := &http.Server{Addr: addr, Handler: http.NewServeMux()}

	manager := newTestManager(t)
	type ctxKey string
	ctx := context.WithValue(context.Background(), ctxKey("caller-key"), "caller-value")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	handle, err := httpx.Serve(ctx, manager, "http-server-values", server, 2*time.Second)
	require.NoError(t, err)

	cancel()

	waitErr := make(chan error, 1)
	go func() { waitErr <- handle.Wait() }()

	select {
	case err := <-waitErr:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not shut down within the expected time")
	}
}

func TestServeRejectsNilServer(t *testing.T) {
	manager := newTestManager(t)

	handle, err := httpx.Serve(context.Background(), manager, "nil-server", nil, time.Second)
	require.Error(t, err)
	assert.Nil(t, handle)
}

func TestServeTreatsListenErrorAsFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	// Binding a second server to the same address forces ListenAndServe to
	// fail immediately with something other than http.ErrServerClosed.
	server := &http.Server{Addr: addr, Handler: http.NewServeMux()}

	manager := newTestManager(t)
	handle, err := httpx.Serve(context.Background(), manager, "conflicting-server", server, time.Second)
	require.NoError(t, err)

	err = handle.Wait()
	assert.Error(t, err)
}
