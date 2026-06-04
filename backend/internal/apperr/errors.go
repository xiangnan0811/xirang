// Package apperr defines sentinel errors for domain-level error classification.
// Handlers and services should use errors.Is to check these instead of string matching.
package apperr

import "errors"

var (
	// ErrDuplicate indicates a unique constraint violation (e.g., duplicate name, host, email).
	ErrDuplicate = errors.New("resource already exists")

	// ErrNotFound indicates the requested resource does not exist.
	ErrNotFound = errors.New("resource not found")

	// ErrValidation indicates the input failed validation rules.
	ErrValidation = errors.New("input validation failed")

	// ErrConflict indicates a logical conflict (e.g., operation on wrong state).
	ErrConflict = errors.New("resource conflict")
)
