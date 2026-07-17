// Package moda is a test fixture module that imports modb's internal
// service package directly, which should be flagged.
package moda

import "forbidden/modb/service" // want `package "forbidden/moda" \(module "moda"\) must not import "forbidden/modb/service" \(module "modb"\) directly; only \[ports\] subpackages may cross module boundaries`

// Consumer uses the concrete service implementation directly, bypassing the
// module boundary.
type Consumer struct {
	Svc *service.Service
}
