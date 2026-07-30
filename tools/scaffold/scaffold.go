// Package scaffold generates a modulex feature module using the recommended
// domain/ports/service/adapters/module.go layout (see
// examples/hexagonal/incident in the mediusfy/modulex repository for the
// hand-written exemplar this mirrors). It is a small, template-based
// generator — stdlib text/template with go:embed, no code-generation
// framework or third-party templating dependency — deliberately kept
// minimal per the scaffolding tool's design goals.
//
// Generated modules use constructor injection as the default wiring
// pattern and additionally register their Service under a typed
// modulex.Key so typed service location remains available as an opt-in
// alternative. See docs/planning/scaffolding-and-test-harness-guide.md for
// the full rationale, and this package's own doc comment on ServiceKey's
// template (templates/ports_service.go.tmpl) for exactly what's generated.
package scaffold

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

var tmpl = template.Must(template.ParseFS(templatesFS, "templates/*.tmpl"))

// Data is the set of values every template can reference. It is fully
// resolved by Generate before any template executes, so template files
// contain no logic beyond field substitution.
type Data struct {
	// ModuleName is the kebab-case feature name returned by
	// modulex.Module.Name(), e.g. "billing" or "scaffolded-sample".
	ModuleName string
	// PkgName is the Go package identifier used for module.go's own
	// package (e.g. "billing"). It is ModuleName with any '-'/'_'
	// separators removed, since Go package names cannot contain them.
	PkgName string
	// TypeName is the PascalCase domain type name, e.g. "Billing".
	TypeName string
	// IDPrefix is an upper-case, underscore-separated prefix used when
	// generating example IDs in service.go (e.g. "BILLING").
	IDPrefix string
	// RootImport is the import path of the generated feature's own root
	// package (module.go's package), e.g.
	// "github.com/mediusfy/modulex/examples/scaffolded-sample".
	RootImport string
	// DomainImport, PortsImport, ServiceImport, AdaptersImport are
	// RootImport plus each subpackage's directory name.
	DomainImport   string
	PortsImport    string
	ServiceImport  string
	AdaptersImport string
}

// Config configures a single Generate call.
type Config struct {
	// Name is the feature name, e.g. "billing". It is normalized into
	// Data's ModuleName/PkgName/TypeName/IDPrefix fields; see deriveNames.
	Name string
	// OutDir is the parent directory under which a new directory named
	// after the normalized ModuleName is created (mirroring
	// examples/hexagonal, whose "incident" subdirectory is both the
	// feature's directory name and its module.go package name).
	OutDir string
	// ModuleImport, if non-empty, overrides auto-detection of the Go
	// import path corresponding to the target directory. Auto-detection
	// walks up from OutDir looking for the nearest go.mod and computes the
	// import path from its module directive; ModuleImport bypasses that
	// when auto-detection would guess wrong (e.g. generating into a
	// directory that does not yet sit under any go.mod on disk).
	ModuleImport string
	// Force allows Generate to write into a TargetDir that already exists
	// and is non-empty. Without Force, Generate refuses to overwrite it.
	Force bool
}

// Result reports what Generate wrote.
type Result struct {
	// TargetDir is the directory Generate wrote into: filepath.Join(cfg.OutDir, data.ModuleName).
	TargetDir string
	// Files lists every file Generate wrote, in the order written.
	Files []string
}

// fileSpec pairs a template name (as embedded under templates/) with the
// path (relative to TargetDir) it renders to.
type fileSpec struct {
	tmplName string
	relPath  string
}

var fileSpecs = []fileSpec{
	{"domain.go.tmpl", "domain/{{.PkgName}}.go"},
	{"ports_repo.go.tmpl", "ports/repo.go"},
	{"ports_service.go.tmpl", "ports/service.go"},
	{"service.go.tmpl", "service/service.go"},
	{"service_test.go.tmpl", "service/service_test.go"},
	{"adapters_inmemory_repo.go.tmpl", "adapters/inmemory_repo.go"},
	{"module.go.tmpl", "module.go"},
	{"module_test.go.tmpl", "module_test.go"},
	{"README.md.tmpl", "README.md"},
}

// Generate renders the domain/ports/service/adapters/module.go layout for
// cfg into a new directory under cfg.OutDir, named after the normalized
// feature name, and returns the directory and the files written to it.
func Generate(cfg Config) (*Result, error) {
	names, err := deriveNames(cfg.Name)
	if err != nil {
		return nil, fmt.Errorf("scaffold: %w", err)
	}

	if strings.TrimSpace(cfg.OutDir) == "" {
		return nil, errors.New("scaffold: OutDir must not be empty")
	}
	targetDir := filepath.Join(cfg.OutDir, names.kebab)

	if err := checkTargetDir(targetDir, cfg.Force); err != nil {
		return nil, err
	}

	rootImport := cfg.ModuleImport
	if rootImport == "" {
		rootImport, err = detectModuleImport(targetDir)
		if err != nil {
			return nil, fmt.Errorf("scaffold: detect module import path for %s: %w (pass -module to override auto-detection)", targetDir, err)
		}
	}

	data := Data{
		ModuleName:     names.kebab,
		PkgName:        names.pkg,
		TypeName:       names.typeName,
		IDPrefix:       strings.ToUpper(strings.ReplaceAll(names.kebab, "-", "_")),
		RootImport:     rootImport,
		DomainImport:   rootImport + "/domain",
		PortsImport:    rootImport + "/ports",
		ServiceImport:  rootImport + "/service",
		AdaptersImport: rootImport + "/adapters",
	}

	result := &Result{TargetDir: targetDir}
	for _, spec := range fileSpecs {
		relPath, err := renderPath(spec.relPath, data)
		if err != nil {
			return nil, fmt.Errorf("scaffold: resolve path for %s: %w", spec.tmplName, err)
		}
		outPath := filepath.Join(targetDir, relPath)

		content, err := renderTemplate(spec.tmplName, data)
		if err != nil {
			return nil, fmt.Errorf("scaffold: render %s: %w", spec.tmplName, err)
		}

		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return nil, fmt.Errorf("scaffold: create directory for %s: %w", outPath, err)
		}
		if err := os.WriteFile(outPath, content, 0o644); err != nil {
			return nil, fmt.Errorf("scaffold: write %s: %w", outPath, err)
		}
		result.Files = append(result.Files, outPath)
	}

	return result, nil
}

