// Package integration holds hermetic smoke tests for every real `package main`
// entry point in the repository. The tests live in the integration_test
// package: they build each binary with the toolchain, run it as a child
// process with a controlled environment, and assert on its observable
// behaviour (exit code, output, network responses, graceful shutdown).
//
// See README.md in this directory for how to run the tests and how to add a
// new entry point.
package integration
