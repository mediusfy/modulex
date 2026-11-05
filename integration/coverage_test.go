//go:build unix

package integration_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// discoverMainDirs returns the repo-relative directories (slash-separated)
// that contain a non-test `package main` file, skipping analyzer fixtures
// (testdata), hidden directories, and nested worktrees.
func discoverMainDirs(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != repoRoot && (name == "testdata" || name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly)
		if perr != nil {
			return nil // not this test's concern; the build rows will surface it
		}
		if f.Name.Name == "main" {
			rel, rerr := filepath.Rel(repoRoot, filepath.Dir(path))
			if rerr != nil {
				return rerr
			}
			seen[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	require.NoError(t, err)
	dirs := make([]string, 0, len(seen))
	for d := range seen {
		dirs = append(dirs, d)
	}
	return dirs
}

// tableDirs returns the repo-relative directories the entryPoints table
// builds.
func tableDirs() map[string]bool {
	dirs := map[string]bool{}
	for _, ep := range entryPoints {
		dirs[filepath.ToSlash(filepath.Join(ep.moduleDir, ep.pkg))] = true
	}
	return dirs
}

// TestEveryMainPackageHasARow fails when a `package main` directory exists in
// the repository without a row in the entryPoints table, so a new entry point
// cannot ship without a smoke test.
func TestEveryMainPackageHasARow(t *testing.T) {
	covered := tableDirs()
	tests := []struct {
		name string
		dirs []string
	}{
		{name: "every discovered package main is covered", dirs: discoverMainDirs(t)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.NotEmpty(t, tc.dirs, "discovery found no package main directories; the walker is broken")
			for _, dir := range tc.dirs {
				assert.True(t, covered[dir], "package main in %q has no row in the entryPoints table (see integration/README.md)", dir)
			}
		})
	}
}

// TestEntryPointRowsAreWellFormed guards the table itself: unique names and
// every row pointing at a real directory.
func TestEntryPointRowsAreWellFormed(t *testing.T) {
	mains := map[string]bool{}
	for _, d := range discoverMainDirs(t) {
		mains[d] = true
	}
	names := map[string]int{}
	for _, ep := range entryPoints {
		names[ep.name]++
	}
	for _, ep := range entryPoints {
		t.Run(ep.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, 1, names[ep.name], "row names must be unique")
			assert.NotNil(t, ep.setup, "row needs a setup func")
			assert.True(t, mains[filepath.ToSlash(filepath.Join(ep.moduleDir, ep.pkg))], "row must point at a package main directory")
		})
	}
}
