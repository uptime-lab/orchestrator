package results

// OperatorFacingError is a domain error that has already decided what it means
// over HTTP: the status a client should branch on, a machine-readable code, and
// a message its author vouches is safe to show a user.
//
// It exists because per-handler mapping does not hold. The typed refusals are
// raised at ONE choke point (command dispatch) and surface through a dozen
// handlers, so every handler needs an arm for every type — and the arm that is
// missing is invisible: the error flattens into the generic 500 and the operator
// is told the platform broke. It has happened more than once on the same error
// class: AgentTooOldError reached the generic command route as
// "failed to send command", and WatcherNodeError reached provision-eeprom as
// "failed to provision EEPROM" — while the error text itself carried the
// remedy the operator needed, in both cases.
//
// Implementing this interface is therefore how a typed error states its
// meaning ONCE, for every site that returns it, including sites written later.
//
// The opt-in matters for a second reason: SendInternalServerError deliberately
// does NOT echo the wrapped error, because an arbitrary error can carry SQL
// text, table names or filesystem paths. A type implementing this interface is
// its author saying the message is written FOR an operator — so only those are
// shown.
type OperatorFacingError interface {
	error
	// HTTPStatus is the status this refusal deserves. Use the one the client's
	// next step implies: 400 when the request must change, 409 when the request
	// is fine and the target's state refuses it.
	HTTPStatus() int
	// ErrorCode is the stable, machine-readable code (snake_case) placed in the
	// response's `code` field, e.g. "watcher_node".
	ErrorCode() string
}

// OperatorFacingDetails is the optional structured half of a refusal: the
// values the message was built from, placed in the response's `data` field so
// a client can render its own copy around them (a cap, a count, a URL)
// instead of parsing the sentence. Implementing it is, like the message, the
// author's statement that every value is safe to show.
type OperatorFacingDetails interface {
	ErrorDetails() any
}
