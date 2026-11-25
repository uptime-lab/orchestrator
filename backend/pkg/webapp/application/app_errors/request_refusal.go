package app_errors

import "net/http"

// RequestRefusalError is a refusal whose HTTP meaning the refusal SITE
// decides: 400 when the request itself must change (a field outside its
// grammar, a reference that does not resolve), 409 when the request is fine
// and the target's state refuses it. It implements
// results.OperatorFacingError, so every handler answers it as itself —
// including handlers with no arm for it, which would otherwise flatten it
// into a 500 that discards the remedy.
//
// Wrapping is the author's statement that err's text is written for the
// operator. Wrap only an error constructed at the refusal site or a domain
// validation error, never one that may carry a repository or transport
// failure: that text is shown verbatim.
//
// Unwrap keeps the inner error matchable, so an existing errors.Is arm for a
// sentinel inside keeps working.
type RequestRefusalError struct {
	status int
	code   string
	err    error
}

func (e *RequestRefusalError) Error() string     { return e.err.Error() }
func (e *RequestRefusalError) Unwrap() error     { return e.err }
func (e *RequestRefusalError) HTTPStatus() int   { return e.status }
func (e *RequestRefusalError) ErrorCode() string { return e.code }

// InvalidRequest marks err as a 400 refusal with the given machine-readable
// code. nil stays nil so call sites can wrap unconditionally.
func InvalidRequest(code string, err error) error {
	if err == nil {
		return nil
	}
	return &RequestRefusalError{status: http.StatusBadRequest, code: code, err: err}
}

// StateConflict marks err as a 409 refusal: the request is well formed and
// the target's current state refuses it. nil stays nil.
func StateConflict(code string, err error) error {
	if err == nil {
		return nil
	}
	return &RequestRefusalError{status: http.StatusConflict, code: code, err: err}
}
