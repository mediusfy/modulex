// Package ports is the interface boundary of modb, safe for other modules to
// import directly.
package ports

// Doer is an example port interface.
type Doer interface {
	Do() error
}
