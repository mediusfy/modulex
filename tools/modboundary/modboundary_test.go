package modboundary_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/mediusfy/modulex/tools/modboundary"
)

func resetFlags(t *testing.T) {
	t.Helper()
	defaults := []struct{ name, value string }{
		{"root", ""},
		{"allow", "ports"},
		{"dbschema", ""},
		{"sqltables", "schema_migrations,goose_db_version,golang_migrations"},
	}
	for _, d := range defaults {
		if err := modboundary.Analyzer.Flags.Set(d.name, d.value); err != nil {
			t.Fatalf("resetting -%s: %v", d.name, err)
		}
	}
}

func TestBoundaryAnalysis(t *testing.T) {
	const dbGlob = "*/migrations/*.sql"
	tests := []struct {
		name     string
		root     string
		dbschema string
		pattern  string
	}{
		{name: "allowed import graph", root: "allowed", pattern: "allowed/..."},
		{name: "forbidden import graph", root: "forbidden", pattern: "forbidden/..."},
		{name: "moda references modb table", root: "forbidden-db", dbschema: dbGlob, pattern: "forbidden-db/moda"},
		// modb owns the users table and has no cross-module references.
		{name: "modb owns its table", root: "forbidden-db", dbschema: dbGlob, pattern: "forbidden-db/modb"},
		{name: "allowed database self reference", root: "allowed-db", dbschema: dbGlob, pattern: "allowed-db/..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetFlags(t)
			flags := map[string]string{"root": tt.root}
			if tt.dbschema != "" {
				flags["dbschema"] = tt.dbschema
			}
			for name, value := range flags {
				if err := modboundary.Analyzer.Flags.Set(name, value); err != nil {
					t.Fatalf("setting -%s: %v", name, err)
				}
			}
			analysistest.Run(t, analysistest.TestData(), modboundary.Analyzer, tt.pattern)
		})
	}
}
