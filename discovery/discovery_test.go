package discovery

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeFile creates dir/name (creating parent directories as needed) with
// the given content, failing the test on any error.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// buildFixture lays out a repository fixture covering every discovery
// facet except real git state (see TestDiscover_GitDirtyDetection for
// that): a root go.mod, a nested module two levels deep with its own
// nested go.mod one level deeper still (which must NOT be discovered, since
// walking stops descending once a go.mod is found), an AGENTS.md, a
// Makefile with .PHONY and plain target lines, a CI workflow file, an
// examples/ composition root, a directory with func main() outside
// examples/, and a mock (non-functional) .git directory.
func buildFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	writeFile(t, root, "go.mod", "module example.com/root\n\ngo 1.25\n")
	writeFile(t, root, "AGENTS.md", "# Agents\n")
	writeFile(t, root, "Makefile", ".PHONY: build test\n\nbuild:\n\tgo build ./...\n\ntest:\n\tgo test ./...\n\nVERSION := v1.0.0\n")
	writeFile(t, root, filepath.Join(".github", "workflows", "ci.yml"), "name: CI\n")

	// Nested module two levels deep.
	writeFile(t, root, filepath.Join("pkg", "sub", "go.mod"), "module example.com/root/pkg/sub\n")
	writeFile(t, root, filepath.Join("pkg", "sub", "sub.go"), "package sub\n")
	// A go.mod one level deeper than pkg/sub must NOT be discovered: the
	// walk stops descending once it finds pkg/sub's go.mod.
	writeFile(t, root, filepath.Join("pkg", "sub", "deeper", "go.mod"), "module example.com/root/pkg/sub/deeper\n")

	// examples/ composition roots.
	writeFile(t, root, filepath.Join("examples", "quickstart", "main.go"), "package main\n\nfunc main() {}\n")
	writeFile(t, root, filepath.Join("examples", "nomain", "helper.go"), "package nomain\n")

	// A func-main directory outside examples/.
	writeFile(t, root, filepath.Join("cmd", "tool", "main.go"), "package main\n\nfunc main() {}\n")

	// Mock (non-functional) .git directory: enough to make IsGitRepo true
	// without a real git repository.
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.git): %v", err)
	}

	return root
}

func TestDiscover_Fixture(t *testing.T) {
	root := buildFixture(t)

	repo, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if !repo.IsGitRepo {
		t.Errorf("IsGitRepo = false, want true (mock .git directory present)")
	}
	if repo.Dirty {
		t.Errorf("Dirty = true, want false (mock .git dir cannot report real status)")
	}

	wantInstruction := InstructionFile{Path: "AGENTS.md", Name: "AGENTS.md"}
	if !containsInstruction(repo.InstructionFiles, wantInstruction) {
		t.Errorf("InstructionFiles = %+v, want to contain %+v", repo.InstructionFiles, wantInstruction)
	}

	for _, want := range []string{"build", "test"} {
		if !containsString(repo.MakeTargets, want) {
			t.Errorf("MakeTargets = %v, want to contain %q", repo.MakeTargets, want)
		}
	}
	if containsString(repo.MakeTargets, "VERSION") {
		t.Errorf("MakeTargets = %v, should not contain variable assignment %q", repo.MakeTargets, "VERSION")
	}

	if !containsString(repo.CIWorkflows, "ci.yml") {
		t.Errorf("CIWorkflows = %v, want to contain %q", repo.CIWorkflows, "ci.yml")
	}

	if !containsCompositionRoot(repo.CompositionRoots, "examples/quickstart") {
		t.Errorf("CompositionRoots = %+v, want to contain examples/quickstart", repo.CompositionRoots)
	}
	// Every direct child of examples/ is a composition root unconditionally
	// (per the discovery spec), regardless of whether it has a func
	// main() — examples/nomain deliberately has none, to prove this.
	if !containsCompositionRoot(repo.CompositionRoots, "examples/nomain") {
		t.Errorf("CompositionRoots = %+v, want to contain examples/nomain (direct child of examples/, even without func main())", repo.CompositionRoots)
	}
	if !containsCompositionRoot(repo.CompositionRoots, "cmd/tool") {
		t.Errorf("CompositionRoots = %+v, want to contain cmd/tool (func main() outside examples/)", repo.CompositionRoots)
	}
}

func TestDiscover_NestedModules(t *testing.T) {
	root := buildFixture(t)

	repo, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	want := map[string]string{
		".":       "example.com/root",
		"pkg/sub": "example.com/root/pkg/sub",
	}
	got := make(map[string]string, len(repo.Modules))
	for _, m := range repo.Modules {
		got[m.Path] = m.ModulePath
	}

	for path, modulePath := range want {
		gotModulePath, ok := got[path]
		if !ok {
			t.Errorf("Modules missing entry for %q", path)
			continue
		}
		if gotModulePath != modulePath {
			t.Errorf("Modules[%q].ModulePath = %q, want %q", path, gotModulePath, modulePath)
		}
	}

	if _, ok := got["pkg/sub/deeper"]; ok {
		t.Errorf("Modules contains pkg/sub/deeper, but the walk must stop descending once pkg/sub's go.mod is found")
	}
	if len(repo.Modules) != len(want) {
		t.Errorf("Modules = %+v, want exactly %d entries (%v)", repo.Modules, len(want), want)
	}
}

