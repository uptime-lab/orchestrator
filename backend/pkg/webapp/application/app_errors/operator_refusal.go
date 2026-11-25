package app_errors

// OperatorRefusalError marks an error whose text is safe to show the caller BY
// CONSTRUCTION: a deliberate refusal worded for the operator ("node offline",
// "no device profile is assigned"), never wrapped platform internals. Handlers
// answer it 400 with its message; an error NOT carrying the marker must stay a
// generic 500 — dispatch mappers once echoed raw repository and transport
// error text through a catch-all
// `SendValidationError(c, err.Error())`.
//
// Unwrap keeps whatever is inside matchable: a typed
// results.OperatorFacingError wrapped in the marker still answers as itself
// when the handler checks errors.As for it FIRST (all of them do).
type OperatorRefusalError struct{ Err error }

func (e *OperatorRefusalError) Error() string { return e.Err.Error() }

func (e *OperatorRefusalError) Unwrap() error { return e.Err }

// OperatorRefusal wraps err as operator-facing. nil stays nil so call sites can
// wrap unconditionally.
func OperatorRefusal(err error) error {
	if err == nil {
		return nil
	}
	return &OperatorRefusalError{Err: err}
}
