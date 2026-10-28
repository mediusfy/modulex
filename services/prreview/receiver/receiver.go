// Package receiver is the Cloud Run webhook receiver (ADR-0035 "Fast ack,
// async work", Jira MOD-83): verify the delivery's HMAC, ignore what the
// service doesn't review, dedupe on the GitHub delivery ID, apply the
// per-installation quota, enqueue to the worker queue, and return well
// inside GitHub's ~10-second budget. The review itself never runs here.
package receiver

import (
	"crypto/sha256"
	"encoding/hex"
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

	// GitHub redelivers on non-2xx and on its own schedule; that is made
	// idempotent below. Dedup keys on a hash of the raw, HMAC-verified
	// body rather than the X-GitHub-Delivery header: that header is NOT
	// covered by the signature, so an attacker who captures one signed
	// webhook body could otherwise replay it under a freshly chosen
	// delivery ID and bypass delivery-ID-keyed dedup entirely (the
	// signature check above only proves the body is authentic, not that
	// this particular delivery ID is). Keying on the body's own hash means
	// replaying identical signed bytes always collides, regardless of
	// which delivery ID header the replay carries. Dedup records
	// self-expire via TTL. The check runs BEFORE enqueue but the same call
	// also records the key — and that record must not outlive a failed
	// enqueue, or the 500-triggered redelivery would be swallowed as a
	// duplicate and the review dropped forever; on enqueue failure the
	// record is best-effort forgotten so the redelivery goes through.
	dedupKey := bodyDedupKey(body)
	seen, err := h.Dedup.Seen(r.Context(), dedupKey, h.DedupTTL)
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
		if ferr := h.Dedup.Forget(r.Context(), dedupKey); ferr != nil {
			// Worst case the review waits out the dedup TTL; log loudly.
			h.Log.Error("dedup forget failed; redelivery will be swallowed until TTL",
				"error", ferr, "delivery", deliveryID)
		}
		http.Error(w, "enqueue failed", http.StatusInternalServerError)
		return
	}
	h.Log.Info("review enqueued",
		"installation", req.InstallationID, "repo", req.Owner+"/"+req.Repo,
		"pr", req.PRNumber, "sha", req.HeadSHA, "delivery", deliveryID)
	w.WriteHeader(http.StatusAccepted)
}

// bodyDedupKey derives the dedup store's key from the raw webhook body —
// already verified against the HMAC signature by the time this is called —
// rather than from the unsigned X-GitHub-Delivery header, so replay
// protection cannot be bypassed simply by resending the same signed body
// under a different, attacker-chosen delivery ID.
func bodyDedupKey(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
