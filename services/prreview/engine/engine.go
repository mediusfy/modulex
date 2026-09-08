// Package engine adapts the modulex review engine for the hosted worker
// (ADR-0035 "The hosted App is a third thin adapter over the same engine";
// it adds delivery and tenancy, never new check logic). Reviewer mirrors
// tools/mcpserver's review_diff composition — discovery, the repository
// contract's protected paths, then agentreview.Review — so the hosted App
// produces identical results to the reusable workflow for the same diff.
package engine

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mediusfy/modulex/agentreview"
	"github.com/mediusfy/modulex/contract"
	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/provenance"
)

// Reviewer runs the deterministic review engine over a checkout.
type Reviewer interface {
	Review(ctx context.Context, dir, baseRef, headRef string) ([]provenance.VerificationResult, error)
}

// Modulex is the production Reviewer: the same composition as the MCP
// server's review_diff tool. Networked checks are always skipped — the
// worker reviews with allowNetwork=false so a tenant's declared commands
// cannot exfiltrate through the service.
type Modulex struct{}

func (Modulex) Review(ctx context.Context, dir, baseRef, headRef string) ([]provenance.VerificationResult, error) {
	repo, err := discovery.Discover(dir)
	if err != nil {
		return nil, fmt.Errorf("discovering checkout: %w", err)
	}
	var protectedPaths []string
	raw, err := os.ReadFile(filepath.Join(dir, "modulex.agent.yaml"))
	if err == nil {
		var c contract.Contract
		if yaml.Unmarshal(raw, &c) == nil {
			protectedPaths = c.ProtectedPaths
		}
	}
	return agentreview.Review(ctx, repo, baseRef, headRef, false, protectedPaths), nil
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
	// fetch would leave FETCH_HEAD ambiguous.
	baseFetch := []string{"fetch", "--quiet", "--depth", "50", "origin",
		"+refs/heads/" + baseRef + ":refs/heads/" + BaseRefName}
	baseBranch := []string(nil)
	if isCommitSHA(baseRef) {
		baseFetch = []string{"fetch", "--quiet", "--depth", "50", "origin", baseRef}
		baseBranch = []string{"branch", "--quiet", BaseRefName, baseRef}
	}
	steps := [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", cloneURL},
		{"fetch", "--quiet", "--depth", "50", "origin", headSHA},
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
