// Command receiver is the Cloud Run webhook receiver (Jira MOD-83). It
// runs with min-instances 0; GitHub's ~10s webhook budget is honored by
// acking after dedup + enqueue, never after a review.
//
// Environment:
//
//	PORT                   listen port (Cloud Run sets this)
//	WEBHOOK_SECRET         GitHub App webhook secret (inject via Secret
//	                       Manager secret env var, never a plain env value)
//	GOOGLE_CLOUD_PROJECT   Firestore/Tasks project
//	TASKS_QUEUE_PATH       projects/{p}/locations/{l}/queues/{q}
//	WORKER_URL             worker Cloud Run task endpoint
//	INVOKER_SERVICE_ACCOUNT service account email for the OIDC task token
//	RATE_LIMIT_PER_HOUR    per-installation accepted deliveries (default 60)
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

	"github.com/mediusfy/modulex/services/prreview/queue"
	"github.com/mediusfy/modulex/services/prreview/ratelimit"
	"github.com/mediusfy/modulex/services/prreview/receiver"
	"github.com/mediusfy/modulex/services/prreview/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx := context.Background()

	project := mustEnv(log, "GOOGLE_CLOUD_PROJECT")
	fs, err := firestore.NewClient(ctx, project)
	if err != nil {
		log.Error("firestore client", "error", err)
		os.Exit(1)
	}
	tasks, err := cloudtasks.NewClient(ctx)
	if err != nil {
		log.Error("cloud tasks client", "error", err)
		os.Exit(1)
	}

	limit := 60
	if v := os.Getenv("RATE_LIMIT_PER_HOUR"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}

	h := &receiver.Handler{
		Secret:   []byte(mustEnv(log, "WEBHOOK_SECRET")),
		Dedup:    &store.Firestore{Client: fs},
		DedupTTL: 24 * time.Hour,
		Queue: &queue.CloudTasks{
			Client:                tasks,
			QueuePath:             mustEnv(log, "TASKS_QUEUE_PATH"),
			WorkerURL:             mustEnv(log, "WORKER_URL"),
			InvokerServiceAccount: mustEnv(log, "INVOKER_SERVICE_ACCOUNT"),
		},
		Limiter: ratelimit.New(limit, time.Hour),
		Log:     log,
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
	log.Info("receiver listening", "port", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Error("serve", "error", err)
		os.Exit(1)
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
