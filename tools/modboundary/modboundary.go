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
	Analyzer.Flags.StringVar(&dbSchemaFlag, "dbschema", "", "glob pattern for SQL migration files to extract and check cross-module table references; \"**\" recurses into subdirectories at any depth, e.g. \"**/migrations/*.sql\" (a plain \"*/migrations/*.sql\" without \"**\" only matches one directory level, as before)")
	Analyzer.Flags.StringVar(&sqlTablesFlag, "sqltables", "schema_migrations,goose_db_version,golang_migrations", "comma-separated list of shared table names exempt from cross-module checks")
}

func run(pass *analysis.Pass) (interface{}, error) {
	root := strings.TrimSuffix(rootFlag, "/")
	if root == "" {
		return nil, fmt.Errorf("modboundary: -root flag must be set to the import path prefix identifying the module boundary")
	}

	allowed := parseNameSet(allowFlag)
	allowedSQLTables := parseNameSet(sqlTablesFlag)

	if pass.Pkg.Name() == "main" {
		return nil, nil
	}

	currentModule, ok := analyzedModule(pass.Pkg.Path(), root)
	if !ok {
		return nil, nil
	}

	// Load schema table ownership if -dbschema is set.
	var schemaTables map[string]string // table name -> owning module
	if dbSchemaFlag != "" {
		var err error
		schemaTables, err = loadSchemaTables(pass, root, dbSchemaFlag, allowedSQLTables)
		if err != nil {
			return nil, fmt.Errorf("modboundary: failed to load schema: %w", err)
		}
	}

	checkImports(pass, root, currentModule, allowed)

	// Check for cross-module table references of string literals.
	if schemaTables != nil {
		checkTableReferences(pass, schemaTables, currentModule)
	}

	return nil, nil
}

// parseNameSet splits a comma-separated flag value into a set, trimming
// whitespace and dropping empty entries.
func parseNameSet(csv string) map[string]bool {
	set := make(map[string]bool)
	for _, name := range strings.Split(csv, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			set[name] = true
		}
	}
	return set
}

// analyzedModule resolves the feature module the package at pkgPath belongs
// to (with an external-test suffix normalized away). It reports false when
// the package is outside root or is a synthesized ".test" main package,
// neither of which is checked.
func analyzedModule(pkgPath, root string) (string, bool) {
	raw, ok := moduleOf(pkgPath, root)
	if !ok || strings.HasSuffix(raw, ".test") {
		return "", false
	}
	return normalizeTestModule(raw), true
}

// checkImports reports every import in the package's files that crosses a
// module boundary through a non-allow-listed subpackage.
func checkImports(pass *analysis.Pass, root, currentModule string, allowed map[string]bool) {
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
}

