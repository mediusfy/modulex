// Package bootstrap holds the process start-up helpers shared by the
// receiver and worker commands: environment lookup, backend selection for
// the store and the NATS JetStream connection, and the hardened HTTP
// server. Every helper logs and exits the process on failure, which is the
// right behavior for a main package's wiring and keeps both mains linear.
package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/nats-io/nats.go"

	"github.com/mediusfy/modulex/services/prreview/queue"
	"github.com/mediusfy/modulex/services/prreview/store"
)

// Structured-log attribute keys.
const (
	LogKeyBackend = "backend"
	LogKeyError   = "error"
	LogKeyName    = "name"
	LogKeyPort    = "port"
)

// Store is the union of store capabilities the commands need from
// whichever backend STORE_BACKEND selects; store.Firestore and
// store.Postgres both satisfy it.
type Store interface {
	store.DedupStore
	store.JobStore
	store.Ledger
}

// Fatal logs the error under msg and exits with status 1.
func Fatal(log *slog.Logger, msg string, err error) {
	log.Error(msg, LogKeyError, err)
	os.Exit(1)
}

// MustEnv returns the named environment variable or exits if it is unset
// or empty.
func MustEnv(log *slog.Logger, name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Error("missing required environment variable", LogKeyName, name)
		os.Exit(1)
	}
	return v
}

// EnvOr returns the named environment variable, or def if it is unset or
// empty.
func EnvOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// OpenStore selects the store backend per STORE_BACKEND.
func OpenStore(ctx context.Context, log *slog.Logger) Store {
	switch backend := EnvOr("STORE_BACKEND", "firestore"); backend {
	case "firestore":
		project := MustEnv(log, "GOOGLE_CLOUD_PROJECT")
		fs, err := firestore.NewClient(ctx, project)
		if err != nil {
			Fatal(log, "firestore client", err)
		}
		return &store.Firestore{Client: fs}
	case "postgres":
		pg, err := store.NewPostgres(MustEnv(log, "POSTGRES_DSN"))
		if err != nil {
			Fatal(log, "postgres open", err)
		}
		return pg
	default:
		log.Error("unknown STORE_BACKEND", LogKeyBackend, backend)
		os.Exit(1)
		return nil
	}
}

// ConnectJetStream connects to NATS_URL and ensures the NATS_STREAM
// review stream exists.
func ConnectJetStream(log *slog.Logger) nats.JetStreamContext {
	conn, err := nats.Connect(EnvOr("NATS_URL", nats.DefaultURL))
	if err != nil {
		Fatal(log, "nats connect", err)
	}
	js, err := conn.JetStream()
	if err != nil {
		Fatal(log, "nats jetstream", err)
	}
	stream := EnvOr("NATS_STREAM", "PRREVIEW_REVIEWS")
	if err := queue.EnsureStream(js, stream, queue.ReviewsStreamSubjects...); err != nil {
		Fatal(log, "nats ensure stream", err)
	}
	return js
}

// HealthMux returns a mux serving only /health.
func HealthMux() *http.ServeMux {
	mux := http.NewServeMux()
	AddHealth(mux)
	return mux
}

// AddHealth registers the /health endpoint on mux.
func AddHealth(mux *http.ServeMux) {
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// Serve listens on $PORT (default 8080) with explicit server timeouts and
// exits if the server stops. msg and attrs are logged once on start, after
// the port attribute.
func Serve(mux *http.ServeMux, log *slog.Logger, msg string, attrs ...any) {
	port := EnvOr("PORT", "8080")
	// Explicit server timeouts: the default http.Server has none, which
	// leaves the endpoint open to slowloris-style connection exhaustion.
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	log.Info(msg, append([]any{LogKeyPort, port}, attrs...)...)
	if err := server.ListenAndServe(); err != nil {
		Fatal(log, "serve", err)
	}
}
