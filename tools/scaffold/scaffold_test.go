package scaffold_test

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mediusfy/modulex/tools/scaffold"
)

func TestDeriveNamesViaGenerate(t *testing.T) {
	// deriveNames itself is unexported; exercise its behavior indirectly
	// through Generate's output, which is the public contract that matters.
	dir := t.TempDir()
	writeGoMod(t, dir, "example.com/app")

	result, err := scaffold.Generate(scaffold.Config{Name: "Billing Thing", OutDir: dir})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	wantDir := filepath.Join(dir, "billing-thing")
	if result.TargetDir != wantDir {
		t.Errorf("TargetDir = %q, want %q", result.TargetDir, wantDir)
	}

	moduleGo := readFile(t, filepath.Join(wantDir, "module.go"))
	if !strings.Contains(moduleGo, "package billingthing") {
		t.Errorf("module.go does not declare `package billingthing`:\n%s", moduleGo)
	}
	if !strings.Contains(moduleGo, `return "billing-thing"`) {
		t.Errorf("module.go's Name() does not return \"billing-thing\":\n%s", moduleGo)
	}

	domainGo := readFile(t, filepath.Join(wantDir, "domain", "billingthing.go"))
	if !strings.Contains(domainGo, "type BillingThing struct") {
		t.Errorf("domain file does not declare `type BillingThing struct`:\n%s", domainGo)
	}
}

func TestGenerateProducesAllExpectedFiles(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir, "example.com/app")

	result, err := scaffold.Generate(scaffold.Config{Name: "widget", OutDir: dir})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	wantRelPaths := []string{
		"domain/widget.go",
		"ports/repo.go",
		"ports/service.go",
		"service/service.go",
		"service/service_test.go",
		"adapters/inmemory_repo.go",
		"module.go",
		"module_test.go",
		"README.md",
	}
	for _, rel := range wantRelPaths {
		want := filepath.Join(result.TargetDir, rel)
		if !contains(result.Files, want) {
			t.Errorf("Generate did not report writing %s; got Files=%v", want, result.Files)
		}
		if _, err := os.Stat(want); err != nil {
			t.Errorf("expected file %s to exist: %v", want, err)
		}
	}
}

func TestGeneratedGoFilesParseAndAreGofmtClean(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir, "example.com/app")

	result, err := scaffold.Generate(scaffold.Config{Name: "widget", OutDir: dir})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	fset := token.NewFileSet()
	for _, f := range result.Files {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		src := readFile(t, f)
		if _, err := parser.ParseFile(fset, f, src, parser.AllErrors); err != nil {
			t.Errorf("generated file %s does not parse as valid Go: %v", f, err)
		}
	}

	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt not on PATH")
	}
	out, err := exec.Command("gofmt", "-l", result.TargetDir).CombinedOutput()
	if err != nil {
		t.Fatalf("gofmt -l: %v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("gofmt -l reports unformatted files:\n%s", out)
	}
}

func TestGenerateAutoDetectsModuleImport(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir, "example.com/app")
	nested := filepath.Join(dir, "examples")

	result, err := scaffold.Generate(scaffold.Config{Name: "widget", OutDir: nested})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	portsGo := readFile(t, filepath.Join(result.TargetDir, "ports", "repo.go"))
	wantImport := `"example.com/app/examples/widget/domain"`
	if !strings.Contains(portsGo, wantImport) {
		t.Errorf("ports/repo.go does not import %s (auto-detected module path is wrong):\n%s", wantImport, portsGo)
	}
}

func TestGenerateModuleOverrideBypassesAutoDetection(t *testing.T) {
	dir := t.TempDir()
	// Deliberately no go.mod anywhere under dir, so auto-detection would fail.

	result, err := scaffold.Generate(scaffold.Config{
		Name:         "widget",
		OutDir:       dir,
		ModuleImport: "override.example/root",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	moduleGo := readFile(t, filepath.Join(result.TargetDir, "module.go"))
	if !strings.Contains(moduleGo, `"override.example/root/adapters"`) {
		t.Errorf("module.go does not use the -module override for its imports:\n%s", moduleGo)
	}
}

func TestGenerateFailsWithoutModuleOverrideOrGoMod(t *testing.T) {
	dir := t.TempDir()

	_, err := scaffold.Generate(scaffold.Config{Name: "widget", OutDir: dir})
	if err == nil {
		t.Fatalf("Generate: expected an error when no go.mod is found and no -module override is given")
	}
}

func TestGenerateRefusesNonEmptyTargetWithoutForce(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir, "example.com/app")

	if _, err := scaffold.Generate(scaffold.Config{Name: "widget", OutDir: dir}); err != nil {
		t.Fatalf("first Generate: %v", err)
	}

	_, err := scaffold.Generate(scaffold.Config{Name: "widget", OutDir: dir})
	if err == nil {
		t.Fatalf("second Generate: expected an error when the target directory already exists and Force is false")
	}

	if _, err := scaffold.Generate(scaffold.Config{Name: "widget", OutDir: dir, Force: true}); err != nil {
		t.Fatalf("Generate with Force: %v", err)
	}
}

func TestGenerateRejectsEmptyName(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir, "example.com/app")

	if _, err := scaffold.Generate(scaffold.Config{Name: "   ", OutDir: dir}); err == nil {
		t.Fatalf("Generate: expected an error for a blank name")
	}
}

func TestGenerateRejectsEmptyOutDir(t *testing.T) {
	if _, err := scaffold.Generate(scaffold.Config{Name: "widget", OutDir: ""}); err == nil {
		t.Fatalf("Generate: expected an error for an empty OutDir")
	}
}

func writeGoMod(t *testing.T, dir, modulePath string) {
	t.Helper()
	content := "module " + modulePath + "\n\ngo 1.25.0\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
