// Package engine adapts the modulex review engine for the hosted worker
// (ADR-0035 "The hosted App is a third thin adapter over the same engine";
// it adds delivery and tenancy, never new check logic).
//
// SECURITY BOUNDARY: unlike CI and the editor plugins — which run in the
// repository owner's own trust domain — the hosted worker reviews
// UNTRUSTED tenant checkouts inside the shared multi-tenant service. It
// therefore runs ONLY the engine's pure checks (review.ScanSecrets,
// review.CheckProtectedPaths), never a tenant-declared command:
// agentreview.Review would execute the checkout's own make targets via
// sh -c, handing every PR author arbitrary code execution next to the
// App's credentials. Declared-gate coverage for hosted tenants belongs in
// their own CI, where their code already runs.
package engine

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mediusfy/modulex/contract"
	"github.com/mediusfy/modulex/provenance"
	"github.com/mediusfy/modulex/review"
)

// Reviewer runs the deterministic review engine over a checkout.
type Reviewer interface {
	Review(ctx context.Context, dir, baseRef, headRef string) ([]provenance.VerificationResult, error)
}

// Modulex is the production Reviewer: the pure-check subset of the review
// engine (see the package comment for why declared commands never run
// here).
type Modulex struct{}

func (Modulex) Review(ctx context.Context, dir, baseRef, headRef string) ([]provenance.VerificationResult, error) {
	// The contract is read fail-CLOSED, matching the mcpserver and
	// agentcli adapters: a present-but-broken modulex.agent.yaml must
	// error, not silently disable protected-path enforcement.
	var protectedPaths []string
	raw, err := os.ReadFile(filepath.Join(dir, "modulex.agent.yaml"))
	switch {
	case err == nil:
		var c contract.Contract
		if err := yaml.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("modulex.agent.yaml is present but unparseable: %w", err)
		}
		protectedPaths = c.ProtectedPaths
	case errors.Is(err, fs.ErrNotExist):
		// No contract is a normal state: review without protected paths.
	default:
		return nil, fmt.Errorf("reading modulex.agent.yaml: %w", err)
	}

	return []provenance.VerificationResult{
		review.ScanSecrets(ctx, dir, baseRef, headRef),
		review.CheckProtectedPaths(ctx, dir, baseRef, headRef, protectedPaths),
	}, nil
}

// Fetcher produces a local checkout of the PR to review and the diff text
// the AI layer summarizes.
type Fetcher interface {
	// Fetch clones owner/repo at headSHA (with baseRef available for the
	// diff as BaseRefName) into a fresh directory, authenticating with
	// token. The caller runs cleanup when done.
	Fetch(ctx context.Context, cloneURL, token, baseRef, headSHA string) (dir string, cleanup func(), err error)
	// Diff returns the unified diff between baseRef and headRef in dir.
	Diff(ctx context.Context, dir, baseRef, headRef string) (string, error)
}

// GitFetcher is the production Fetcher, shelling out to git. The token is
// passed via a one-shot Basic auth header, never written to disk or into
// the remote URL (which would land in .git/config).
type GitFetcher struct{}

func (GitFetcher) Fetch(ctx context.Context, cloneURL, token, baseRef, headSHA string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "prreview-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	authHeader := "Authorization: Basic " +
		basicAuth("x-access-token", token)
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git",
			append([]string{"-C", dir, "-c", "http." + cloneURL + ".extraheader=" + authHeader}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, redactToken(string(out), token))
		}
		return nil
	}
	// The diff base may be a branch name (first review) or the
	// last-reviewed commit SHA (incremental review, MOD-84); a SHA is
	// fetched directly, a branch via an explicit refspec — a combined
	// fetch would leave FETCH_HEAD ambiguous. Fetches are blobless
	// partial fetches (--filter=blob:none), NOT shallow: the review diffs
	// use the three-dot base...head form, which needs the merge base — a
	// shallow fetch drops it whenever the branch point is older than the
	// depth, silently disabling the secret and protected-path checks on
	// exactly the large, stale PRs that most need them. Blobless keeps
	// the full commit graph small; git fetches file contents on demand
	// while diffing.
	baseFetch := []string{"fetch", "--quiet", "--filter=blob:none", "origin",
		"+refs/heads/" + baseRef + ":refs/heads/" + BaseRefName}
	baseBranch := []string(nil)
	if isCommitSHA(baseRef) {
		baseFetch = []string{"fetch", "--quiet", "--filter=blob:none", "origin", baseRef}
		baseBranch = []string{"branch", "--quiet", BaseRefName, baseRef}
	}
	steps := [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", cloneURL},
		{"fetch", "--quiet", "--filter=blob:none", "origin", headSHA},
		baseFetch,
	}
	if baseBranch != nil {
		steps = append(steps, baseBranch)
	}
	steps = append(steps, []string{"checkout", "--quiet", headSHA})
	for _, step := range steps {
		if err := run(step...); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return dir, cleanup, nil
}

// BaseRefName is the local ref GitFetcher creates for the PR's base, which
// the worker passes to the engine as the diff base.
const BaseRefName = "prreview-base"

func basicAuth(user, token string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
}

func isCommitSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, c := range ref {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Diff returns the unified diff the AI layer summarizes. Part of Fetcher so
// worker tests need no git.
func (GitFetcher) Diff(ctx context.Context, dir, baseRef, headRef string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "diff", baseRef+"..."+headRef)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return string(out), nil
}

func redactToken(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "***")
}
