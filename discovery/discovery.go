// Package discovery scans a repository directory and reports what an AI
// coding agent needs to select a project and begin useful work, per
// ADR-0032 ("Agent-First Development Experience"), P0: "Add Modulex agent
// project discovery and command classification" (Jira MOD-64), step 1 of
// the ADR's "Standard agent workflow": `modulex agent discover` identifies
// the repository root, projects, modules, composition roots, instruction
// files, Make targets, CI workflows, and available indexes.
//
// This package is a standalone leaf package, like provenance
// (github.com/mediusfy/modulex/provenance): it does not import the core
// modulex package, and depends on provenance only for the CommandClass enum
// so command classification stays consistent with the provenance/handoff
// schema rather than inventing a parallel one. Discover is otherwise pure
// standard library and never consults global, user-scoped configuration
// (no ~/.claude, no ~/.kimi-code, nothing outside the given root and PATH),
// per the ADR's "discovery works without global hooks" acceptance
// criterion.
//
// Discover is read-only: it enumerates files, parses text, and checks PATH,
// but it never executes a discovered binary or a Make target. The one
// external process it runs is `git status --porcelain`, used only to detect
// a dirty worktree.
//
// See docs/planning/agent-discovery-guide.md for the full guide, including
// the nested-module-boundary behavior and the command-classification rule
// table's fail-safe default.
package discovery

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// GoModule describes one discovered Go module: its path relative to the
// discovery root ("." for the root module itself) and the module path
// declared by its go.mod's "module" directive.
type GoModule struct {
	Path       string `json:"path"`
	ModulePath string `json:"module_path"`
}

// CompositionRoot describes one directory identified as a candidate
// composition root (a place where modules are wired together into a
// runnable program), along with why it was flagged.
type CompositionRoot struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// InstructionFile describes one discovered agent-instruction file (e.g.
// AGENTS.md, CLAUDE.md) and where it was found.
type InstructionFile struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// IndexStatus reports whether a well-known semantic-index directory (e.g.
// .codegraph, .tokensave) is present at the discovery root. Absence is
// reported explicitly rather than omitted, per ADR-0032's "missing tools
// and optional services are reported explicitly" acceptance criterion.
type IndexStatus struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
}

// ToolStatus reports whether a commonly-needed binary is available on
// PATH. A missing tool is reported explicitly (Present: false) rather than
// silently dropped from the result.
type ToolStatus struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	// Path is the resolved absolute path from exec.LookPath, empty when
	// Present is false.
	Path string `json:"path,omitempty"`
}

// Repository is the result of Discover: everything found by walking a
// repository root and checking PATH, with no dependency on global or
// user-scoped state. Every slice field is deterministically sorted and
// never nil, so two Discover calls against the same on-disk repository
// state produce byte-identical JSON via json.Marshal (the same discipline
// as provenance.Envelope and the core module's Manager.Diagnostics /
// Manager.ModuleContract).
type Repository struct {
	// Root is the absolute path Discover resolved the given root to.
	Root string `json:"root"`
	// IsGitRepo is true if Root contains a .git entry (directory or file,
	// the latter covering git worktrees). It does not imply Dirty is
	// meaningful in the same run if the entry turns out not to be a usable
	// git repository (see Discover's doc comment).
	IsGitRepo bool `json:"is_git_repo"`
	// Dirty is true if `git status --porcelain` reported any uncommitted
	// changes. Always false when IsGitRepo is false; this is the
	// "not a git repository" case, not an error.
	Dirty bool `json:"dirty"`

	// Modules lists every Go module found under Root, including Root's own
	// module if it has a go.mod. Walking stops descending into a directory
	// once a go.mod is found there: a nested module's own subdirectories
	// belong to it, not to the parent scan, mirroring how `go list ./...`
	// treats module boundaries.
	Modules []GoModule `json:"modules"`
	// CompositionRoots lists directories identified as candidate
	// composition roots: every direct child directory of examples/ (if
	// present), plus any directory anywhere under Root containing a Go
	// file with a package-level func main().
	CompositionRoots []CompositionRoot `json:"composition_roots"`
	// InstructionFiles lists every well-known agent-instruction file found
	// anywhere under Root (AGENTS.md, CLAUDE.md, .cursorrules,
	// copilot-instructions.md).
	InstructionFiles []InstructionFile `json:"instruction_files"`
	// MakeTargets lists target names parsed from a Makefile at Root, if
	// present, both from .PHONY: lines and plain "target:" lines at column
	// zero. Empty (never nil) if there is no Makefile at Root.
	MakeTargets []string `json:"make_targets"`
	// CIWorkflows lists *.yml/*.yaml file names under .github/workflows/ at
	// Root, if present.
	CIWorkflows []string `json:"ci_workflows"`
	// Indexes reports presence/absence of well-known semantic-index
	// directories at Root (.codegraph, .git, .tokensave).
	Indexes []IndexStatus `json:"indexes"`
	// Tools reports presence/absence on PATH of commonly-needed binaries
	// (go, git, golangci-lint, gofmt, docker). Discover never executes any
	// of them.
	Tools []ToolStatus `json:"tools"`
}

