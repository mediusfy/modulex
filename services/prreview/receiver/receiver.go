// Package receiver is the Cloud Run webhook receiver (ADR-0035 "Fast ack,
// async work", Jira MOD-83): verify the delivery's HMAC, ignore what the
// service doesn't review, dedupe on the GitHub delivery ID, apply the
// per-installation quota, enqueue to the worker queue, and return well
// inside GitHub's ~10-second budget. The review itself never runs here.
package receiver

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/mediusfy/modulex/services/prreview/queue"
	"github.com/mediusfy/modulex/services/prreview/ratelimit"
	"github.com/mediusfy/modulex/services/prreview/store"
	"github.com/mediusfy/modulex/services/prreview/webhook"
)

// maxBodyBytes bounds a webhook body; GitHub caps payloads at 25MB but
// pull_request payloads are far smaller, and unbounded reads are an abuse
// vector on a public endpoint.
const maxBodyBytes = 1 << 20

// Handler is the webhook HTTP handler.
type Handler struct {
	Secret   []byte
	Dedup    store.DedupStore
	DedupTTL time.Duration
	Queue    queue.Enqueuer
	Limiter  *ratelimit.Limiter
	Log      *slog.Logger
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	// The HMAC gate comes first: nothing from an unverified body is parsed
	// or logged.
	if err := webhook.VerifySignature(h.Secret, body, r.Header.Get("X-Hub-Signature-256")); err != nil {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}

	deliveryID := r.Header.Get("X-GitHub-Delivery")
	if deliveryID == "" {
		http.Error(w, "missing delivery id", http.StatusBadRequest)
		return
	}
	req, err := webhook.ParsePullRequestEvent(r.Header.Get("X-GitHub-Event"), deliveryID, body)
	if errors.Is(err, webhook.ErrIgnore) {
		w.WriteHeader(http.StatusOK) // acked, deliberately not reviewed
		return
	}
	if err != nil {
		http.Error(w, "unparseable delivery", http.StatusBadRequest)
		return
	}

	// GitHub redelivers on non-2xx and on its own schedule; the delivery ID
	// makes that idempotent. Dedup records self-expire via TTL.
	seen, err := h.Dedup.Seen(r.Context(), deliveryID, h.DedupTTL)
	if err != nil {
		// Failing open would double-review; failing closed (non-2xx) makes
		// GitHub retry into the same error. 500 is honest: the retry will
		// land once the store recovers, and dedup absorbs any duplicate.
		h.Log.Error("dedup store", "error", err, "delivery", deliveryID)
		http.Error(w, "dedup unavailable", http.StatusInternalServerError)
		return
	}
	if seen {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Over-quota installations are acked and dropped: a 2xx stops GitHub
	// redelivering, and not enqueueing caps the tenant's blast radius.
	if !h.Limiter.Allow(req.InstallationID) {
		h.Log.Warn("installation over quota; delivery dropped",
			"installation", req.InstallationID, "delivery", deliveryID)
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := h.Queue.Enqueue(r.Context(), req); err != nil {
		h.Log.Error("enqueue", "error", err, "delivery", deliveryID)
		http.Error(w, "enqueue failed", http.StatusInternalServerError)
		return
	}
	h.Log.Info("review enqueued",
		"installation", req.InstallationID, "repo", req.Owner+"/"+req.Repo,
		"pr", req.PRNumber, "sha", req.HeadSHA, "delivery", deliveryID)
	w.WriteHeader(http.StatusAccepted)
}