// checkTableReferences reports comments and string literals that reference a
// table owned by a different module.
func checkTableReferences(pass *analysis.Pass, schemaTables map[string]string, currentModule string) {
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

// sqlKeywordPattern matches any of isLikelySQLReference's SQL keywords as a
// whole word, so e.g. "set" does not fire inside "assets" or "on" inside
// "constitution".
var sqlKeywordPattern = regexp.MustCompile(`\b(select|from|where|join|insert|update|delete|into|set|on|table|alter|drop|create|truncate|grant)\b`)

// isLikelySQLReference checks whether a lowercased string containing a
// lowercased table name (the caller already confirmed this via a
// word-boundary match) is likely a genuine SQL reference rather than an
// unrelated string that merely happens to contain the table name as a
// substring (e.g. an error message like "orders not found" next to a table
// named "orders"). It looks for SQL keywords near the table name, or — since
// a bare constant value legitimately has no keyword at all — treats the
// string as a likely reference only when it is nothing but the table name
// itself (e.g. a `TableName = "orders"` constant), not merely a string that
// contains it somewhere.
func isLikelySQLReference(lower, table string) bool {
	if sqlKeywordPattern.MatchString(lower) {
		return true
	}
	return strings.TrimSpace(lower) == table
}

// globToRegexp translates a shell-style glob pattern into an anchored
// regular expression matching a "/"-separated relative path, with "**"
// given genuine recursive-directory semantics that neither Go's
// path/filepath.Match/Glob nor a plain shell (without "shopt -s globstar")
// provide on their own: a "**" path segment matches zero or more entire
// path segments (so "**/migrations/*.sql" matches "migrations/x.sql" at
// the root AND "a/b/c/migrations/x.sql" at any depth), while a lone "*"
// still matches only within a single segment (never crossing "/"), exactly
// matching filepath.Match's existing behavior for a pattern that contains
// no "**" — this is what keeps an existing one-level pattern like
// "*/migrations/*.sql" resolving to precisely the same matches it always
// has, for full backward compatibility with any caller already using this
// flag as documented.
func globToRegexp(pattern string) (*regexp.Regexp, error) {
	segments := strings.Split(filepath.ToSlash(pattern), "/")

	// Translate every non-"**" segment first, so the join step below only
	// has to decide what separator (a literal "/", or nothing — a "**"
	// group supplies its own trailing "/" when it matches at least one
	// segment) belongs between two adjacent translated pieces.
	type piece struct {
		text         string
		isDoubleStar bool
	}
	pieces := make([]piece, len(segments))
	for i, seg := range segments {
		if seg == "**" {
			// Matches zero or more entire path segments, each followed by
			// its "/" — the group is optional so "**" can also match zero
			// segments (letting "**/migrations/*.sql" match a migrations
			// directory directly under the walked root, not only a nested
			// one).
			pieces[i] = piece{text: `(?:.+/)?`, isDoubleStar: true}
			continue
		}
		var b strings.Builder
		for _, r := range seg {
			switch r {
			case '*':
				b.WriteString(`[^/]*`)
			case '?':
				b.WriteString(`[^/]`)
			default:
				b.WriteString(regexp.QuoteMeta(string(r)))
			}
		}
		pieces[i] = piece{text: b.String()}
	}

	var out strings.Builder
	out.WriteString("^")
	for i, p := range pieces {
		if i > 0 && !pieces[i-1].isDoubleStar {
			// The previous piece was a normal segment (not "**"), so the
			// "/" that separated these two pattern segments still needs to
			// be emitted literally. When the previous piece WAS "**", its
			// own "(?:.+/)?" group already accounts for that separator.
			out.WriteString("/")
		}
		out.WriteString(p.text)
	}
	out.WriteString("$")
	return regexp.Compile(out.String())
}

// loadSchemaTables scans SQL migration files matching dbschemaGlob under the
// root directory and returns a map of table name to owning module name (the
// first path segment below root). dbschemaGlob may use "**" for genuine
// recursive-directory matching (see globToRegexp); a pattern without "**"
// matches exactly as filepath.Glob always has, so an existing one-level
// pattern such as "*/migrations/*.sql" is unaffected.
func loadSchemaTables(pass *analysis.Pass, root, dbschemaGlob string, allowedTables map[string]bool) (map[string]string, error) {
	tables := make(map[string]string)

	// Determine the base directory on disk from the root import path.
	// We use the first package file's position to derive the filesystem root.
	dir, ok := packageDir(pass)
	if !ok {
		return tables, nil
	}

	// Walk up to find the root directory. We look for a directory named after
	// the last segment of the root import path, or we anchor at the package dir.
	moduleRoot := findModuleRoot(dir, root)

	matcher, err := globToRegexp(dbschemaGlob)
	if err != nil {
		return nil, err
	}
	matches, walkErr := findSchemaFiles(moduleRoot, matcher)
	if walkErr != nil {
		if os.IsNotExist(walkErr) {
			return tables, nil
		}
		return nil, walkErr
	}

	for _, match := range matches {
		addTablesFromFile(tables, moduleRoot, match, allowedTables)
	}
	return tables, nil
}

// packageDir returns the on-disk directory of the package's first file, or
// false when the pass has no files or no position information.
func packageDir(pass *analysis.Pass) (string, bool) {
	if len(pass.Files) == 0 {
		return "", false
	}
	fsetFile := pass.Fset.File(pass.Files[0].Pos())
	if fsetFile == nil {
		return "", false
	}
	return filepath.Dir(fsetFile.Name()), true
}

// findSchemaFiles walks moduleRoot and returns every file whose
// slash-separated path relative to moduleRoot matches matcher.
func findSchemaFiles(moduleRoot string, matcher *regexp.Regexp) ([]string, error) {
	var matches []string
	err := filepath.WalkDir(moduleRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(moduleRoot, path)
		if relErr != nil {
			return nil
		}
		if matcher.MatchString(filepath.ToSlash(rel)) {
			matches = append(matches, path)
		}
		return nil
	})
	return matches, err
}

// createStatementRE extracts the object name from a CREATE TABLE/VIEW
// statement.
var createStatementRE = regexp.MustCompile(`(?i)CREATE\s+(TABLE|VIEW)\s+(\S+)`)

// addTablesFromFile records the tables and views created in the migration
// file at path into tables, owned by the module named by path's first
// directory segment below moduleRoot. Unreadable files are skipped.
func addTablesFromFile(tables map[string]string, moduleRoot, path string, allowedTables map[string]bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	rel, err := filepath.Rel(moduleRoot, path)
	if err != nil {
		return
	}
	// The module name is the first directory segment below root.
	module := strings.SplitN(rel, string(filepath.Separator), 2)[0]

	for _, m := range createStatementRE.FindAllSubmatch(data, -1) {
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