// defaultIndexNames is the fixed, well-known list of semantic-index
// directories Discover checks for at the repository root.
var defaultIndexNames = []string{".codegraph", ".git", ".tokensave"}

// defaultToolNames is the fixed list of binaries Discover checks for on
// PATH. It is deliberately small and specific to this repository's toolchain
// rather than an attempt at an exhaustive list.
var defaultToolNames = []string{"go", "git", "golangci-lint", "gofmt", "docker"}

// wellKnownInstructionNames is the fixed list of agent-instruction file
// basenames Discover looks for anywhere under the discovery root. This is
// deliberately not exhaustive (per the ticket: "this doesn't need to be
// exhaustive, just needs to report what it finds and where").
var wellKnownInstructionNames = map[string]bool{
	"AGENTS.md":               true,
	"CLAUDE.md":               true,
	".cursorrules":            true,
	"copilot-instructions.md": true,
}

// Discover scans root (which must be an existing directory; never the
// process's implicit working directory — the caller always supplies it
// explicitly, even if that means passing "." for their own current
// directory) and reports Go modules, composition roots, instruction files,
// Make targets, CI workflows, semantic indexes, available tools, and
// git dirty-worktree state.
//
// Discover never mutates the repository and never executes a discovered
// binary or Make target; it only reads files, walks directories, checks
// PATH, and (for dirty-worktree detection only) runs `git status
// --porcelain`.
func Discover(root string) (Repository, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Repository{}, fmt.Errorf("discovery: resolving root %q: %w", root, err)
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return Repository{}, fmt.Errorf("discovery: root %q: %w", root, err)
	}
	if !info.IsDir() {
		return Repository{}, fmt.Errorf("discovery: root %q is not a directory", root)
	}

	modules, err := discoverModules(absRoot)
	if err != nil {
		return Repository{}, fmt.Errorf("discovery: scanning for go modules: %w", err)
	}

	compositionRoots, err := discoverCompositionRoots(absRoot)
	if err != nil {
		return Repository{}, fmt.Errorf("discovery: scanning for composition roots: %w", err)
	}

	instructionFiles, err := discoverInstructionFiles(absRoot)
	if err != nil {
		return Repository{}, fmt.Errorf("discovery: scanning for instruction files: %w", err)
	}

	makeTargets, err := discoverMakeTargets(absRoot)
	if err != nil {
		return Repository{}, fmt.Errorf("discovery: parsing Makefile: %w", err)
	}

	ciWorkflows, err := discoverCIWorkflows(absRoot)
	if err != nil {
		return Repository{}, fmt.Errorf("discovery: scanning for CI workflows: %w", err)
	}

	isGitRepo, dirty := discoverGitStatus(absRoot)

	return Repository{
		Root:             absRoot,
		IsGitRepo:        isGitRepo,
		Dirty:            dirty,
		Modules:          modules,
		CompositionRoots: compositionRoots,
		InstructionFiles: instructionFiles,
		MakeTargets:      makeTargets,
		CIWorkflows:      ciWorkflows,
		Indexes:          discoverIndexes(absRoot, defaultIndexNames),
		Tools:            discoverTools(defaultToolNames),
	}, nil
}

