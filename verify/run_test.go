package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/provenance"
)

func TestRun_MissingToolIsUnavailableAndNeverInvoked(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")

	check := CheckSpec{
		Name: "fake-check",
		// If this were ever actually executed, it would create marker.
		Command:      "touch " + marker,
		Category:     provenance.VerificationFull,
		RequiredTool: "definitely-not-a-real-tool-on-this-machine",
	}

	results := Run(context.Background(), []CheckSpec{check}, nil, false)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]

	if r.Status != provenance.StatusUnavailable {
		t.Errorf("Status = %q, want %q", r.Status, provenance.StatusUnavailable)
	}
	if r.Reason == "" {
		t.Error("Reason is empty, want a non-empty reason naming the missing tool")
	}

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker file exists at %s; the command was executed despite the required tool being absent", marker)
	}
}

func TestRun_NetworkedCheckSkippedWithoutAllowNetwork(t *testing.T) {
	check := CheckSpec{
		Name:      "deps",
		Command:   "go mod download",
		Category:  provenance.VerificationFull,
		Networked: true,
	}

	results := Run(context.Background(), []CheckSpec{check}, nil, false)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]

	if r.Status != provenance.StatusSkipped {
		t.Errorf("Status = %q, want %q", r.Status, provenance.StatusSkipped)
	}
	if r.Reason == "" {
		t.Error("Reason is empty, want a non-empty reason explaining the skip")
	}
}

func TestRun_NetworkedCheckRunsWithAllowNetwork(t *testing.T) {
	check := CheckSpec{
		Name:      "networked-but-actually-cheap",
		Command:   "go version",
		Category:  provenance.VerificationFull,
		Networked: true,
	}

	results := Run(context.Background(), []CheckSpec{check}, []discovery.ToolStatus{{Name: "go", Present: true}}, true)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]

	if r.Status != provenance.StatusPass {
		t.Errorf("Status = %q, want %q (allowNetwork=true should let a Networked check actually run)", r.Status, provenance.StatusPass)
	}
}

func TestRun_RealCommandPasses(t *testing.T) {
	check := CheckSpec{
		Name:         "go-version",
		Command:      "go version",
		Category:     provenance.VerificationFocused,
		RequiredTool: "go",
	}

	results := Run(context.Background(), []CheckSpec{check}, []discovery.ToolStatus{{Name: "go", Present: true}}, false)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]

	if r.Status != provenance.StatusPass {
		t.Errorf("Status = %q, want %q", r.Status, provenance.StatusPass)
	}
	if r.Message == "" {
		t.Error("Message is empty, want captured command output")
	}
}

func TestRun_FailingCommandFails(t *testing.T) {
	check := CheckSpec{
		Name:     "always-fails",
		Command:  "exit 1",
		Category: provenance.VerificationFocused,
	}

	results := Run(context.Background(), []CheckSpec{check}, nil, false)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]

	if r.Status != provenance.StatusFail {
		t.Errorf("Status = %q, want %q", r.Status, provenance.StatusFail)
	}
}

func TestRun_EveryInputProducesExactlyOneResult(t *testing.T) {
	checks := []CheckSpec{
		{Name: "pass", Command: "true", Category: provenance.VerificationFocused},
		{Name: "fail", Command: "exit 1", Category: provenance.VerificationFocused},
		{Name: "unavailable", Command: "touch /should/not/run", Category: provenance.VerificationFull, RequiredTool: "no-such-tool"},
		{Name: "skipped", Command: "go mod download", Category: provenance.VerificationFull, Networked: true},
		{Name: "pass-2", Command: "echo ok", Category: provenance.VerificationFocused},
	}

	results := Run(context.Background(), checks, []discovery.ToolStatus{{Name: "go", Present: true}}, false)

	if len(results) != len(checks) {
		t.Fatalf("len(results) = %d, want %d (one result per input check, never fewer)", len(results), len(checks))
	}

	wantStatus := map[string]provenance.Status{
		"pass":        provenance.StatusPass,
		"fail":        provenance.StatusFail,
		"unavailable": provenance.StatusUnavailable,
		"skipped":     provenance.StatusSkipped,
		"pass-2":      provenance.StatusPass,
	}
	for _, r := range results {
		want, ok := wantStatus[r.Name]
		if !ok {
			t.Fatalf("unexpected result name %q", r.Name)
		}
		if r.Status != want {
			t.Errorf("result %q: Status = %q, want %q", r.Name, r.Status, want)
		}
		switch r.Status {
		case provenance.StatusSkipped, provenance.StatusUnavailable:
			if r.Reason == "" {
				t.Errorf("result %q: Reason is empty for status %q", r.Name, r.Status)
			}
		}
	}

	if _, err := os.Stat("/should/not/run"); !os.IsNotExist(err) {
		t.Fatal("the unavailable check's command was executed despite the missing tool")
	}
}

