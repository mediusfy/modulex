// Package ports is the interface boundary of modb, safe for other modules to
// import directly.
package ports

// Thing is an example port interface.
type Thing interface {
	Do() error
}
