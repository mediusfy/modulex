package main

import (
	"github.com/mediusfy/modulex/tools/modboundary"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(modboundary.Analyzer)
}
