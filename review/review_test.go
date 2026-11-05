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
	tests := []struct {
		name     string
		baseSrc  string
		changeTo string
	}{
		{
			name:     "adds a benign function with no tools and no protected paths",
			baseSrc:  "package app\n",
			changeTo: "package app\n\nfunc Hello() string { return \"hi\" }\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newTestRepo(t)

			writeFile(t, root, "app.go", tt.baseSrc)
			runGit(t, root, "add", "app.go")
			runGit(t, root, "commit", "--quiet", "-m", "base")
			runGit(t, root, "branch", "base")

			writeFile(t, root, "app.go", tt.changeTo)
			runGit(t, root, "commit", "--quiet", "-am", "add hello")

			assertReviewComposition(t, root)
		})
	}
}

func assertReviewComposition(t *testing.T, root string) {
	t.Helper()

	results := Review(context.Background(), root, "base", "HEAD", nil, false, nil)

	if len(results) != len(Checks)+2 {
		t.Fatalf("len(results) = %d, want %d (len(Checks) + secret scan + protected paths)", len(results), len(Checks)+2)
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

	secretResult := results[len(Checks)]
	if secretResult.Name != "check-secrets" {
		t.Errorf("secret scan result Name = %q, want check-secrets", secretResult.Name)
	}
	if secretResult.Category != provenance.VerificationSecretScan {
		t.Errorf("secret scan result Category = %q, want %q", secretResult.Category, provenance.VerificationSecretScan)
	}
	if secretResult.Status != provenance.StatusPass {
		t.Errorf("secret scan result Status = %q, want pass (no secret-shaped lines were added)", secretResult.Status)
	}

	protectedResult := results[len(results)-1]
	if protectedResult.Name != "check-protected-paths" {
		t.Errorf("last result Name = %q, want check-protected-paths", protectedResult.Name)
	}
	if protectedResult.Category != provenance.VerificationProtectedPaths {
		t.Errorf("last result Category = %q, want %q", protectedResult.Category, provenance.VerificationProtectedPaths)
	}
	if protectedResult.Status != provenance.StatusPass {
		t.Errorf("last result Status = %q, want pass (nil protectedPaths was passed)", protectedResult.Status)
	}
}

// TestReview_ProtectedPathHit asserts Review's last result fails when the
// diff touches a protected path. Uses SECURITY.md rather than go.mod or
// CHANGELOG.md, which carry their own exceptions tested in
// protectedpaths_test.go.
func TestReview_ProtectedPathHit(t *testing.T) {
	tests := []struct {
		name          string
		file          string
		baseContent   string
		changeContent string
		protected     []string
		wantStatus    provenance.Status
	}{
		{
			name:          "changed file declared protected fails",
			file:          "SECURITY.md",
			baseContent:   "# Security\n",
			changeContent: "# Security\n\nReport issues privately.\n",
			protected:     []string{"SECURITY.md"},
			wantStatus:    provenance.StatusFail,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newTestRepo(t)

			writeFile(t, root, tt.file, tt.baseContent)
			runGit(t, root, "add", tt.file)
			runGit(t, root, "commit", "--quiet", "-m", "base")
			runGit(t, root, "branch", "base")

			writeFile(t, root, tt.file, tt.changeContent)
			runGit(t, root, "commit", "--quiet", "-am", "touch "+tt.file)

			results := Review(context.Background(), root, "base", "HEAD", nil, false, tt.protected)

			protectedResult := results[len(results)-1]
			if protectedResult.Status != tt.wantStatus {
				t.Errorf("protected-paths result Status = %q, want %q (%s was declared protected and changed)", protectedResult.Status, tt.wantStatus, tt.file)
			}
			if protectedResult.Category != provenance.VerificationProtectedPaths {
				t.Errorf("protected-paths result Category = %q, want %q", protectedResult.Category, provenance.VerificationProtectedPaths)
			}
		})
	}
}

// TestReview_CategoriesCoverBoundaryCompatibilityAndChangelog asserts Checks
// carries the three non-secret-scan categories named in the spec
// ("boundaries, secrets, API, and changelog"), so a future caller grouping
// results by Category always sees all of them represented.
func TestReview_CategoriesCoverBoundaryCompatibilityAndChangelog(t *testing.T) {
	tests := []struct {
		name string
		want provenance.VerificationCategory
	}{
		{name: "boundary", want: provenance.VerificationBoundary},
		{name: "compatibility", want: provenance.VerificationCompatibility},
		{name: "changelog", want: provenance.VerificationChangelog},
	}

	seen := make(map[provenance.VerificationCategory]bool)
	for _, c := range Checks {
		seen[c.Category] = true
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !seen[tt.want] {
				t.Errorf("Checks does not include any entry with Category %q", tt.want)
			}
		})
	}
}