func TestDiscover_RejectsNonDirectory(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "not-a-dir")
	writeFile(t, root, "not-a-dir", "hello\n")

	if _, err := Discover(filePath); err == nil {
		t.Errorf("Discover(%q) = nil error, want error since path is not a directory", filePath)
	}
}

func TestDiscover_MissingRoot(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "does-not-exist")

	if _, err := Discover(missing); err == nil {
		t.Errorf("Discover(%q) = nil error, want error since root does not exist", missing)
	}
}

// TestDiscover_GitDirtyDetection uses a real `git init`'d repository, since
// dirty-vs-clean detection depends on real `git status --porcelain`
// output, which a mock .git directory cannot produce.
func TestDiscover_GitDirtyDetection(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}

	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")

	writeFile(t, root, "tracked.txt", "hello\n")
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "-m", "initial commit")

	repo, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !repo.IsGitRepo {
		t.Errorf("IsGitRepo = false, want true")
	}
	if repo.Dirty {
		t.Errorf("Dirty = true, want false right after a clean commit")
	}

	writeFile(t, root, "tracked.txt", "modified\n")

	repo, err = Discover(root)
	if err != nil {
		t.Fatalf("Discover (after edit): %v", err)
	}
	if !repo.Dirty {
		t.Errorf("Dirty = false, want true after modifying a tracked file")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestDiscoverTools_MissingToolReportedExplicitly(t *testing.T) {
	fakeName := "definitely-not-a-real-binary-xyz123"
	tools := discoverTools([]string{"go", fakeName})

	var found bool
	for _, tool := range tools {
		if tool.Name == fakeName {
			found = true
			if tool.Present {
				t.Errorf("ToolStatus for %q reports Present = true, want false", fakeName)
			}
			if tool.Path != "" {
				t.Errorf("ToolStatus for %q has Path = %q, want empty", fakeName, tool.Path)
			}
		}
	}
	if !found {
		t.Errorf("discoverTools result %+v does not contain an entry for %q; missing tools must be reported explicitly, not dropped", tools, fakeName)
	}
}

func TestDiscoverIndexes_MissingIndexReportedExplicitly(t *testing.T) {
	root := t.TempDir()
	// Only create .git; .codegraph and .tokensave are absent.
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.git): %v", err)
	}

	indexes := discoverIndexes(root, defaultIndexNames)

	want := map[string]bool{".codegraph": false, ".git": true, ".tokensave": false}
	got := make(map[string]bool, len(indexes))
	for _, idx := range indexes {
		got[idx.Name] = idx.Present
	}
	for name, present := range want {
		gotPresent, ok := got[name]
		if !ok {
			t.Errorf("Indexes missing entry for %q", name)
			continue
		}
		if gotPresent != present {
			t.Errorf("Indexes[%q].Present = %v, want %v", name, gotPresent, present)
		}
	}
}

func TestRepository_JSONMarshalDeterministic(t *testing.T) {
	repo := Repository{
		Root:      "/repo",
		IsGitRepo: true,
		Dirty:     false,
		Modules: []GoModule{
			{Path: ".", ModulePath: "example.com/root"},
			{Path: "tools/sub", ModulePath: "example.com/root/tools/sub"},
		},
		CompositionRoots: []CompositionRoot{
			{Path: "examples/quickstart", Reason: "direct child directory of examples/"},
			{Path: "cmd/tool", Reason: "contains a func main() declaration"},
		},
		InstructionFiles: []InstructionFile{
			{Path: "AGENTS.md", Name: "AGENTS.md"},
			{Path: ".github/copilot-instructions.md", Name: "copilot-instructions.md"},
		},
		MakeTargets: []string{"build", "deps", "test"},
		CIWorkflows: []string{"ci.yml"},
		Indexes: []IndexStatus{
			{Name: ".codegraph", Present: false},
			{Name: ".git", Present: true},
			{Name: ".tokensave", Present: false},
		},
		Tools: []ToolStatus{
			{Name: "docker", Present: false},
			{Name: "git", Present: true, Path: "/usr/bin/git"},
			{Name: "go", Present: true, Path: "/usr/local/go/bin/go"},
			{Name: "gofmt", Present: true, Path: "/usr/local/go/bin/gofmt"},
			{Name: "golangci-lint", Present: false},
		},
	}

	b1, err := json.Marshal(repo)
	if err != nil {
		t.Fatalf("json.Marshal (1st): %v", err)
	}
	b2, err := json.Marshal(repo)
	if err != nil {
		t.Fatalf("json.Marshal (2nd): %v", err)
	}
	if string(b1) != string(b2) {
		t.Errorf("json.Marshal is not deterministic:\n1st: %s\n2nd: %s", b1, b2)
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func containsInstruction(files []InstructionFile, want InstructionFile) bool {
	for _, f := range files {
		if f == want {
			return true
		}
	}
	return false
}

func containsCompositionRoot(roots []CompositionRoot, path string) bool {
	for _, r := range roots {
		if r.Path == path {
			return true
		}
	}
	return false
}
