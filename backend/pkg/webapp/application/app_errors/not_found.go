package app_errors

import "net/http"

// NotFoundError is a lookup sentinel that declares its own HTTP meaning: 404,
// with the sentinel's fixed text as the message and code.
//
// These are the lookups a request names by id — a node, a unit, an SSH key —
// and the services return them from deep inside a create, an attach or a
// scope check, wrapped with context. Without a status of their own they
// reached results.SendInternalServerError at every handler that had no arm for
// them and answered 500: an ordinary "that id is not in your account" read as
// a server fault, logged at ERROR, while the routes that did have an arm said
// 404. Declaring it here makes every handler answer the same, including ones
// written later, and makes another account's id and an id that does not exist
// indistinguishable everywhere — the lookups are account-scoped, so both are
// the same miss.
//
// Each sentinel is ONE pointer, so errors.Is keeps matching it exactly as it
// matched the errors.New value it replaces. Error() is the fixed sentinel
// text and nothing else, which is what makes it safe to show.
type NotFoundError struct {
	msg  string
	code string
}

// newNotFound returns the sentinel as an error, so the variables keep the type
// they had as errors.New values.
func newNotFound(msg, code string) error { return &NotFoundError{msg: msg, code: code} }

func (e *NotFoundError) Error() string     { return e.msg }
func (e *NotFoundError) HTTPStatus() int   { return http.StatusNotFound }
func (e *NotFoundError) ErrorCode() string { return e.code }
