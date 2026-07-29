// Package moda is a test fixture module that only imports modb's ports
// subpackage, which is allowed across module boundaries.
package moda

import "allowed/modb/ports"

// Consumer uses the ports.Doer interface exposed by modb.
type Consumer struct {
	Thing ports.Doer
}
