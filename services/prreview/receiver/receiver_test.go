package receiver

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mediusfy/modulex/services/prreview/queue"
	"github.com/mediusfy/modulex/services/prreview/ratelimit"
	"github.com/mediusfy/modulex/services/prreview/store"
)

var secret = []byte("hooksecret")

const prPayload = `{
  "action": "opened",
  "installation": {"id": 42},
  "repository": {"name": "modulex", "clone_url": "https://github.com/mediusfy/modulex.git", "owner": {"login": "mediusfy"}},
  "pull_request": {"number": 7, "head": {"sha": "abc123"}, "base": {"ref": "main"}}
}`

func sign(body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

type request struct {
	method    string
	event     string
	delivery  string
	body      string
	signature string // "" means sign correctly
}

func do(h *Handler, r request) *httptest.ResponseRecorder {
	if r.method == "" {
		r.method = http.MethodPost
	}
	body := []byte(r.body)
	sig := r.signature
	if sig == "" {
		sig = sign(body)
	}
	req := httptest.NewRequest(r.method, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", r.event)
	req.Header.Set("X-GitHub-Delivery", r.delivery)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func newHandler(limit int) (*Handler, *queue.Memory) {
	q := &queue.Memory{}
	return &Handler{
		Secret:   secret,
		Dedup:    store.NewMemory(),
		DedupTTL: 24 * time.Hour,
		Queue:    q,
		Limiter:  ratelimit.New(limit, time.Hour),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, q
}

func TestHandler(t *testing.T) {
	tests := []struct {
		name        string
		requests    []request
		limit       int
		wantCodes   []int
		wantQueued  int
		description string
	}{
		{
			name:       "valid delivery is enqueued and accepted",
			requests:   []request{{event: "pull_request", delivery: "d1", body: prPayload}},
			limit:      100,
			wantCodes:  []int{http.StatusAccepted},
			wantQueued: 1,
		},
		{
			name: "duplicate delivery id is acked but enqueued once",
			requests: []request{
				{event: "pull_request", delivery: "d1", body: prPayload},
				{event: "pull_request", delivery: "d1", body: prPayload},
			},
			limit:      100,
			wantCodes:  []int{http.StatusAccepted, http.StatusOK},
			wantQueued: 1,
		},
		{
			name:       "bad signature is rejected before any processing",
			requests:   []request{{event: "pull_request", delivery: "d1", body: prPayload, signature: "sha256=deadbeef"}},
			limit:      100,
			wantCodes:  []int{http.StatusUnauthorized},
			wantQueued: 0,
		},
		{
			name:       "ignored event types are acked without enqueueing",
			requests:   []request{{event: "issues", delivery: "d1", body: prPayload}},
			limit:      100,
			wantCodes:  []int{http.StatusOK},
			wantQueued: 0,
		},
		{
			name:       "ignored PR actions are acked without enqueueing",
			requests:   []request{{event: "pull_request", delivery: "d1", body: `{"action":"closed"}`}},
			limit:      100,
			wantCodes:  []int{http.StatusOK},
			wantQueued: 0,
		},
		{
			name:       "missing delivery id is a bad request",
			requests:   []request{{event: "pull_request", delivery: "", body: prPayload}},
			limit:      100,
			wantCodes:  []int{http.StatusBadRequest},
			wantQueued: 0,
		},
		{
			name:       "malformed but signed payload is a bad request",
			requests:   []request{{event: "pull_request", delivery: "d1", body: `{"action":"opened"}`}},
			limit:      100,
			wantCodes:  []int{http.StatusBadRequest},
			wantQueued: 0,
		},
		{
			name:       "GET is rejected",
			requests:   []request{{method: http.MethodGet, event: "pull_request", delivery: "d1", body: prPayload}},
			limit:      100,
			wantCodes:  []int{http.StatusMethodNotAllowed},
			wantQueued: 0,
		},
		{
			name: "over-quota installation is acked and dropped",
			requests: []request{
				{event: "pull_request", delivery: "d1", body: prPayload},
				{event: "pull_request", delivery: "d2", body: prPayload},
			},
			limit:      1,
			wantCodes:  []int{http.StatusAccepted, http.StatusOK},
			wantQueued: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, q := newHandler(tt.limit)
			for i, r := range tt.requests {
				w := do(h, r)
				if w.Code != tt.wantCodes[i] {
					t.Fatalf("request %d: code %d, want %d (body %q)", i, w.Code, tt.wantCodes[i], w.Body.String())
				}
			}
			if got := len(q.Tasks()); got != tt.wantQueued {
				t.Fatalf("queued %d tasks, want %d", got, tt.wantQueued)
			}
		})
	}
}

func TestHandlerEnqueuesScopedIdentity(t *testing.T) {
	tests := []struct{ name string }{{name: "tenant identity round-trips"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, q := newHandler(100)
			do(h, request{event: "pull_request", delivery: "d9", body: prPayload})
			tasks := q.Tasks()
			if len(tasks) != 1 {
				t.Fatalf("queued %d, want 1", len(tasks))
			}
			got := tasks[0]
			if got.InstallationID != 42 || got.Owner != "mediusfy" || got.Repo != "modulex" ||
				got.PRNumber != 7 || got.HeadSHA != "abc123" || got.BaseRef != "main" || got.DeliveryID != "d9" {
				t.Fatalf("unexpected task %+v", got)
			}
		})
	}
}
