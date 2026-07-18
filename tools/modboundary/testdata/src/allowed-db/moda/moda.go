// Package moda is a test fixture module that references its own table,
// which should not be flagged.
package moda

const query = "SELECT * FROM orders WHERE total > 100"
