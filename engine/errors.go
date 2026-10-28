package engine

import (
	"errors"
	"fmt"
	"strings"
)

// ErrReadOnly is reported for a change that the server's permissions (or an
// explicit 403) do not allow; the local item was left or restored as is.
var ErrReadOnly = errors.New("not permitted on the server (read-only)")

// FileError records an action that failed for a single path.
type FileError struct {
	Path string
	Op   string
	Err  error
}

func (e FileError) Error() string {
	return fmt.Sprintf("%s %q: %v", e.Op, e.Path, e.Err)
}

func (e FileError) Unwrap() error { return e.Err }

// SyncError is returned by Run when one or more actions failed. The other
// actions of the cycle were still applied.
type SyncError struct {
	Errors []FileError
}

func (e *SyncError) Error() string {
	msgs := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		msgs[i] = fe.Error()
	}
	return fmt.Sprintf("%d action(s) failed: %s", len(e.Errors), strings.Join(msgs, "; "))
}

func (e *SyncError) Unwrap() []error {
	errs := make([]error, len(e.Errors))
	for i, fe := range e.Errors {
		errs[i] = fe
	}
	return errs
}
