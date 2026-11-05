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
			// The default (non-detailed) mode replaces the check's real
			// error text with a generic message — see
			// TestHealthHandlerDefaultHidesCheckErrorDetail and
			// TestHealthHandlerWithDetailedErrorsExposesCheckErrorDetail for
			// the dedicated coverage of that behavior (M9).
			name: "one check fails",
			setup: func(t *testing.T, manager *modulex.Manager) {
				require.NoError(t, manager.RegisterHealthCheck("db", func(context.Context) error { return nil }))
				require.NoError(t, manager.RegisterHealthCheck("cache", func(context.Context) error {
					return errors.New("connection refused")
				}))
			},
			wantCode:   http.StatusServiceUnavailable,
			wantStatus: "unhealthy",
			wantChecks: map[string]string{"db": "ok", "cache": "failed"},
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
			// See the comment on TestHealthHandler's "one check fails" case:
			// the default (non-detailed) mode reports a generic message,
			// not the check's real error text (M9).
			name: "one check fails",
			setup: func(t *testing.T, manager *modulex.Manager) {
				require.NoError(t, manager.RegisterReadinessCheck("db-pool-warm", func(context.Context) error {
					return errors.New("pool not warm")
				}))
			},
			wantCode:   http.StatusServiceUnavailable,
			wantStatus: "not-ready",
			wantChecks: map[string]string{"db-pool-warm": "failed"},
		},
	})
}

// TestHealthHandlerCheckErrorDetail covers the M9 fix: by default, a
// failing check's real error text (which may embed hostnames, DSNs, file
// paths, or SQL fragments from a wrapped driver error) is not written into
// the JSON response body reachable by an unauthenticated caller; the
// WithDetailedErrors escape hatch restores the real text for callers who
// explicitly want it (e.g. an internal-only endpoint).
func TestHealthHandlerCheckErrorDetail(t *testing.T) {
	const sensitive = "dial tcp 10.0.0.5:5432: connection refused (dsn=postgres://user:pw@10.0.0.5/db)"
	tests := []struct {
		name         string
		checkErr     string
		opts         []httpx.HandlerOption
		wantDB       string
		wantStatus   string
		notContained string
	}{
		{
			name:         "default hides check error detail",
			checkErr:     sensitive,
			wantDB:       "failed",
			wantStatus:   "unhealthy",
			notContained: sensitive,
		},
		{
			name:     "WithDetailedErrors exposes check error detail",
			checkErr: "connection refused",
			opts:     []httpx.HandlerOption{httpx.WithDetailedErrors()},
			wantDB:   "connection refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := newTestManager(t)
			require.NoError(t, manager.RegisterHealthCheck("db", func(context.Context) error {
				return errors.New(tt.checkErr)
			}))

			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			rec := httptest.NewRecorder()

			httpx.HealthHandler(manager, tt.opts...)(rec, req)

			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
			body := decodeBody(t, rec)
			if tt.wantStatus != "" {
				assert.Equal(t, tt.wantStatus, body["status"])
			}
			checks, ok := body["checks"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, tt.wantDB, checks["db"])
			if tt.notContained != "" {
				assert.NotContains(t, rec.Body.String(), tt.notContained)
			}
		})
	}
}

func TestHealthHandlerRespectsCallerDeadline(t *testing.T) {
	tests := []struct {
		name       string
		timeout    time.Duration
		wantStatus string
	}{
		{name: "slow check bounded by caller deadline", timeout: 20 * time.Millisecond, wantStatus: "unhealthy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := newTestManager(t)
			started := make(chan struct{})
			require.NoError(t, manager.RegisterHealthCheck("slow", func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}))

			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			ctx, cancel := context.WithTimeout(req.Context(), tt.timeout)
			defer cancel()
			req = req.WithContext(ctx)
			rec := httptest.NewRecorder()

			httpx.HealthHandler(manager)(rec, req)
			<-started

			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
			body := decodeBody(t, rec)
			assert.Equal(t, tt.wantStatus, body["status"])
		})
	}
}

