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

func TestAllowedImportGraph(t *testing.T) {
	tests := []struct {
		name     string
		root     string
		dbschema string
		pattern  string
	}{
		{name: "allowed", root: "allowed", pattern: "allowed/..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetFlags(t)
			testdata := analysistest.TestData()
			if err := modboundary.Analyzer.Flags.Set("root", tt.root); err != nil {
				t.Fatal(err)
			}
			if tt.dbschema != "" {
				if err := modboundary.Analyzer.Flags.Set("dbschema", tt.dbschema); err != nil {
					t.Fatal(err)
				}
			}
			analysistest.Run(t, testdata, modboundary.Analyzer, tt.pattern)
		})
	}
}

func TestForbiddenImportGraph(t *testing.T) {
	tests := []struct {
		name     string
		root     string
		dbschema string
		pattern  string
	}{
		{name: "forbidden", root: "forbidden", pattern: "forbidden/..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetFlags(t)
			testdata := analysistest.TestData()
			if err := modboundary.Analyzer.Flags.Set("root", tt.root); err != nil {
				t.Fatal(err)
			}
			if tt.dbschema != "" {
				if err := modboundary.Analyzer.Flags.Set("dbschema", tt.dbschema); err != nil {
					t.Fatal(err)
				}
			}
			analysistest.Run(t, testdata, modboundary.Analyzer, tt.pattern)
		})
	}
}

func TestForbiddenDatabaseCrossReference(t *testing.T) {
	tests := []struct {
		name     string
		root     string
		dbschema string
		pattern  string
	}{
		{name: "moda references modb table", root: "forbidden-db", dbschema: "*/migrations/*.sql", pattern: "forbidden-db/moda"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetFlags(t)
			testdata := analysistest.TestData()
			if err := modboundary.Analyzer.Flags.Set("root", tt.root); err != nil {
				t.Fatal(err)
			}
			if tt.dbschema != "" {
				if err := modboundary.Analyzer.Flags.Set("dbschema", tt.dbschema); err != nil {
					t.Fatal(err)
				}
			}
			analysistest.Run(t, testdata, modboundary.Analyzer, tt.pattern)
		})
	}
}

func TestForbiddenDatabaseNoCrossReference(t *testing.T) {
	tests := []struct {
		name     string
		root     string
		dbschema string
		pattern  string
	}{
		// modb owns the users table and has no cross-module references.
		{name: "modb owns its table", root: "forbidden-db", dbschema: "*/migrations/*.sql", pattern: "forbidden-db/modb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetFlags(t)
			testdata := analysistest.TestData()
			if err := modboundary.Analyzer.Flags.Set("root", tt.root); err != nil {
				t.Fatal(err)
			}
			if tt.dbschema != "" {
				if err := modboundary.Analyzer.Flags.Set("dbschema", tt.dbschema); err != nil {
					t.Fatal(err)
				}
			}
			analysistest.Run(t, testdata, modboundary.Analyzer, tt.pattern)
		})
	}
}

func TestAllowedDatabaseSelfReference(t *testing.T) {
	tests := []struct {
		name     string
		root     string
		dbschema string
		pattern  string
	}{
		{name: "self reference", root: "allowed-db", dbschema: "*/migrations/*.sql", pattern: "allowed-db/..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetFlags(t)
			testdata := analysistest.TestData()
			if err := modboundary.Analyzer.Flags.Set("root", tt.root); err != nil {
				t.Fatal(err)
			}
			if tt.dbschema != "" {
				if err := modboundary.Analyzer.Flags.Set("dbschema", tt.dbschema); err != nil {
					t.Fatal(err)
				}
			}
			analysistest.Run(t, testdata, modboundary.Analyzer, tt.pattern)
		})
	}
}