// shouldSkipDir reports whether a directory named name should be excluded
// from the tree walks below. Dot-directories are skipped as a rule (build
// caches, editor state, VCS internals, semantic-index stores) except
// .github, which must be walked to find copilot-instructions.md and CI
// workflow files.
func shouldSkipDir(name string) bool {
	switch name {
	case "vendor", "node_modules":
		return true
	case ".github":
		return false
	}
	return strings.HasPrefix(name, ".")
}

// moduleDirectiveRe matches a go.mod "module" directive line.
var moduleDirectiveRe = regexp.MustCompile(`^\s*module\s+(\S+)`)

// readModulePath reads goModPath and returns its declared module path. ok
// is true iff the file exists; modulePath may be empty if the file exists
// but no "module" directive line was found (a malformed go.mod), which is
// reported rather than treated as an error.
func readModulePath(goModPath string) (modulePath string, ok bool, err error) {
	data, err := os.ReadFile(goModPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("reading %s: %w", goModPath, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if m := moduleDirectiveRe.FindStringSubmatch(line); m != nil {
			return strings.Trim(m[1], "\"`"), true, nil
		}
	}
	return "", true, nil
}

// discoverModules walks root for go.mod files, recording each as a
// GoModule. root's own go.mod (if any) is always recorded as Path ".". For
// every other directory, once a go.mod is found, discoverModules stops
// descending into that directory's subtree: a nested module owns its own
// subdirectories, so they are never walked looking for a further nested
// go.mod as if it belonged to the parent scan.
func discoverModules(root string) ([]GoModule, error) {
	mods := make([]GoModule, 0)

	rootModPath, ok, err := readModulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	if ok {
		mods = append(mods, GoModule{Path: ".", ModulePath: rootModPath})
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", root, err)
	}
	for _, e := range entries {
		if !e.IsDir() || shouldSkipDir(e.Name()) {
			continue
		}
		if err := walkForModules(root, filepath.Join(root, e.Name()), &mods); err != nil {
			return nil, err
		}
	}

	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods, nil
}

// walkForModules recursively scans dir for a go.mod. If found, it records
// the module and returns without descending further (see discoverModules'
// doc comment for why); otherwise it recurses into dir's subdirectories.
func walkForModules(root, dir string, mods *[]GoModule) error {
	modulePath, ok, err := readModulePath(filepath.Join(dir, "go.mod"))
	if err != nil {
		return err
	}
	if ok {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return fmt.Errorf("computing relative path for %s: %w", dir, err)
		}
		*mods = append(*mods, GoModule{Path: filepath.ToSlash(rel), ModulePath: modulePath})
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() || shouldSkipDir(e.Name()) {
			continue
		}
		if err := walkForModules(root, filepath.Join(dir, e.Name()), mods); err != nil {
			return err
		}
	}
	return nil
}

// discoverCompositionRoots identifies candidate composition roots: every
// direct child directory of examples/ (if root has one), plus any
// directory anywhere under root (module boundaries are not a barrier here,
// unlike discoverModules) containing a Go file with a package-level func
// main().
func discoverCompositionRoots(root string) ([]CompositionRoot, error) {
	out := make([]CompositionRoot, 0)
	seen := make(map[string]bool)

	examplesDir := filepath.Join(root, "examples")
	if info, err := os.Stat(examplesDir); err == nil && info.IsDir() {
		entries, err := os.ReadDir(examplesDir)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", examplesDir, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			rel := filepath.ToSlash(filepath.Join("examples", e.Name()))
			out = append(out, CompositionRoot{Path: rel, Reason: "direct child directory of examples/"})
			seen[rel] = true
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat %s: %w", examplesDir, err)
	}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && shouldSkipDir(d.Name()) {
			return filepath.SkipDir
		}

		hasMain, err := dirHasMainFunc(path)
		if err != nil {
			// Best-effort: an unparsable directory is not fatal to
			// discovery as a whole.
			return nil
		}
		if !hasMain {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if seen[rel] {
			return nil
		}
		seen[rel] = true
		out = append(out, CompositionRoot{Path: rel, Reason: "contains a func main() declaration"})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walking for composition roots: %w", walkErr)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// dirHasMainFunc reports whether dir directly contains a .go file
// declaring `package main` with a package-level `func main()`. It does not
// look into subdirectories.
func dirHasMainFunc(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}

	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			// Skip files that fail to parse rather than aborting all of
			// discovery over one malformed file.
			continue
		}
		if f.Name == nil || f.Name.Name != "main" {
			continue
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if fn.Name.Name == "main" {
				return true, nil
			}
		}
	}
	return false, nil
}

