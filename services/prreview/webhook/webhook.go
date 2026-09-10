// Package webhook verifies and parses GitHub App webhook deliveries for the
// hosted PR-review service (ADR-0035 plan step 6, Jira MOD-83). It has no
// GCP dependencies: signature verification and event parsing are pure so the
// receiver's security boundary is table-testable.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrBadSignature is returned when X-Hub-Signature-256 does not match the
// body under the webhook secret. The receiver must reject the delivery
// without processing any of its content.
var ErrBadSignature = errors.New("webhook signature mismatch")

// VerifySignature checks a GitHub X-Hub-Signature-256 header ("sha256=<hex>")
// against the raw request body using constant-time comparison.
func VerifySignature(secret, body []byte, signatureHeader string) error {
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return ErrBadSignature
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signatureHeader, prefix))
	if err != nil {
		return ErrBadSignature
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), got) {
		return ErrBadSignature
	}
	return nil
}

// ReviewRequest is the minimal, tenant-scoped identity of one PR review job:
// everything the worker needs to run a review, and nothing that could reach
// another installation's data.
type ReviewRequest struct {
	InstallationID int64  `json:"installation_id"`
	Owner          string `json:"owner"`
	Repo           string `json:"repo"`
	PRNumber       int    `json:"pr_number"`
	HeadSHA        string `json:"head_sha"`
	BaseRef        string `json:"base_ref"`
	CloneURL       string `json:"clone_url"`
	DeliveryID     string `json:"delivery_id"`
}

// ErrIgnore is returned by ParsePullRequestEvent for deliveries the service
// deliberately does not review (wrong event type or action). The receiver
// acks them with 200 and does no further work.
var ErrIgnore = errors.New("delivery ignored")

// reviewedActions are the pull_request actions that trigger a review,
// matching the modulex pr-review.yml trigger set.
var reviewedActions = map[string]bool{
	"opened":      true,
	"synchronize": true,
	"reopened":    true,
}

// ParsePullRequestEvent extracts a ReviewRequest from a pull_request event
// body. Any structurally incomplete payload is an error: the service never
// guesses tenant identity.
func ParsePullRequestEvent(eventType, deliveryID string, body []byte) (ReviewRequest, error) {
	if eventType != "pull_request" {
		return ReviewRequest{}, fmt.Errorf("%w: event %q", ErrIgnore, eventType)
	}
	var payload struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository struct {
			Name     string `json:"name"`
			CloneURL string `json:"clone_url"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
		PullRequest struct {
			Number int `json:"number"`
			Head   struct {
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ReviewRequest{}, fmt.Errorf("parsing pull_request payload: %w", err)
	}
	if !reviewedActions[payload.Action] {
		return ReviewRequest{}, fmt.Errorf("%w: action %q", ErrIgnore, payload.Action)
	}
	req := ReviewRequest{
		InstallationID: payload.Installation.ID,
		Owner:          payload.Repository.Owner.Login,
		Repo:           payload.Repository.Name,
		PRNumber:       payload.PullRequest.Number,
		HeadSHA:        payload.PullRequest.Head.SHA,
		BaseRef:        payload.PullRequest.Base.Ref,
		CloneURL:       payload.Repository.CloneURL,
		DeliveryID:     deliveryID,
	}
	switch {
	case req.InstallationID == 0:
		return ReviewRequest{}, errors.New("pull_request payload missing installation id")
	case req.Owner == "" || req.Repo == "":
		return ReviewRequest{}, errors.New("pull_request payload missing repository identity")
	case req.PRNumber == 0 || req.HeadSHA == "" || req.BaseRef == "":
		return ReviewRequest{}, errors.New("pull_request payload missing PR identity")
	}
	return req, nil
}
