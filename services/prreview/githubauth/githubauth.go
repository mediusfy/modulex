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
type TokenMinter interface {
	InstallationToken(ctx context.Context, installationID int64) (string, error)
}

// Commenter posts or updates the App's single review comment on a PR
// (ADR-0035 "update its existing PR comment instead of posting a duplicate").
type Commenter interface {
	// CreateComment posts a new issue comment and returns its ID.
	CreateComment(ctx context.Context, token, owner, repo string, prNumber int, body string) (int64, error)
	// UpdateComment replaces an existing comment's body.
	UpdateComment(ctx context.Context, token, owner, repo string, commentID int64, body string) error
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

func (a *AppAuth) InstallationToken(ctx context.Context, installationID int64) (string, error) {
	jwt, err := a.appJWT(time.Now())
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", a.BaseURL, installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
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
