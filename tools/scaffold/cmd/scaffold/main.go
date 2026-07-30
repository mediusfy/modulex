// Command scaffold generates a modulex feature module using the
// recommended domain/ports/service/adapters/module.go layout. See
// docs/planning/scaffolding-and-test-harness-guide.md for a full
// walkthrough.
//
// Usage:
//
//	scaffold -name billing -out examples
//
// generates examples/billing/{domain,ports,service,adapters}/*.go and
// examples/billing/module.go (plus tests and a README.md), inferring the
// Go import path from the nearest go.mod above the output directory.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mediusfy/modulex/tools/scaffold"
)

func main() {
	name := flag.String("name", "", "feature name, e.g. \"billing\" (required)")
	out := flag.String("out", "", "parent directory to generate into, e.g. \"examples\" (required)")
	module := flag.String("module", "", "Go import path corresponding to -out; auto-detected from the nearest go.mod if omitted")
	force := flag.Bool("force", false, "overwrite the target directory if it already exists and is non-empty")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s -name <feature-name> -out <parent-dir> [-module <import-path>] [-force]\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if *name == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}

	result, err := scaffold.Generate(scaffold.Config{
		Name:         *name,
		OutDir:       *out,
		ModuleImport: *module,
		Force:        *force,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("generated %s:\n", result.TargetDir)
	for _, f := range result.Files {
		fmt.Printf("  %s\n", f)
	}
}
