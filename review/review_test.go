package review

import (
	"context"
	"testing"

	"github.com/mediusfy/modulex/provenance"
)

// TestReview_ComposesChecksAndSecretScan runs Review with no tools present,
// so every Checks entry (all RequiredTool: "go" or "git") reports
// StatusUnavailable without ever invoking the underlying make target — this
// keeps the test fast and hermetic while still exercising Review's actual
// composition and ordering logic, mirroring verify's own
// TestRun_MissingToolIsUnavailableAndNeverInvoked.
func TestReview_ComposesChecksAndSecretScan(t *testing.T) {
	root := newTestRepo(t)

	writeFile(t, root, "app.go", "package app\n")
	runGit(t, root, "add", "app.go")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	writeFile(t, root, "app.go", "package app\n\nfunc Hello() string { return \"hi\" }\n")
	runGit(t, root, "commit", "--quiet", "-am", "add hello")

	results := Review(context.Background(), "base", "HEAD", nil, false)

	if len(results) != len(Checks)+1 {
		t.Fatalf("len(results) = %d, want %d (len(Checks) + secret scan)", len(results), len(Checks)+1)
	}

	for i, c := range Checks {
		r := results[i]
		if r.Name != c.Name {
			t.Errorf("results[%d].Name = %q, want %q (Checks order must be preserved)", i, r.Name, c.Name)
		}
		if r.Category != c.Category {
			t.Errorf("results[%d].Category = %q, want %q", i, r.Category, c.Category)
		}
		if r.Status != provenance.StatusUnavailable {
			t.Errorf("results[%d].Status = %q, want %q (no tools were provided)", i, r.Status, provenance.StatusUnavailable)
		}
	}

	secretResult := results[len(results)-1]
	if secretResult.Name != "check-secrets" {
		t.Errorf("last result Name = %q, want check-secrets", secretResult.Name)
	}
	if secretResult.Category != provenance.VerificationSecretScan {
		t.Errorf("last result Category = %q, want %q", secretResult.Category, provenance.VerificationSecretScan)
	}
	if secretResult.Status != provenance.StatusPass {
		t.Errorf("last result Status = %q, want pass (no secret-shaped lines were added)", secretResult.Status)
	}
}

// TestReview_CategoriesCoverBoundaryCompatibilityAndChangelog asserts Checks
// carries the three non-secret-scan categories ADR-0032/Jira MOD-65 names
// ("boundaries, secrets, API, and changelog"), so a future caller grouping
// results by Category always sees all of them represented.
func TestReview_CategoriesCoverBoundaryCompatibilityAndChangelog(t *testing.T) {
	seen := make(map[provenance.VerificationCategory]bool)
	for _, c := range Checks {
		seen[c.Category] = true
	}

	for _, want := range []provenance.VerificationCategory{
		provenance.VerificationBoundary,
		provenance.VerificationCompatibility,
		provenance.VerificationChangelog,
	} {
		if !seen[want] {
			t.Errorf("Checks does not include any entry with Category %q", want)
		}
	}
}
