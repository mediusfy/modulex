package modboundary

import "testing"

// TestGlobToRegexp_OneLevelPatternUnchanged is a regression test for the
// exact backward-compatibility guarantee globToRegexp's doc comment
// promises: a pattern with no "**" (e.g. the flag's own documented example,
// "*/migrations/*.sql") must match precisely what filepath.Glob always
// matched -- one directory segment between the walked root and
// "migrations" -- and must NOT match a migrations directory nested two or
// more levels deep. Before this fix, loadSchemaTables used filepath.Glob
// directly, which silently skipped any more-deeply-nested migrations
// directory; this test locks in that a plain (non-"**") pattern keeps that
// same one-level-only matching, so existing callers see no behavior change.
func TestGlobToRegexp_OneLevelPatternUnchanged(t *testing.T) {
	re, err := globToRegexp("*/migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want bool
	}{
		{"modb/migrations/001_create_users.sql", true},
		{"migrations/001_create_users.sql", false},             // zero levels: not matched by a one-level pattern
		{"modb/sub/migrations/001_create_users.sql", false},    // two levels: not matched by a one-level pattern
		{"modb/migrations/nested/001_create_users.sql", false}, // extra trailing depth
		{"modb/migrations/001_create_users.txt", false},        // wrong extension
	}
	for _, c := range cases {
		if got := re.MatchString(c.path); got != c.want {
			t.Errorf("globToRegexp(%q).MatchString(%q) = %v, want %v", "*/migrations/*.sql", c.path, got, c.want)
		}
	}
}

// TestGlobToRegexp_DoubleStarMatchesAnyDepth proves the actual fix: a "**"
// segment matches zero or more entire path segments, so
// "**/migrations/*.sql" finds a migrations directory regardless of how
// deeply nested it is under the walked root -- the class of migration
// files a plain one-level glob (in Go's filepath.Glob, or in a shell
// without "shopt -s globstar") silently misses.
func TestGlobToRegexp_DoubleStarMatchesAnyDepth(t *testing.T) {
	re, err := globToRegexp("**/migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want bool
	}{
		{"migrations/001_create_users.sql", true},                      // zero levels
		{"modb/migrations/001_create_users.sql", true},                 // one level
		{"modb/sub/migrations/001_create_users.sql", true},             // two levels
		{"modb/sub/deep/nested/migrations/001_create_users.sql", true}, // many levels
		{"modb/migrations/001_create_users.txt", false},                // wrong extension still rejected
		{"modb/notmigrations/001_create_users.sql", false},             // directory name must be exactly "migrations"
	}
	for _, c := range cases {
		if got := re.MatchString(c.path); got != c.want {
			t.Errorf("globToRegexp(%q).MatchString(%q) = %v, want %v", "**/migrations/*.sql", c.path, got, c.want)
		}
	}
}

// TestGlobToRegexp_InvalidPattern confirms a pattern regexp.QuoteMeta cannot
// rescue (there isn't really one, since every character is either escaped
// or translated) still compiles -- globToRegexp should never itself produce
// an invalid regexp for arbitrary glob input, since every character class
// it emits is a fixed, valid regexp fragment.
func TestGlobToRegexp_ArbitraryCharactersAreEscaped(t *testing.T) {
	re, err := globToRegexp("migrations/v1.2+build.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString("migrations/v1.2+build.sql") {
		t.Error("literal characters like '.' and '+' should be escaped and matched literally, not treated as regexp metacharacters")
	}
	if re.MatchString("migrations/v1X2+build.sql") {
		t.Error("an escaped '.' must not behave as a regexp wildcard")
	}
}