func TestServe(t *testing.T) {
	tests := []struct {
		name       string
		serverName string
	}{
		{name: "serves requests and shuts down via StopModules", serverName: "http-server"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

			handle, err := httpx.Serve(context.Background(), manager, tt.serverName, server, 2*time.Second)
			require.NoError(t, err)
			require.Equal(t, tt.serverName, handle.Name())

			client := &http.Client{Timeout: time.Second}
			require.Eventually(t, func() bool {
				resp, err := client.Get("http://" + addr + "/ping")
				if err != nil {
					return false
				}
				defer func() { _ = resp.Body.Close() }()
				return resp.StatusCode == http.StatusOK
			}, 2*time.Second, 10*time.Millisecond, "server did not become reachable")

			// Since Go 1.27 the client can leave a spare, never-used keep-alive
			// connection in its pool; server-side it sits in StateNew, which
			// Server.Shutdown only reaps after a hardcoded ~5s grace -- longer than
			// the 2s shutdown window here. Empty the pool so shutdown is prompt.
			client.CloseIdleConnections()

			waitErr := make(chan error, 1)
			go func() { waitErr <- handle.Wait() }()

			// Shutdown is driven by the task ctx the spawner owns, not by any ctx
			// passed into Serve, so it is triggered here via StopModules (which
			// cancels the manager's task ctx) rather than cancelling a local ctx.
			require.NoError(t, manager.StopModules(context.Background()))

			select {
			case err := <-waitErr:
				assert.NoError(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("Serve did not shut down within the expected time")
			}
		})
	}
}

// TestServeIgnoresCallerContextCancellation proves the fix for M8: the ctx
// passed into Serve (typically a short, bounded context such as the
// caller's Init/Start timeout) no longer controls the server's shutdown.
// Only the long-lived task ctx the spawner owns (cancelled via
// Manager.StopModules) does. Before the fix, cancelling ctx alone shut the
// server down immediately.
func TestServeIgnoresCallerContextCancellation(t *testing.T) {
	tests := []struct {
		name       string
		serverName string
	}{
		{name: "caller ctx cancellation does not stop server", serverName: "http-server-ctx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			addr := ln.Addr().String()
			require.NoError(t, ln.Close())

			server := &http.Server{Addr: addr, Handler: http.NewServeMux()}

			manager := newTestManager(t)
			ctx, cancel := context.WithCancel(context.Background())

			handle, err := httpx.Serve(ctx, manager, tt.serverName, server, 2*time.Second)
			require.NoError(t, err)

			waitErr := make(chan error, 1)
			go func() { waitErr <- handle.Wait() }()

			cancel()

			select {
			case err := <-waitErr:
				t.Fatalf("Serve shut down after the caller ctx alone was cancelled, err=%v", err)
			case <-time.After(200 * time.Millisecond):
			}

			require.NoError(t, manager.StopModules(context.Background()))

			select {
			case err := <-waitErr:
				assert.NoError(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("Serve did not shut down after StopModules")
			}
		})
	}
}

// TestServeStartFailure proves (for its "listen error" case) the fix for M8: a bind
// failure (here, a second listener on an address already bound by ln)
// must surface directly from the Serve call itself, not only later as a
// task error collected once Manager.StopModules drains task errors. Before
// the fix, this call returned (nil error, a non-nil handle) and the bind
// failure only appeared once handle.Wait() was called, which let
// Manager.StartModules report success while nothing was actually
// listening.
func TestServeStartFailure(t *testing.T) {
	tests := []struct {
		name       string
		serverName string
		occupied   bool
		nilServer  bool
	}{
		{name: "rejects nil server", serverName: "nil-server", nilServer: true},
		{name: "treats listen error as failure", serverName: "conflicting-server", occupied: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var server *http.Server
			if tt.occupied {
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				defer func() { _ = ln.Close() }()

				// Binding a second server to the same address forces
				// net.Listen to fail immediately.
				server = &http.Server{Addr: ln.Addr().String(), Handler: http.NewServeMux()}
			}

			manager := newTestManager(t)
			handle, err := httpx.Serve(context.Background(), manager, tt.serverName, server, time.Second)

			require.Error(t, err)
			assert.Nil(t, handle)
		})
	}
}
