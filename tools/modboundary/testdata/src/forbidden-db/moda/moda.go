// Package moda is a test fixture module that references another module's
// database table in a raw SQL string, which should be flagged.
package moda

const query = "SELECT * FROM users WHERE id = 1" // want `violating database boundary`
