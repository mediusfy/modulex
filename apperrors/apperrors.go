// Package apperrors provides the constant-string sentinel error type used
// across Modulex (see CODING_STANDARDS.md §5).
//
// Declaring sentinels as typed string constants makes them immutable and
// comparable, so they can neither be reassigned nor accidentally compared by
// pointer identity. errors.Is and errors.As work on them as on any error.
package apperrors

// Error is a constant-string error. Declare sentinels as typed constants:
//
//	const ErrNotFound apperrors.Error = "not found"
type Error string

// Error implements the error interface.
func (e Error) Error() string { return string(e) }
