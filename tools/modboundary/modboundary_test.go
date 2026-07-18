package modboundary_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/mediusfy/modulex/tools/modboundary"
)

func resetFlags() {
	modboundary.Analyzer.Flags.Set("root", "")
	modboundary.Analyzer.Flags.Set("allow", "ports")
	modboundary.Analyzer.Flags.Set("dbschema", "")
	modboundary.Analyzer.Flags.Set("sqltables", "schema_migrations,goose_db_version,golang_migrations")
}

func TestAllowedImportGraph(t *testing.T) {
	resetFlags()
	testdata := analysistest.TestData()
	if err := modboundary.Analyzer.Flags.Set("root", "allowed"); err != nil {
		t.Fatal(err)
	}
	analysistest.Run(t, testdata, modboundary.Analyzer, "allowed/...")
}

func TestForbiddenImportGraph(t *testing.T) {
	resetFlags()
	testdata := analysistest.TestData()
	if err := modboundary.Analyzer.Flags.Set("root", "forbidden"); err != nil {
		t.Fatal(err)
	}
	analysistest.Run(t, testdata, modboundary.Analyzer, "forbidden/...")
}

func TestForbiddenDatabaseCrossReference(t *testing.T) {
	resetFlags()
	testdata := analysistest.TestData()
	if err := modboundary.Analyzer.Flags.Set("root", "forbidden-db"); err != nil {
		t.Fatal(err)
	}
	if err := modboundary.Analyzer.Flags.Set("dbschema", "*/migrations/*.sql"); err != nil {
		t.Fatal(err)
	}
	analysistest.Run(t, testdata, modboundary.Analyzer, "forbidden-db/moda")
}

func TestForbiddenDatabaseNoCrossReference(t *testing.T) {
	resetFlags()
	testdata := analysistest.TestData()
	if err := modboundary.Analyzer.Flags.Set("root", "forbidden-db"); err != nil {
		t.Fatal(err)
	}
	if err := modboundary.Analyzer.Flags.Set("dbschema", "*/migrations/*.sql"); err != nil {
		t.Fatal(err)
	}
	// modb owns the users table and has no cross-module references.
	analysistest.Run(t, testdata, modboundary.Analyzer, "forbidden-db/modb")
}

func TestAllowedDatabaseSelfReference(t *testing.T) {
	resetFlags()
	testdata := analysistest.TestData()
	if err := modboundary.Analyzer.Flags.Set("root", "allowed-db"); err != nil {
		t.Fatal(err)
	}
	if err := modboundary.Analyzer.Flags.Set("dbschema", "*/migrations/*.sql"); err != nil {
		t.Fatal(err)
	}
	analysistest.Run(t, testdata, modboundary.Analyzer, "allowed-db/...")
}
