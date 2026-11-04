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

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "modb/migrations/001_create_users.sql", path: "modb/migrations/001_create_users.sql", want: true},
		{name: "migrations/001_create_users.sql", path: "migrations/001_create_users.sql", want: false},                         // zero levels: not matched by a one-level pattern
		{name: "modb/sub/migrations/001_create_users.sql", path: "modb/sub/migrations/001_create_users.sql", want: false},       // two levels: not matched by a one-level pattern
		{name: "modb/migrations/nested/001_create_users.sql", path: "modb/migrations/nested/001_create_users.sql", want: false}, // extra trailing depth
		{name: "modb/migrations/001_create_users.txt", path: "modb/migrations/001_create_users.txt", want: false},               // wrong extension
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := re.MatchString(tt.path); got != tt.want {
				t.Errorf("globToRegexp(%q).MatchString(%q) = %v, want %v", "*/migrations/*.sql", tt.path, got, tt.want)
			}
		})
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

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "migrations/001_create_users.sql", path: "migrations/001_create_users.sql", want: true},                                           // zero levels
		{name: "modb/migrations/001_create_users.sql", path: "modb/migrations/001_create_users.sql", want: true},                                 // one level
		{name: "modb/sub/migrations/001_create_users.sql", path: "modb/sub/migrations/001_create_users.sql", want: true},                         // two levels
		{name: "modb/sub/deep/nested/migrations/001_create_users.sql", path: "modb/sub/deep/nested/migrations/001_create_users.sql", want: true}, // many levels
		{name: "modb/migrations/001_create_users.txt", path: "modb/migrations/001_create_users.txt", want: false},                                // wrong extension still rejected
		{name: "modb/notmigrations/001_create_users.sql", path: "modb/notmigrations/001_create_users.sql", want: false},                          // directory name must be exactly "migrations"
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := re.MatchString(tt.path); got != tt.want {
				t.Errorf("globToRegexp(%q).MatchString(%q) = %v, want %v", "**/migrations/*.sql", tt.path, got, tt.want)
			}
		})
	}
}

// TestGlobToRegexp_InvalidPattern confirms a pattern regexp.QuoteMeta cannot
// rescue (there isn't really one, since every character is either escaped
// or translated) still compiles -- globToRegexp should never itself produce
// an invalid regexp for arbitrary glob input, since every character class
// it emits is a fixed, valid regexp fragment.
func TestGlobToRegexp_ArbitraryCharactersAreEscaped(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		match   string
		noMatch string
	}{
		{
			name:    "literal dot and plus are escaped",
			pattern: "migrations/v1.2+build.sql",
			match:   "migrations/v1.2+build.sql",
			noMatch: "migrations/v1X2+build.sql",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re, err := globToRegexp(tt.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if !re.MatchString(tt.match) {
				t.Error("literal characters like '.' and '+' should be escaped and matched literally, not treated as regexp metacharacters")
			}
			if re.MatchString(tt.noMatch) {
				t.Error("an escaped '.' must not behave as a regexp wildcard")
			}
		})
	}
}
