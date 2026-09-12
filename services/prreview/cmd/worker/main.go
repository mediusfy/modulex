// Command worker is the Cloud Tasks-dispatched Cloud Run review worker
// (Jira MOD-84). It runs with min-instances 0 and is deployed
// no-allow-unauthenticated: Cloud Run's IAM layer verifies the queue's
// OIDC token before a request reaches this process, so only the queue's
// service account can invoke it.
//
// Environment:
//
//	PORT                    listen port (Cloud Run sets this)
//	GOOGLE_CLOUD_PROJECT    Firestore/Secret Manager project
//	TASKS_QUEUE_PATH        queue path (for follow-up tasks)
//	INVOKER_SERVICE_ACCOUNT OIDC service account for follow-up tasks
//	GITHUB_APP_ID           GitHub App ID
//	GITHUB_APP_PRIVATE_KEY  App private key PEM (Secret Manager secret env)
//	LEASE_TTL               per-PR lease duration (default 10m)
package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	cloudtasks "cloud.google.com/go/cloudtasks/apiv2"
	"cloud.google.com/go/firestore"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"

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

	fsStore := &store.Firestore{Client: fs}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	githubREST := &githubauth.RESTCommenter{BaseURL: "https://api.github.com", HTTP: httpClient}
	queueTemplate := queue.CloudTasks{
		Client:                tasks,
		QueuePath:             mustEnv(log, "TASKS_QUEUE_PATH"),
		InvokerServiceAccount: mustEnv(log, "INVOKER_SERVICE_ACCOUNT"),
		// WorkerURL is set per request from the incoming task's Host: the
		// service cannot know its own Cloud Run URL at deploy time without
		// a Terraform self-reference cycle, and the URL the task actually
		// arrived on is by definition the right follow-up target.
	}
	w := &worker.Worker{
		Jobs: fsStore,
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
		Ledger:     fsStore,
		Tenants:    &tenants.SecretManager{Client: secrets, ProjectID: project},
		LeaseTTL:   leaseTTL,
		Log:        log,
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
	log.Info("worker listening", "port", port)
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
