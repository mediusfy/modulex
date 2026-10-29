// Package githubauth handles the hosted App's GitHub side (ADR-0035
// "Hundreds of concurrent users, never mixed", Jira MOD-86): minting a
// short-lived token scoped to exactly one installation per job, and
// creating/updating the single review comment on a PR. The interfaces are
// narrow so worker tests use fakes; nothing else in the service touches
// GitHub credentials.
package githubauth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// TokenMinter mints a short-lived installation-scoped access token. No job
// ever holds a token for any other installation.
//
// The token is additionally scoped down to exactly the one repository being
// reviewed, and to the minimum GitHub permissions this service's job
// actually needs (reading PR/repo contents and commenting on issues/PRs):
// an unscoped installation token would carry the installation's full,
// potentially multi-repo, default-permission grant, which is far more
// blast radius than any single review job requires if that token were ever
// leaked or misused.
type TokenMinter interface {
	InstallationToken(ctx context.Context, installationID int64, owner, repo string) (string, error)
}

// Commenter posts or updates the App's single review comment on a PR
// (ADR-0035 "update its existing PR comment instead of posting a duplicate").
type Commenter interface {
	// CreateComment posts a new issue comment and returns its ID.
	CreateComment(ctx context.Context, token, owner, repo string, prNumber int, body string) (int64, error)
	// UpdateComment replaces an existing comment's body.
	UpdateComment(ctx context.Context, token, owner, repo string, commentID int64, body string) error
}

// PRReader resolves a PR's current head SHA — the authoritative ordering
// source the lease state machine lacks (Cloud Tasks deliveries are
// unordered, so the worker re-anchors its target on GitHub's answer).
type PRReader interface {
	PRHead(ctx context.Context, token, owner, repo string, prNumber int) (string, error)
}

// PRHead implements PRReader over the GitHub REST API.
func (c *RESTCommenter) PRHead(ctx context.Context, token, owner, repo string, prNumber int) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.BaseURL, owner, repo, prNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("reading PR head: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("reading PR head: status %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Head.SHA == "" {
		return "", errors.New("PR response missing head sha")
	}
	return out.Head.SHA, nil
}

// AppAuth is the production TokenMinter: a GitHub App's ID and private key
// sign a short-lived JWT, exchanged for an installation token via the REST
// API. The private key comes from Secret Manager at startup, never from
// Firestore or the environment of any per-tenant code path.
type AppAuth struct {
	AppID      string
	PrivateKey *rsa.PrivateKey
	BaseURL    string // https://api.github.com; overridable for tests
	HTTP       *http.Client
}

// ParsePrivateKey parses a PEM-encoded RSA private key (PKCS#1 or PKCS#8).
func ParsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block in App private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing App private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("app private key is not RSA")
	}
	return key, nil
}

// appJWT builds the App's authentication JWT (RS256, ≤10 minutes).
func (a *AppAuth) appJWT(now time.Time) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		// 60s clock-drift allowance, per GitHub's App auth docs.
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": a.AppID,
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.PrivateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("signing App JWT: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// minimalReviewPermissions is the smallest GitHub App permission set this
// service's job actually exercises against the token it mints: reading the
// repository's code (the checkout), reading the PR (head-SHA re-anchoring),
// and writing issue comments (PR comments are posted via the issues API).
// Any installation-level permission beyond this (e.g. admin, workflows,
// other repos' contents) is deliberately never requested.
var minimalReviewPermissions = map[string]string{
	"contents":      "read",
	"pull_requests": "read",
	"issues":        "write",
}

func (a *AppAuth) InstallationToken(ctx context.Context, installationID int64, owner, repo string) (string, error) {
	jwt, err := a.appJWT(time.Now())
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", a.BaseURL, installationID)
	// Scope the minted token down to exactly the one repository under
	// review and to minimalReviewPermissions, rather than requesting the
	// installation's full default-scoped grant (every repo and permission
	// the installation has access to): a per-job token this narrow limits
	// the blast radius of any single leaked or misused token to one repo
	// and the few permissions this job needs. owner is the repository's
	// account, implied by installationID itself in GitHub's API (the
	// "repositories" field below takes bare repo names within that
	// account), so it is not sent on the wire, but callers always know
	// which repo they're asking for by owner/repo.
	payload, err := json.Marshal(struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}{
		Repositories: []string{repo},
		Permissions:  minimalReviewPermissions,
	})
	if err != nil {
		return "", fmt.Errorf("encoding scoped token request for %s/%s: %w", owner, repo, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("minting installation token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("minting installation token: %s: %s", resp.Status, body)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Token == "" {
		return "", errors.New("installation token response missing token")
	}
	return out.Token, nil
}

// RESTCommenter is the production Commenter over the GitHub REST API.
type RESTCommenter struct {
	BaseURL string
	HTTP    *http.Client
}

func (c *RESTCommenter) CreateComment(ctx context.Context, token, owner, repo string, prNumber int, body string) (int64, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", c.BaseURL, owner, repo, prNumber)
	respBody, status, err := c.do(ctx, http.MethodPost, url, token, body)
	if err != nil {
		return 0, err
	}
	if status != http.StatusCreated {
		return 0, fmt.Errorf("creating comment: status %d: %s", status, respBody)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil || out.ID == 0 {
		return 0, errors.New("create-comment response missing id")
	}
	return out.ID, nil
}

func (c *RESTCommenter) UpdateComment(ctx context.Context, token, owner, repo string, commentID int64, body string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/comments/%d", c.BaseURL, owner, repo, commentID)
	respBody, status, err := c.do(ctx, http.MethodPatch, url, token, body)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("updating comment %d: status %d: %s", commentID, status, respBody)
	}
	return nil
}

func (c *RESTCommenter) do(ctx context.Context, method, url, token, commentBody string) ([]byte, int, error) {
	payload, err := json.Marshal(map[string]string{"body": commentBody})
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return respBody, resp.StatusCode, nil
}
