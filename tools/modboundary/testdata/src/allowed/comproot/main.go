// Package main is a composition root that wires moda and modb's service
// package directly. Composition roots are exempt from the module boundary
// rule, so this import must not be flagged even though it reaches into
// modb's non-ports internals.
package main

import (
	_ "allowed/moda" // wired for side effects only; the composition root does not call into moda directly
	_ "allowed/modb" // wired for side effects only; the composition root does not call into modb directly
)

// main is intentionally empty; this fixture only needs to compile so the
// module boundary checker can analyze its imports.
func main() {}
