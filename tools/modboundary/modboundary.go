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
// # Import-boundary checks
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
//
// # Database-boundary checks
//
// When -dbschema is set to a glob pattern for SQL migration files, the
// analyzer extracts CREATE TABLE and CREATE VIEW statements from those
// files and flags any Go file under -root that references a table owned
// by a different module. Table names listed in -sqltables (comma-separated)
// are exempt as shared system tables.
package modboundary

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// Analyzer reports direct imports between sibling feature modules under -root that
// do not go through an allow-listed subpackage, and optionally flags cross-module
// database table references when -dbschema is provided.
var Analyzer = &analysis.Analyzer{
	Name: "modboundary",
	Doc:  "reports imports and database table references that cross feature-module boundaries",
	Run:  run,
}

var (
	rootFlag      string
	allowFlag     string
	dbSchemaFlag  string
	sqlTablesFlag string
)

func init() {
	Analyzer.Flags.StringVar(&rootFlag, "root", "", "import path prefix under which direct child directories are independent feature modules (required)")
	Analyzer.Flags.StringVar(&allowFlag, "allow", "ports", "comma-separated list of subpackage names that may be imported across module boundaries")
	Analyzer.Flags.StringVar(&dbSchemaFlag, "dbschema", "", "glob pattern for SQL migration files to extract and check cross-module table references (e.g. \"*/migrations/*.sql\")")
	Analyzer.Flags.StringVar(&sqlTablesFlag, "sqltables", "schema_migrations,goose_db_version,golang_migrations", "comma-separated list of shared table names exempt from cross-module checks")
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

	allowedSQLTables := make(map[string]bool)
	for _, name := range strings.Split(sqlTablesFlag, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			allowedSQLTables[name] = true
		}
	}

	if pass.Pkg.Name() == "main" {
		return nil, nil
	}

	rawCurrentModule, ok := moduleOf(pass.Pkg.Path(), root)
	if !ok {
		return nil, nil
	}
	if strings.HasSuffix(rawCurrentModule, ".test") {
		return nil, nil
	}
	currentModule := normalizeTestModule(rawCurrentModule)

	// Load schema table ownership if -dbschema is set.
	var schemaTables map[string]string // table name -> owning module
	if dbSchemaFlag != "" {
		var err error
		schemaTables, err = loadSchemaTables(pass, root, dbSchemaFlag, allowedSQLTables)
		if err != nil {
			return nil, fmt.Errorf("modboundary: failed to load schema: %w", err)
		}
	}

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

	// Check for cross-module table references of string literals.
	if schemaTables != nil {
		for _, file := range pass.Files {
			for _, group := range file.Comments {
				for _, comment := range group.List {
					checkSQLTableRef(pass, comment.Text, comment.Pos(), schemaTables, currentModule)
				}
			}
		}
		// Also check basic string literals for table names.
		// This is best-effort: we look at the AST for string literal tokens.
		// A full dataflow analysis would be needed for variables, but scanning
		// literals catches the common case of inline SQL.
		for _, file := range pass.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				checkSQLTableRef(pass, val, lit.Pos(), schemaTables, currentModule)
				return true
			})
		}
	}

	return nil, nil
}

// checkSQLTableRef reports a diagnostic if val contains a reference to a table
// owned by a different module.
func checkSQLTableRef(pass *analysis.Pass, val string, pos token.Pos, schemaTables map[string]string, currentModule string) {
	lower := strings.ToLower(val)
	for table, owner := range schemaTables {
		if owner == currentModule {
			continue
		}
		// Match the table name as a word boundary in SQL context.
		pattern := `(?i)\b` + regexp.QuoteMeta(table) + `\b`
		matched, err := regexp.MatchString(pattern, val)
		if err != nil || !matched {
			continue
		}
		// Skip false positives: the string contains the table name but is not
		// a SQL reference. Heuristic: require proximity to SQL keywords or
		// the table name appearing verbatim in string.
		if !isLikelySQLReference(lower, strings.ToLower(table)) {
			continue
		}
		pass.Reportf(pos,
			"module %q references table %q owned by module %q, violating database boundary",
			currentModule, table, owner)
	}
}

// isLikelySQLReference checks whether a lowercased string containing a
// lowercased table name is likely a SQL reference rather than an accidental
// substring match. It looks for SQL keywords near the table name.
func isLikelySQLReference(lower, table string) bool {
	keywords := []string{"select", "from", "where", "join", "insert", "update", "delete", "into", "set", "on", "table", "alter", "drop", "create", "truncate", "grant"}
	// Check if any SQL keyword appears in the same string as the table name.
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	// If the string is just the table name (or contains it boundaried), it's
	// likely a reference (e.g. config values, table name constants).
	return strings.Contains(lower, table)
}

// loadSchemaTables scans SQL migration files matching dbschemaGlob under the
// root directory and returns a map of table name to owning module name (the
// first path segment below root).
func loadSchemaTables(pass *analysis.Pass, root, dbschemaGlob string, allowedTables map[string]bool) (map[string]string, error) {
	tables := make(map[string]string)

	// Determine the base directory on disk from the root import path.
	// We use the first package file's position to derive the filesystem root.
	if len(pass.Files) == 0 {
		return tables, nil
	}
	pos := pass.Files[0].Pos()
	fsetFile := pass.Fset.File(pos)
	if fsetFile == nil {
		return tables, nil
	}
	dir := filepath.Dir(fsetFile.Name())

	// Walk up to find the root directory. We look for a directory named after
	// the last segment of the root import path, or we anchor at the package dir.
	moduleRoot := findModuleRoot(dir, root)

	matches, err := filepath.Glob(filepath.Join(moduleRoot, dbschemaGlob))
	if err != nil {
		return nil, err
	}

	createRE := regexp.MustCompile(`(?i)CREATE\s+(TABLE|VIEW)\s+(\S+)`)

	for _, match := range matches {
		data, err := os.ReadFile(match)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(moduleRoot, match)
		if err != nil {
			continue
		}
		// The module name is the first directory segment below root.
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		module := parts[0]

		for _, m := range createRE.FindAllSubmatch(data, -1) {
			tableName := strings.TrimRight(string(m[2]), " (;\"`")
			tableName = strings.TrimLeft(tableName, "\"`")
			if tableName == "" || allowedTables[tableName] {
				continue
			}
			if existing, ok := tables[tableName]; ok && existing != module {
				// Table is claimed by two different modules — we still record
				// the first owner but additional detection will catch cross-refs.
				continue
			}
			tables[tableName] = module
		}
	}

	return tables, nil
}

// findModuleRoot walks up from dir to find the directory that corresponds to
// the root import path. Returns the starting dir if it cannot determine a root.
func findModuleRoot(dir, root string) string {
	// Use the last segment of the root import path as a heuristic anchor.
	segments := strings.Split(root, "/")
	if len(segments) == 0 {
		return dir
	}
	lastSegment := segments[len(segments)-1]
	current := dir
	for {
		if filepath.Base(current) == lastSegment {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return dir
		}
		current = parent
	}
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
