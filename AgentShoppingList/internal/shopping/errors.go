package shopping

import (
	"errors"
	"fmt"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/store/sqlite"
)

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

// SafeError avoids leaking SQL or operational details through either adapter.
func SafeError(err error) string {
	if v, ok := errors.AsType[*ValidationError](err); ok {
		return v.Message
	}
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		return "Sign in to continue."
	case errors.Is(err, identity.ErrForbidden):
		return "Permission denied."
	case errors.Is(err, sqlite.ErrNotFound):
		return "Record not found."
	case errors.Is(err, sqlite.ErrConflict):
		return "The record changed. Refresh and use the current revision."
	case errors.Is(err, sqlite.ErrIdempotencyConflict):
		return "This idempotency key belongs to another request."
	default:
		return "The request could not be saved. Try again."
	}
}
