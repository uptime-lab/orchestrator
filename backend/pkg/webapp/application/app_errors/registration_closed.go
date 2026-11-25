package app_errors

import "net/http"

// RegistrationClosedErrorCode is the machine-readable code on the refusal,
// so a sign-up page can tell "closed" from a wrong invite code without
// parsing the sentence.
const RegistrationClosedErrorCode = "registration_closed"

// RegistrationClosedReason says why the instance refused, for the log; the
// caller is told only that registration is closed, because which reason it
// was is nothing a stranger at the sign-up page needs to learn.
type RegistrationClosedReason string

const (
	// ReasonNoInviteCode: the instance already has an account and no invite
	// code is configured — closed by default, from the first owner on.
	ReasonNoInviteCode RegistrationClosedReason = "no invite code configured"
	// ReasonAccountCeiling: SERVER_MAX_ACCOUNTS is reached.
	ReasonAccountCeiling RegistrationClosedReason = "account ceiling reached"
	// ReasonCountUnavailable: the account count could not be read, and a
	// sign-up is the one admission that is safe to refuse on doubt.
	ReasonCountUnavailable RegistrationClosedReason = "account count unavailable"
	// ReasonOpenWithoutMail: open registration is on, but the install cannot
	// send the confirmation mail an open sign-up must be verified with, so it
	// would only create accounts nobody can ever use.
	ReasonOpenWithoutMail RegistrationClosedReason = "open registration without mail delivery"
)

// RegistrationClosedError is the typed refusal of a self-registration. It is
// raised at the one place an account comes into existence and reaches the
// caller through whichever handler returns it, so it declares its own HTTP
// meaning here: 403, because nothing in the request was wrong and nothing
// the caller changes will open it — the operator has to.
//
// Error() is fixed and carries no reason: it is shown to the caller
// verbatim (the sign-up page renders the server's message), and the next
// step is the same for every reason — ask the operator of this server for
// an invitation into an existing account.
type RegistrationClosedError struct {
	Reason RegistrationClosedReason
}

func (e *RegistrationClosedError) Error() string {
	return "registration is currently closed; ask the operator of this server to invite you to an account"
}

func (e *RegistrationClosedError) HTTPStatus() int   { return http.StatusForbidden }
func (e *RegistrationClosedError) ErrorCode() string { return RegistrationClosedErrorCode }

// Unwrap keeps errors.Is(err, ErrRegistrationClosed) true for the callers
// that branched on the sentinel before the type existed.
func (e *RegistrationClosedError) Unwrap() error { return ErrRegistrationClosed }

// RegistrationBusyErrorCode is the machine-readable code on the refusal
// answered while too many open sign-ups wait for their confirmation.
const RegistrationBusyErrorCode = "registration_busy"

// RegistrationBusyError refuses an open sign-up while the cap on pending,
// unconfirmed sign-ups is reached (or, rarely, while the instance changed
// shape under a sign-up in flight). 429, not 403: the caller's next step is
// to wait, because pending sign-ups age out of the cap on their own. The
// message carries no count.
type RegistrationBusyError struct{}

func (e *RegistrationBusyError) Error() string {
	return "too many sign-ups are waiting for confirmation right now; try again later"
}

func (e *RegistrationBusyError) HTTPStatus() int   { return http.StatusTooManyRequests }
func (e *RegistrationBusyError) ErrorCode() string { return RegistrationBusyErrorCode }
