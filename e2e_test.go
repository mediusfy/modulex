package modulex_test

// This file exercises modulex end-to-end the way a real service would wire
// it: a Manager with OpenTelemetry tracing, a Chi router, an httpx-served
// HTTP server exposing a business route plus health/readiness checks, a
// watermill-backed EventBus, and a supervised background task — composed by
// one Module and driven through the full Init -> Start -> exercise -> Stop
// lifecycle. Unlike example_test.go's single-concern Examples, this test
// asserts that every adapter package actually cooperates through the public
// Manager API in one realistic run.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	gochi "github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/mediusfy/modulex"
	modulexchi "github.com/mediusfy/modulex/chi"
	"github.com/mediusfy/modulex/httpx"
	modulexotel "github.com/mediusfy/modulex/otel"
	"github.com/mediusfy/modulex/watermill"
)

// e2eModule wires a business HTTP route, a health check, a readiness check,
// an EventBus subscriber, and a supervised background task through the
// Registry — mirroring what a real feature module does in Init/Start/Stop.
type e2eModule struct {
	receivedEvents chan string
}

func (m *e2eModule) Name() string        { return "e2e-module" }
func (m *e2eModule) DependsOn() []string { return nil }

func (m *e2eModule) Init(ctx context.Context, reg modulex.Registry) error {
	router, err := modulexchi.ResolveRouter(reg)
	if err != nil {
		return err
	}
	router.Get("/greet/{name}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"greeting": "hello " + gochi.URLParam(r, "name"),
		})
	})

	if err := reg.RegisterHealthCheck("e2e", func(context.Context) error { return nil }); err != nil {
		return err
	}
	if err := reg.RegisterReadinessCheck("e2e", func(context.Context) error { return nil }); err != nil {
		return err
	}

	return reg.EventBus().Subscribe(ctx, "e2e.events", func(_ context.Context, payload []byte) error {
		m.receivedEvents <- string(payload)
		return nil
	})
}

func (m *e2eModule) Start(ctx context.Context) error {
	return nil
}

func (m *e2eModule) Stop(ctx context.Context) error {
	return nil
}

// freeAddr reserves an ephemeral TCP port and returns "host:port" for the
// httpx.Serve-managed *http.Server to bind. There is a small unavoidable
// race between releasing the listener and the server rebinding it, which is
// an accepted tradeoff for a test wanting a real listening port.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func TestEndToEnd_FullStackLifecycle(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	eventBus := watermill.NewEventBus(0, false, false)

	manager, err := modulex.NewManager(
		modulex.WithLogger(logger),
		modulex.WithTracer(modulexotel.NewTracer(tp)),
		modulex.WithEventBus(eventBus),
	)
	require.NoError(t, err)

	router := gochi.NewRouter()
	require.NoError(t, modulexchi.RegisterRouter(manager, router))
	router.Get("/healthz", httpx.HealthHandler(manager))
	router.Get("/readyz", httpx.ReadinessHandler(manager))

	mod := &e2eModule{receivedEvents: make(chan string, 1)}
	require.NoError(t, manager.RegisterModule(mod))

	ctx := context.Background()
	require.NoError(t, manager.InitModules(ctx))
	require.NoError(t, manager.StartModules(ctx))
	assert.Equal(t, modulex.StateRunning, manager.State())

	addr := freeAddr(t)
	httpServer := &http.Server{Addr: addr, Handler: router}
	_, err = httpx.Serve(ctx, manager, "http-server", httpServer, 5*time.Second)
	require.NoError(t, err)

	baseURL := "http://" + addr
	client := &http.Client{}
	require.Eventually(t, func() bool {
		resp, err := client.Get(baseURL + "/healthz")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == http.StatusOK
	}, 2*time.Second, 10*time.Millisecond, "server did not start listening in time")

	t.Run("business route resolves the chi-registered handler", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/greet/modulex")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		var body map[string]string
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "hello modulex", body["greeting"])
	})

	t.Run("health and readiness checks are exposed over HTTP", func(t *testing.T) {
		for _, path := range []string{"/healthz", "/readyz"} {
			resp, err := client.Get(baseURL + path)
			require.NoError(t, err)
			_ = resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode, "GET %s", path)
		}
	})

	t.Run("event published through the registry EventBus reaches the module's subscriber", func(t *testing.T) {
		require.NoError(t, manager.EventBus().Publish(ctx, "e2e.events", []byte("order-created")))

		select {
		case got := <-mod.receivedEvents:
			assert.Equal(t, "order-created", got)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for published event to reach subscriber")
		}
	})

	t.Run("supervised background task runs and is awaited on shutdown", func(t *testing.T) {
		taskRan := make(chan struct{})
		handle, err := manager.Go(ctx, "e2e-background-task", func(context.Context) error {
			close(taskRan)
			return nil
		})
		require.NoError(t, err)

		select {
		case <-taskRan:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for background task to run")
		}
		require.NoError(t, handle.Wait())
	})

	// Close the client's pooled connections before shutting down. Since Go
	// 1.27 the client reuses keep-alive connections more aggressively, which
	// can leave a spare, never-used connection in its pool; server-side that
	// connection sits in StateNew, and Server.Shutdown only reaps StateNew
	// connections after a hardcoded ~5s grace -- longer than Serve's
	// shutdown window here, so shutdown would flakily time out.
	client.CloseIdleConnections()
	require.NoError(t, manager.StopModules(ctx))
	assert.Equal(t, modulex.StateStopped, manager.State())

	// The HTTP server was shut down by StopModules cancelling the supervised
	// task; a fresh request must fail rather than hang.
	_, err = client.Get(baseURL + "/healthz")
	assert.Error(t, err, "server should no longer be listening after StopModules")

	spanNames := make(map[string]struct{})
	for _, s := range sr.Ended() {
		spanNames[s.Name()] = struct{}{}
	}
	assert.Contains(t, spanNames, "InitModules")
	assert.Contains(t, spanNames, fmt.Sprintf("InitModule:%s", mod.Name()))
	assert.Contains(t, spanNames, "StopModules")
}