func TestRun_CheckSpecDirSetsWorkingDirectory(t *testing.T) {
	dir := t.TempDir()

	check := CheckSpec{
		Name:     "pwd-check",
		Command:  "pwd",
		Category: provenance.VerificationFocused,
		Dir:      dir,
	}

	results := Run(context.Background(), []CheckSpec{check}, nil, false)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]

	if r.Status != provenance.StatusPass {
		t.Fatalf("Status = %q, want %q; Message: %s", r.Status, provenance.StatusPass, r.Message)
	}

	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", dir, err)
	}
	gotDir := strings.TrimSpace(r.Message)
	resolvedGot, err := filepath.EvalSymlinks(gotDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", gotDir, err)
	}
	if resolvedGot != resolvedDir {
		t.Errorf("pwd reported %q, want %q (CheckSpec.Dir was not honored)", resolvedGot, resolvedDir)
	}
}

func TestRun_EmptyCheckSpecDirUsesCallingProcessCwd(t *testing.T) {
	wantCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}

	check := CheckSpec{
		Name:     "pwd-check-no-dir",
		Command:  "pwd",
		Category: provenance.VerificationFocused,
	}

	results := Run(context.Background(), []CheckSpec{check}, nil, false)
	if results[0].Status != provenance.StatusPass {
		t.Fatalf("Status = %q, want %q; Message: %s", results[0].Status, provenance.StatusPass, results[0].Message)
	}

	resolvedWant, err := filepath.EvalSymlinks(wantCwd)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", wantCwd, err)
	}
	gotDir := strings.TrimSpace(results[0].Message)
	resolvedGot, err := filepath.EvalSymlinks(gotDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", gotDir, err)
	}
	if resolvedGot != resolvedWant {
		t.Errorf("pwd reported %q, want %q (empty Dir must leave the calling process's cwd unchanged, matching pre-Dir-field behavior)", resolvedGot, resolvedWant)
	}
}

func TestRun_MakeTargetWithoutMakefileIsUnavailable(t *testing.T) {
	check := CheckSpec{
		Name:     "check-changelog",
		Command:  "make check-changelog",
		Category: provenance.VerificationChangelog,
		Dir:      t.TempDir(), // no Makefile here
	}

	results := Run(context.Background(), []CheckSpec{check}, nil, false)
	if results[0].Status != provenance.StatusUnavailable {
		t.Fatalf("Status = %q, want %q; Message: %s",
			results[0].Status, provenance.StatusUnavailable, results[0].Message)
	}
	if !strings.Contains(results[0].Reason, "no Makefile") {
		t.Errorf("Reason = %q, want it to say the repository has no Makefile", results[0].Reason)
	}
}

func TestRun_UndefinedMakeTargetIsUnavailable(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available on PATH")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("hello:\n\t@echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check := CheckSpec{
		Name:     "check-changelog",
		Command:  "make check-changelog",
		Category: provenance.VerificationChangelog,
		Dir:      dir,
	}

	results := Run(context.Background(), []CheckSpec{check}, nil, false)
	if results[0].Status != provenance.StatusUnavailable {
		t.Fatalf("Status = %q, want %q; Message: %s",
			results[0].Status, provenance.StatusUnavailable, results[0].Message)
	}
	if !strings.Contains(results[0].Reason, "not defined") {
		t.Errorf("Reason = %q, want it to say the target is not defined", results[0].Reason)
	}
}

func TestRun_DefinedMakeTargetStillRunsAndCanFail(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available on PATH")
	}
	dir := t.TempDir()
	makefile := "pass:\n\t@true\nfail:\n\t@echo boom; false\n"
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(makefile), 0o644); err != nil {
		t.Fatal(err)
	}

	checks := []CheckSpec{
		{Name: "pass", Command: "make pass", Category: provenance.VerificationFull, Dir: dir},
		{Name: "fail", Command: "make fail", Category: provenance.VerificationFull, Dir: dir},
	}
	results := Run(context.Background(), checks, nil, false)
	if results[0].Status != provenance.StatusPass {
		t.Errorf("pass target Status = %q, want %q; Message: %s",
			results[0].Status, provenance.StatusPass, results[0].Message)
	}
	if results[1].Status != provenance.StatusFail {
		t.Errorf("fail target Status = %q, want %q (a defined-but-failing target must stay a real failure)",
			results[1].Status, provenance.StatusFail)
	}
}

func TestRun_NonMakeCommandsSkipTheMakePreflight(t *testing.T) {
	// A non-"make <target>" command in a Makefile-less directory must run
	// normally -- the preflight only reasons about the bare make shape.
	check := CheckSpec{
		Name:     "echo",
		Command:  "echo ok",
		Category: provenance.VerificationFull,
		Dir:      t.TempDir(),
	}
	results := Run(context.Background(), []CheckSpec{check}, nil, false)
	if results[0].Status != provenance.StatusPass {
		t.Fatalf("Status = %q, want %q", results[0].Status, provenance.StatusPass)
	}
}
