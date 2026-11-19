package domain

import "errors"

// Sentinel errors returned by repositories (wrapped with operation context).
// Services and handlers map them to transport-level responses; raw database
// errors are never exposed.
var (
	ErrNotFound      = errors.New("not found")
	ErrConflict      = errors.New("conflict")              // unique violation
	ErrReference     = errors.New("reference violation")   // foreign-key violation
	ErrInvalid       = errors.New("invalid input")         // check/not-null/type violation
	ErrSerialization = errors.New("serialization failure") // retryable: 40P01 deadlock / 40001 serialization
)
