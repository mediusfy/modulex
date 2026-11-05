package modboundary

import "testing"

// TestGlobToRegexp covers globToRegexp's guarantees:
//   - a pattern with no "**" (e.g. "*/migrations/*.sql") matches exactly what
//     filepath.Glob always matched: one directory segment, no deeper nesting
//     (backward compatibility for existing callers);
//   - a "**" segment matches zero or more entire path segments, so
//     "**/migrations/*.sql" finds migrations directories at any depth;
//   - every other character is escaped and matched literally.
func TestGlobToRegexp(t *testing.T) {
	const (
		oneLevel  = "*/migrations/*.sql"
		anyDepth  = "**/migrations/*.sql"
		literal   = "migrations/v1.2+build.sql"
		usersFile = "migrations/001_create_users.sql"
	)
	tests := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{"one-level matches single directory", oneLevel, "modb/" + usersFile, true},
		{"one-level rejects zero levels", oneLevel, usersFile, false},
		{"one-level rejects two levels", oneLevel, "modb/sub/" + usersFile, false},
		{"one-level rejects extra trailing depth", oneLevel, "modb/migrations/nested/001_create_users.sql", false},
		{"one-level rejects wrong extension", oneLevel, "modb/migrations/001_create_users.txt", false},
		{"double-star matches zero levels", anyDepth, usersFile, true},
		{"double-star matches one level", anyDepth, "modb/" + usersFile, true},
		{"double-star matches two levels", anyDepth, "modb/sub/" + usersFile, true},
		{"double-star matches many levels", anyDepth, "modb/sub/deep/nested/" + usersFile, true},
		{"double-star rejects wrong extension", anyDepth, "modb/migrations/001_create_users.txt", false},
		{"double-star requires exact directory name", anyDepth, "modb/notmigrations/001_create_users.sql", false},
		{"literal dot and plus match literally", literal, literal, true},
		{"escaped dot is not a wildcard", literal, "migrations/v1X2+build.sql", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re, err := globToRegexp(tt.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if got := re.MatchString(tt.path); got != tt.want {
				t.Errorf("globToRegexp(%q).MatchString(%q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}
