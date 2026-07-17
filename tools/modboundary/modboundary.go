// Package modboundary provides a go/analysis Analyzer that flags direct
// imports between sibling feature modules, enforcing the pattern used
// throughout Modulex's examples: modules communicate through the shared
// registry (typed services and events), not by importing each other's
// implementation packages directly.
//
// This is deliberately a separate, optional tool rather than an enforcement
// mechanism baked into the core modulex package: Modulex does not claim to
// enforce architectural boundaries at compile time (see
// docs/adr/adr-0030-modulex-release-readiness.md), and consumers who want
// static enforcement can opt into this analyzer without the core package
// depending on golang.org/x/tools.
//
// A "module" is the first import-path segment below the configured -root
// flag. A file belongs to the module its own package path resolves to. An
// import is flagged if:
//   - its path also has the -root prefix,
//   - its module differs from the current file's module, and
//   - the imported package's last path segment is not in the -allow list
//     (a comma-separated list of subpackage names that are safe to share
//     across module boundaries, e.g. "ports"; default "ports").
//
// Given root "example.com/app/modules", a file in
// "example.com/app/modules/orders" importing
// "example.com/app/modules/billing/ports" is allowed, but importing
// "example.com/app/modules/billing/service" is flagged.
//
// Composition roots (package main) are exempt: wiring modules together by
// importing their constructors is their entire purpose, so files in a
// "package main" are never checked as importers. External test packages
// (files declaring "package foo_test") are treated as part of the module
// they test, so a module's own tests may import it freely.
package modboundary

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// Analyzer reports direct imports between sibling modules under -root that
// do not go through an allow-listed subpackage.
var Analyzer = &analysis.Analyzer{
	Name: "modboundary",
	Doc:  "reports imports that cross feature-module boundaries without going through an allow-listed subpackage",
	Run:  run,
}

var (
	rootFlag  string
	allowFlag string
)

func init() {
	Analyzer.Flags.StringVar(&rootFlag, "root", "", "import path prefix under which direct child directories are independent feature modules (required)")
	Analyzer.Flags.StringVar(&allowFlag, "allow", "ports", "comma-separated list of subpackage names that may be imported across module boundaries")
}

func run(pass *analysis.Pass) (interface{}, error) {
	root := strings.TrimSuffix(rootFlag, "/")
	if root == "" {
		return nil, fmt.Errorf("modboundary: -root flag must be set to the import path prefix identifying the module boundary")
	}

	allowed := make(map[string]bool)
	for _, name := range strings.Split(allowFlag, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			allowed[name] = true
		}
	}

	if pass.Pkg.Name() == "main" {
		// A composition root wires modules together and is expected to
		// import them directly; it sits outside the module boundary rule.
		//
		// This is keyed on the package name alone, not its position in the
		// tree, so it also exempts any "package main" nested inside a
		// module (e.g. a debug CLI under a module's own directory). That is
		// a deliberate false-negative: such a file is rare, and requiring
		// -root-relative depth tracking to distinguish "the" composition
		// root from a module-owned main would add configuration surface
		// for a case this repo does not have.
		return nil, nil
	}

	rawCurrentModule, ok := moduleOf(pass.Pkg.Path(), root)
	if !ok {
		// This package is not under -root at all; nothing to check.
		return nil, nil
	}
	if strings.HasSuffix(rawCurrentModule, ".test") {
		// The synthetic "main" package the Go toolchain generates to build a
		// test binary; it has no user-authored imports worth checking.
		return nil, nil
	}
	currentModule := normalizeTestModule(rawCurrentModule)

	for _, file := range pass.Files {
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			rawImportedModule, ok := moduleOf(path, root)
			if !ok {
				continue
			}
			importedModule := normalizeTestModule(rawImportedModule)
			if importedModule == currentModule {
				continue
			}
			lastSegment := path[strings.LastIndex(path, "/")+1:]
			if allowed[lastSegment] {
				continue
			}
			pass.Reportf(imp.Pos(),
				"package %q (module %q) must not import %q (module %q) directly; only %v subpackages may cross module boundaries",
				pass.Pkg.Path(), currentModule, path, importedModule, sortedKeys(allowed))
		}
	}

	return nil, nil
}

// moduleOf returns the first path segment of importPath below root, and
// whether importPath is under root at all.
func moduleOf(importPath, root string) (string, bool) {
	if importPath == root {
		return "", false
	}
	prefix := root + "/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", false
	}
	rest := importPath[len(prefix):]
	if idx := strings.Index(rest, "/"); idx >= 0 {
		return rest[:idx], true
	}
	return rest, true
}

// normalizeTestModule maps a Go toolchain external-test-package module name
// (e.g. "notification_test", the package name for files declaring
// "package notification_test") back to the module it belongs to
// ("notification"), so a module's own external test files are never flagged
// for importing the package they test.
//
// This is a directory-name heuristic, not a precise signal: a module
// legitimately named with a "_test" suffix (e.g. "load_test") would have its
// violations silently merged into a same-prefixed module. That tradeoff is
// accepted here because Go's own "_test" package-name convention is far more
// common than a module intentionally named that way; a project that hits
// this collision should rename the module or fork the analyzer.
func normalizeTestModule(module string) string {
	return strings.TrimSuffix(module, "_test")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
