package modboundary_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/mediusfy/modulex/tools/modboundary"
)

func TestAllowedImportGraph(t *testing.T) {
	testdata := analysistest.TestData()
	if err := modboundary.Analyzer.Flags.Set("root", "allowed"); err != nil {
		t.Fatal(err)
	}
	analysistest.Run(t, testdata, modboundary.Analyzer, "allowed/...")
}

func TestForbiddenImportGraph(t *testing.T) {
	testdata := analysistest.TestData()
	if err := modboundary.Analyzer.Flags.Set("root", "forbidden"); err != nil {
		t.Fatal(err)
	}
	analysistest.Run(t, testdata, modboundary.Analyzer, "forbidden/...")
}
