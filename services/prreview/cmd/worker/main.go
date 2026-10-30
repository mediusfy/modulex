// Command worker runs the review pipeline (Jira MOD-84). Under
// QUEUE_BACKEND=cloudtasks it is the Cloud Tasks-dispatched Cloud Run
// worker, pushed to over HTTP and deployed no-allow-unauthenticated (Cloud
// Run's IAM layer verifies the queue's OIDC token before a request reaches
// this process). Under QUEUE_BACKEND=nats it instead pulls review tasks
// itself from a JetStream durable consumer (self-hosted k3s deployments
// that have traded Cloud Tasks for a NATS cluster they already run); it
// still serves /health over HTTP for a k8s liveness/readiness probe, but
// the review pipeline runs on the consume loop, not an HTTP handler.
//
// Environment:
//
//	PORT                    listen port (Cloud Run sets this; also used
//	                        for the /health-only server under QUEUE_BACKEND=nats)
//	GITHUB_APP_ID           GitHub App ID
//	GITHUB_APP_PRIVATE_KEY  App private key PEM (Secret Manager secret env
//	                        or a k8s Secret)
//	LEASE_TTL               per-PR lease duration (default 20m)
//
//	GOOGLE_CLOUD_PROJECT    Secret Manager project for per-installation AI
//	                        config (tenants.SecretManager); required
//	                        regardless of STORE_BACKEND/QUEUE_BACKEND —
//	                        swapping the job store and task queue is this
//	                        change's whole scope, and tenant AI config
//	                        resolution is untouched
//
//	STORE_BACKEND           "firestore" (default) or "postgres"
//	POSTGRES_DSN            Postgres connection string (STORE_BACKEND=postgres);
//	                        apply store/migrations/001_init_schema.sql first
//
//	QUEUE_BACKEND           "cloudtasks" (default) or "nats"
//	TASKS_QUEUE_PATH        queue path for follow-up tasks (QUEUE_BACKEND=cloudtasks)
//	INVOKER_SERVICE_ACCOUNT OIDC service account for follow-up tasks (QUEUE_BACKEND=cloudtasks)
//	NATS_URL                NATS server URL (QUEUE_BACKEND=nats; default nats.DefaultURL)
//	NATS_STREAM             JetStream stream name for review tasks (QUEUE_BACKEND=nats; default "PRREVIEW_REVIEWS")
//	NATS_CONSUMER           durable consumer name (QUEUE_BACKEND=nats; default "prreview-worker")
//	NATS_WORKER_CONCURRENCY number of competing durable-consumer pull loops,
//	                        approximating Cloud Tasks' max_concurrent_dispatches
//	                        (QUEUE_BACKEND=nats; default 5)
package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	cloudtasks "cloud.google.com/go/cloudtasks/apiv2"
	"cloud.google.com/go/firestore"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"github.com/nats-io/nats.go"

	natsadapter "github.com/mediusfy/modulex/nats"
	"github.com/mediusfy/modulex/services/prreview/ai"
	"github.com/mediusfy/modulex/services/prreview/engine"
	"github.com/mediusfy/modulex/services/prreview/githubauth"
	"github.com/mediusfy/modulex/services/prreview/queue"
	"github.com/mediusfy/modulex/services/prreview/store"
	"github.com/mediusfy/modulex/services/prreview/tenants"
	"github.com/mediusfy/modulex/services/prreview/webhook"
	"github.com/mediusfy/modulex/services/prreview/worker"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx := context.Background()

	project := mustEnv(log, "GOOGLE_CLOUD_PROJECT")
	secrets, err := secretmanager.NewClient(ctx)
	if err != nil {
		log.Error("secret manager client", "error", err)
		os.Exit(1)
	}
	appKey, err := githubauth.ParsePrivateKey([]byte(mustEnv(log, "GITHUB_APP_PRIVATE_KEY")))
	if err != nil {
		log.Error("app private key", "error", err)
		os.Exit(1)
	}
	// Defense in depth: the parsed key lives only in process memory from
	// here on; nothing this process spawns (git, review subprocesses)
	// should inherit the PEM through the environment.
	_ = os.Unsetenv("GITHUB_APP_PRIVATE_KEY")

	// The lease must outlive the longest possible task attempt (Cloud Run
	// timeout 900s < Cloud Tasks DispatchDeadline 15m), or a retry could
	// take over the lease while the first attempt still runs.
	leaseTTL := 20 * time.Minute
	if v := os.Getenv("LEASE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			leaseTTL = d
		}
	}

	jobsStore := newJobsStore(ctx, log)
	httpClient := &http.Client{Timeout: 30 * time.Second}
	githubREST := &githubauth.RESTCommenter{BaseURL: "https://api.github.com", HTTP: httpClient}
	w := &worker.Worker{
		Jobs: jobsStore,
		Tokens: &githubauth.AppAuth{
			AppID:      mustEnv(log, "GITHUB_APP_ID"),
			PrivateKey: appKey,
			BaseURL:    "https://api.github.com",
			HTTP:       httpClient,
		},
		Fetcher:    engine.GitFetcher{},
		Reviewer:   engine.Modulex{},
		Commentary: ai.Dispatch{},
		Comments:   githubREST,
		PRs:        githubREST,
		Ledger:     jobsStore,
		Tenants:    &tenants.SecretManager{Client: secrets, ProjectID: project},
		LeaseTTL:   leaseTTL,
		Log:        log,
	}

	switch backend := envOr("QUEUE_BACKEND", "cloudtasks"); backend {
	case "cloudtasks":
		runCloudTasksWorker(w, log)
	case "nats":
		runNATSWorker(ctx, w, log)
	default:
		log.Error("unknown QUEUE_BACKEND", "backend", backend)
		os.Exit(1)
	}
}

