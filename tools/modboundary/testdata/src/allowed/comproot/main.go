// Package main is a composition root that wires moda and modb's service
// package directly. Composition roots are exempt from the module boundary
// rule, so this import must not be flagged even though it reaches into
// modb's non-ports internals.
package main

import (
	_ "allowed/moda"
	_ "allowed/modb"
)

func main() {}
