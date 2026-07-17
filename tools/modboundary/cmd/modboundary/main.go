// Command modboundary runs the modboundary analyzer as a standalone vet-style
// tool. Example:
//
//	go run ./tools/modboundary/cmd/modboundary \
//	  -root=github.com/mediusfy/modulex/examples/deployment \
//	  ./examples/deployment/...
package main

import (
	"github.com/mediusfy/modulex/tools/modboundary"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(modboundary.Analyzer)
}