// discoverInstructionFiles walks root looking for well-known
// agent-instruction file basenames anywhere in the tree.
func discoverInstructionFiles(root string) ([]InstructionFile, error) {
	out := make([]InstructionFile, 0)

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !wellKnownInstructionNames[d.Name()] {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		out = append(out, InstructionFile{Path: filepath.ToSlash(rel), Name: d.Name()})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walking for instruction files: %w", walkErr)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// makeTargetRe matches a plain "target:" line at column zero. It is a
// deliberately simple line-based parser (no Makefile grammar support for
// pattern rules, multi-target lines, or line continuations), sufficient for
// enumerating target names without executing make.
var makeTargetRe = regexp.MustCompile(`^([A-Za-z0-9_-]+):`)

// discoverMakeTargets parses a Makefile at root's top level (if present)
// for target names, both from a ".PHONY:" line and plain "target:" lines
// at column zero. It returns an empty (never nil) slice if there is no
// Makefile at root.
func discoverMakeTargets(root string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		if os.IsNotExist(err) {
			return make([]string, 0), nil
		}
		return nil, fmt.Errorf("reading Makefile: %w", err)
	}

	names := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "\t"):
			// Recipe line, not a target declaration.
			continue
		case strings.HasPrefix(line, "#"):
			continue
		case strings.Contains(line, ":="):
			// Variable assignment (including "FOO:=bar" with no space),
			// never a target line.
			continue
		case strings.HasPrefix(line, ".PHONY:"):
			for _, name := range strings.Fields(strings.TrimPrefix(line, ".PHONY:")) {
				names[name] = true
			}
			continue
		}
		if m := makeTargetRe.FindStringSubmatch(line); m != nil {
			names[m[1]] = true
		}
	}

	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// discoverCIWorkflows lists *.yml/*.yaml files directly under
// .github/workflows/ at root, if that directory exists.
func discoverCIWorkflows(root string) ([]string, error) {
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return make([]string, 0), nil
		}
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// discoverIndexes reports presence/absence of each named directory at
// root, sorted alphabetically by name so the result is deterministic
// regardless of the order names is given in.
func discoverIndexes(root string, names []string) []IndexStatus {
	out := make([]IndexStatus, 0, len(names))
	for _, name := range names {
		info, err := os.Stat(filepath.Join(root, name))
		out = append(out, IndexStatus{Name: name, Present: err == nil && info.IsDir()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// discoverTools reports presence/absence on PATH of each named binary,
// sorted alphabetically by name. It never executes any of them, only
// resolves them via exec.LookPath. A missing tool is always present in the
// returned slice with Present: false — never silently dropped.
func discoverTools(names []string) []ToolStatus {
	out := make([]ToolStatus, 0, len(names))
	for _, name := range names {
		path, err := exec.LookPath(name)
		out = append(out, ToolStatus{Name: name, Present: err == nil, Path: path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// discoverGitStatus reports whether root is a git repository (a .git entry
// exists, directory or file — the latter covering git worktrees) and, if
// so, whether it has a dirty worktree per `git status --porcelain`.
//
// If root is not a git repository, discoverGitStatus returns (false,
// false) — "not a git repository" is a normal, expected outcome, never an
// error. If a .git entry exists but `git status` cannot be run against it
// (git missing from PATH, or a non-functional .git directory such as a
// test fixture's bare os.Mkdir(".git")), discoverGitStatus still reports
// IsGitRepo: true (a .git entry is present) but leaves Dirty at its zero
// value (false) rather than failing discovery as a whole.
func discoverGitStatus(root string) (isGitRepo, dirty bool) {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return false, false
	}

	out, err := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	if err != nil {
		return true, false
	}
	return true, len(strings.TrimSpace(string(out))) > 0
}