// checkTargetDir refuses to write into an existing, non-empty targetDir
// unless force is true. A non-existent targetDir, or an existing empty one,
// is always fine.
func checkTargetDir(targetDir string, force bool) error {
	info, err := os.Stat(targetDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("scaffold: stat %s: %w", targetDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("scaffold: %s already exists and is not a directory", targetDir)
	}
	if force {
		return nil
	}
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return fmt.Errorf("scaffold: read %s: %w", targetDir, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("scaffold: %s already exists and is not empty; pass -force to overwrite", targetDir)
	}
	return nil
}

// renderPath executes relPathTmpl (a path that may itself reference
// template fields, e.g. "domain/{{.PkgName}}.go") as a one-off template
// against data.
func renderPath(relPathTmpl string, data Data) (string, error) {
	if !strings.Contains(relPathTmpl, "{{") {
		return relPathTmpl, nil
	}
	t, err := template.New("path").Parse(relPathTmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// renderTemplate executes the named embedded template against data. Output
// for ".go.tmpl" templates is passed through go/format.Source so the
// generated files are gofmt-clean regardless of the template's own
// whitespace.
func renderTemplate(name string, data Data) ([]byte, error) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return nil, err
	}
	if !strings.HasSuffix(name, ".go.tmpl") {
		return buf.Bytes(), nil
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("gofmt generated source: %w\n--- rendered source ---\n%s", err, buf.String())
	}
	return formatted, nil
}

// derivedNames holds the normalized forms of a raw feature name.
type derivedNames struct {
	kebab    string // e.g. "billing-thing"
	pkg      string // e.g. "billingthing"
	typeName string // e.g. "BillingThing"
}

// deriveNames normalizes a raw feature name (e.g. "Billing Thing",
// "billing_thing", "billing-thing") into the kebab-case module name, the
// Go package identifier (which cannot contain '-'/'_'), and the PascalCase
// domain type name. Words are split on any run of non-alphanumeric
// characters; a name given in camelCase is treated as a single word (this
// is a small, template-based generator, not a full identifier-casing
// library).
func deriveNames(raw string) (derivedNames, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return derivedNames{}, errors.New("name must not be empty")
	}

	words := splitWords(raw)
	if len(words) == 0 {
		return derivedNames{}, fmt.Errorf("name %q contains no letters or digits", raw)
	}

	lowerWords := make([]string, len(words))
	for i, w := range words {
		lowerWords[i] = strings.ToLower(w)
	}

	kebab := strings.Join(lowerWords, "-")
	pkg := strings.Join(lowerWords, "")
	if unicode.IsDigit(rune(pkg[0])) {
		pkg = "m" + pkg
	}

	var typeBuilder strings.Builder
	for _, w := range lowerWords {
		typeBuilder.WriteString(strings.ToUpper(w[:1]))
		typeBuilder.WriteString(w[1:])
	}
	typeName := typeBuilder.String()
	if unicode.IsDigit(rune(typeName[0])) {
		typeName = "M" + typeName
	}

	return derivedNames{kebab: kebab, pkg: pkg, typeName: typeName}, nil
}

// splitWords splits s on any run of characters that are neither letters
// nor digits.
func splitWords(s string) []string {
	var words []string
	var current strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current.WriteRune(r)
			continue
		}
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}

// detectModuleImport walks up from the nearest existing ancestor of
// targetDir looking for a go.mod, then computes the Go import path
// corresponding to targetDir from that go.mod's module directive plus the
// relative path between the two. targetDir itself need not exist yet.
func detectModuleImport(targetDir string) (string, error) {
	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return "", err
	}

	searchDir := absTarget
	for {
		if _, err := os.Stat(searchDir); err == nil {
			break
		}
		parent := filepath.Dir(searchDir)
		if parent == searchDir {
			return "", fmt.Errorf("no existing ancestor directory found for %s", absTarget)
		}
		searchDir = parent
	}

	goModDir := searchDir
	for {
		modPath := filepath.Join(goModDir, "go.mod")
		data, err := os.ReadFile(modPath)
		if err == nil {
			modName, err := parseModuleDirective(data)
			if err != nil {
				return "", fmt.Errorf("parse %s: %w", modPath, err)
			}
			rel, err := filepath.Rel(goModDir, absTarget)
			if err != nil {
				return "", err
			}
			rel = filepath.ToSlash(rel)
			if rel == "." {
				return modName, nil
			}
			return modName + "/" + rel, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("read %s: %w", modPath, err)
		}
		parent := filepath.Dir(goModDir)
		if parent == goModDir {
			return "", fmt.Errorf("no go.mod found above %s", absTarget)
		}
		goModDir = parent
	}
}

// parseModuleDirective extracts the module path from a go.mod file's
// "module ..." directive.
func parseModuleDirective(data []byte) (string, error) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module")), nil
		}
	}
	return "", errors.New("no module directive found")
}