// jobsAndLedgerStore is the capability the worker needs from whichever
// backend STORE_BACKEND selects; store.Firestore and store.Postgres both
// satisfy it.
type jobsAndLedgerStore interface {
	store.JobStore
	store.Ledger
}

func newJobsStore(ctx context.Context, log *slog.Logger) jobsAndLedgerStore {
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

// runCloudTasksWorker serves the Cloud Tasks push endpoint: the worker
// never initiates anything, it only reacts to an authenticated HTTP POST
// per task.
func runCloudTasksWorker(w *worker.Worker, log *slog.Logger) {
	ctx := context.Background()
	tasks, err := cloudtasks.NewClient(ctx)
	if err != nil {
		log.Error("cloud tasks client", "error", err)
		os.Exit(1)
	}
	queueTemplate := queue.CloudTasks{
		Client:                tasks,
		QueuePath:             mustEnv(log, "TASKS_QUEUE_PATH"),
		InvokerServiceAccount: mustEnv(log, "INVOKER_SERVICE_ACCOUNT"),
		// WorkerURL is set per request from the incoming task's Host: the
		// service cannot know its own Cloud Run URL at deploy time without
		// a Terraform self-reference cycle, and the URL the task actually
		// arrived on is by definition the right follow-up target.
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/task", func(rw http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(rw, "unreadable body", http.StatusBadRequest)
			return
		}
		var req webhook.ReviewRequest
		if err := json.Unmarshal(body, &req); err != nil {
			// A malformed task will never become well-formed: 400 tells
			// Cloud Tasks not to retry it forever.
			http.Error(rw, "malformed task", http.StatusBadRequest)
			return
		}
		// Per-request copies: the worker is shared across concurrent
		// requests and must not be mutated in place.
		followUpQueue := queueTemplate
		followUpQueue.WorkerURL = "https://" + r.Host + "/task"
		requestWorker := *w
		requestWorker.Queue = &followUpQueue
		if err := requestWorker.Handle(r.Context(), req); err != nil {
			log.Error("review failed; task will retry", "error", err)
			http.Error(rw, "review failed", http.StatusInternalServerError)
			return
		}
		rw.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/health", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})

	serve(mux, log, "cloud tasks")
}

// runNATSWorker starts `concurrency` competing durable-consumer pull loops
// against QUEUE_BACKEND=nats's JetStream subject and serves /health only;
// the review pipeline itself runs on those pull loops (see
// queue.Consume), not on an HTTP handler.
func runNATSWorker(ctx context.Context, w *worker.Worker, log *slog.Logger) {
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

	// MaxDeliver 8 / AckWait 10s approximate the Cloud Tasks queue this
	// replaces (infra/prreview: max_attempts 8, min_backoff 10s); see
	// queue.Consume's doc comment for the parts of that mapping that are
	// only approximate and why that is still safe.
	bus := natsadapter.NewJetStreamEventBus(js,
		natsadapter.WithJetStreamLogger(log),
		natsadapter.WithDurableMaxDeliver(8),
		natsadapter.WithDurableAckWait(10*time.Second),
	)
	w.Queue = &queue.NATS{Bus: bus}

	consumerName := envOr("NATS_CONSUMER", "prreview-worker")
	concurrency := 5
	if v := os.Getenv("NATS_WORKER_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			concurrency = n
		}
	}
	// Each Consume call starts its own durable pull loop on the adapter's
	// own goroutine and returns once the subscription is established
	// (see queue.Consume); calling it `concurrency` times with the same
	// consumerName forms a JetStream competing-consumers group, the
	// nearest NATS equivalent to Cloud Tasks' max_concurrent_dispatches.
	for i := 0; i < concurrency; i++ {
		if err := queue.Consume(ctx, bus, queue.ReviewsSubject, consumerName, w.Handle); err != nil {
			log.Error("nats consume", "error", err)
			os.Exit(1)
		}
	}
	log.Info("worker consuming", "backend", "nats", "consumer", consumerName, "concurrency", concurrency)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})
	serve(mux, log, "nats")
}

func serve(mux *http.ServeMux, log *slog.Logger, backend string) {
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
	log.Info("worker listening", "port", port, "backend", backend)
	if err := server.ListenAndServe(); err != nil {
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

// envOr returns the named environment variable, or def if it is unset or
// empty.
func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
