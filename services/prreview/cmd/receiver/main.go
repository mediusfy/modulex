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
	"os"
	"strconv"
	"time"

	cloudtasks "cloud.google.com/go/cloudtasks/apiv2"

	natsadapter "github.com/mediusfy/modulex/nats"
	"github.com/mediusfy/modulex/services/prreview/internal/bootstrap"
	"github.com/mediusfy/modulex/services/prreview/queue"
	"github.com/mediusfy/modulex/services/prreview/ratelimit"
	"github.com/mediusfy/modulex/services/prreview/receiver"
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
		Secret:   []byte(bootstrap.MustEnv(log, "WEBHOOK_SECRET")),
		Dedup:    bootstrap.OpenStore(ctx, log),
		DedupTTL: 24 * time.Hour,
		Queue:    newEnqueuer(ctx, log),
		Limiter:  ratelimit.New(limit, time.Hour),
		Log:      log,
	}

	mux := bootstrap.HealthMux()
	mux.Handle("/webhook", h)
	bootstrap.Serve(mux, log, "receiver listening")
}

// newEnqueuer selects the queue backend per QUEUE_BACKEND.
func newEnqueuer(ctx context.Context, log *slog.Logger) queue.Enqueuer {
	switch backend := bootstrap.EnvOr("QUEUE_BACKEND", "cloudtasks"); backend {
	case "cloudtasks":
		tasks, err := cloudtasks.NewClient(ctx)
		if err != nil {
			bootstrap.Fatal(log, "cloud tasks client", err)
		}
		return &queue.CloudTasks{
			Client:                tasks,
			QueuePath:             bootstrap.MustEnv(log, "TASKS_QUEUE_PATH"),
			WorkerURL:             bootstrap.MustEnv(log, "WORKER_URL"),
			InvokerServiceAccount: bootstrap.MustEnv(log, "INVOKER_SERVICE_ACCOUNT"),
		}
	case "nats":
		js := bootstrap.ConnectJetStream(log)
		return &queue.NATS{Bus: natsadapter.NewJetStreamEventBus(js, natsadapter.WithJetStreamLogger(log))}
	default:
		log.Error("unknown QUEUE_BACKEND", bootstrap.LogKeyBackend, backend)
		os.Exit(1)
		return nil
	}
}
