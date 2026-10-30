// Command receiver is the webhook receiver (Jira MOD-83). On Cloud Run it
// runs with min-instances 0; GitHub's ~10s webhook budget is honored by
// acking after dedup + enqueue, never after a review. On a self-hosted
// k3s cluster (STORE_BACKEND=postgres, QUEUE_BACKEND=nats) it runs as an
// ordinary always-on Deployment instead — scale-to-zero is a GCP-specific
// property of this service, not a requirement of the receiver/worker/
// engine pipeline itself.
//
// Environment:
//
//	PORT                    listen port (Cloud Run sets this)
//	WEBHOOK_SECRET          GitHub App webhook secret (inject via Secret
//	                        Manager secret env var or a k8s Secret, never a
//	                        plain env value)
//	RATE_LIMIT_PER_HOUR     per-installation accepted deliveries (default 60)
//
//	STORE_BACKEND           "firestore" (default) or "postgres"
//	GOOGLE_CLOUD_PROJECT    Firestore project (STORE_BACKEND=firestore)
//	POSTGRES_DSN            Postgres connection string (STORE_BACKEND=postgres);
//	                        apply store/migrations/001_init_schema.sql first
//
//	QUEUE_BACKEND           "cloudtasks" (default) or "nats"
//	TASKS_QUEUE_PATH        projects/{p}/locations/{l}/queues/{q} (QUEUE_BACKEND=cloudtasks)
//	WORKER_URL              worker Cloud Run task endpoint (QUEUE_BACKEND=cloudtasks)
//	INVOKER_SERVICE_ACCOUNT service account email for the OIDC task token (QUEUE_BACKEND=cloudtasks)
//	NATS_URL                NATS server URL (QUEUE_BACKEND=nats; default nats.DefaultURL)
//	NATS_STREAM             JetStream stream name for review tasks (QUEUE_BACKEND=nats; default "PRREVIEW_REVIEWS")
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	cloudtasks "cloud.google.com/go/cloudtasks/apiv2"
	"cloud.google.com/go/firestore"
	"github.com/nats-io/nats.go"

	natsadapter "github.com/mediusfy/modulex/nats"
	"github.com/mediusfy/modulex/services/prreview/queue"
	"github.com/mediusfy/modulex/services/prreview/ratelimit"
	"github.com/mediusfy/modulex/services/prreview/receiver"
	"github.com/mediusfy/modulex/services/prreview/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx := context.Background()

	limit := 60
	if v := os.Getenv("RATE_LIMIT_PER_HOUR"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}

	h := &receiver.Handler{
		Secret:   []byte(mustEnv(log, "WEBHOOK_SECRET")),
		Dedup:    newDedupStore(ctx, log),
		DedupTTL: 24 * time.Hour,
		Queue:    newEnqueuer(ctx, log),
		Limiter:  ratelimit.New(limit, time.Hour),
		Log:      log,
	}

	mux := http.NewServeMux()
	mux.Handle("/webhook", h)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	// Explicit server timeouts: the default http.Server has none, which
	// leaves the endpoint open to slowloris-style connection exhaustion.
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	log.Info("receiver listening", "port", port)
	if err := server.ListenAndServe(); err != nil {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}

// newDedupStore selects the dedup backend per STORE_BACKEND.
// store.Firestore and store.Postgres both satisfy store.DedupStore.
func newDedupStore(ctx context.Context, log *slog.Logger) store.DedupStore {
	switch backend := envOr("STORE_BACKEND", "firestore"); backend {
	case "firestore":
		project := mustEnv(log, "GOOGLE_CLOUD_PROJECT")
		fs, err := firestore.NewClient(ctx, project)
		if err != nil {
			log.Error("firestore client", "error", err)
			os.Exit(1)
		}
		return &store.Firestore{Client: fs}
	case "postgres":
		pg, err := store.NewPostgres(mustEnv(log, "POSTGRES_DSN"))
		if err != nil {
			log.Error("postgres open", "error", err)
			os.Exit(1)
		}
		return pg
	default:
		log.Error("unknown STORE_BACKEND", "backend", backend)
		os.Exit(1)
		return nil
	}
}

// newEnqueuer selects the queue backend per QUEUE_BACKEND.
func newEnqueuer(ctx context.Context, log *slog.Logger) queue.Enqueuer {
	switch backend := envOr("QUEUE_BACKEND", "cloudtasks"); backend {
	case "cloudtasks":
		tasks, err := cloudtasks.NewClient(ctx)
		if err != nil {
			log.Error("cloud tasks client", "error", err)
			os.Exit(1)
		}
		return &queue.CloudTasks{
			Client:                tasks,
			QueuePath:             mustEnv(log, "TASKS_QUEUE_PATH"),
			WorkerURL:             mustEnv(log, "WORKER_URL"),
			InvokerServiceAccount: mustEnv(log, "INVOKER_SERVICE_ACCOUNT"),
		}
	case "nats":
		conn, err := nats.Connect(envOr("NATS_URL", nats.DefaultURL))
		if err != nil {
			log.Error("nats connect", "error", err)
			os.Exit(1)
		}
		js, err := conn.JetStream()
		if err != nil {
			log.Error("nats jetstream", "error", err)
			os.Exit(1)
		}
		stream := envOr("NATS_STREAM", "PRREVIEW_REVIEWS")
		if err := queue.EnsureStream(js, stream, queue.ReviewsStreamSubjects...); err != nil {
			log.Error("nats ensure stream", "error", err)
			os.Exit(1)
		}
		return &queue.NATS{Bus: natsadapter.NewJetStreamEventBus(js, natsadapter.WithJetStreamLogger(log))}
	default:
		log.Error("unknown QUEUE_BACKEND", "backend", backend)
		os.Exit(1)
		return nil
	}
}

func mustEnv(log *slog.Logger, name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Error("missing required environment variable", "name", name)
		os.Exit(1)
	}
	return v
}

// envOr returns the named environment variable, or def if it is unset or
// empty.
func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
