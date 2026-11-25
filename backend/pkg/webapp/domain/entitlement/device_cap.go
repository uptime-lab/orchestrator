package entitlement

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"
)

// DefaultSubscriptionURL is where the refusal sends an operator who wants to
// admit more devices. An install may override it through configuration; the
// refusal always carries one, because a refusal that names no remedy is a
// dead end.
const DefaultSubscriptionURL = "https://orchestrator.sh/subscription/"

// DeviceCapErrorCode is the machine-readable code on the HTTP refusal. The
// add-node flows branch on it to render the subscription copy instead of a
// generic failure.
const DeviceCapErrorCode = "device_cap_reached"

// GRPCRefusalMessage is the FIXED status message a device receives when its
// master-token adoption is refused at the cap. Fixed, because the agent can
// only tell this refusal from a transient resource shortage by the message —
// the status code is shared with the per-node stream cap, which the agent is
// right to retry.
const GRPCRefusalMessage = "device cap reached"

// DeviceCapUnverifiedErrorCode is the code on the HTTP refusal of an
// admission whose cap could not be checked. A client must not render it as
// the limit being reached: nothing is known about the count, and the next
// step is to try again, not to remove a device.
const DeviceCapUnverifiedErrorCode = "device_cap_unverified"

// GRPCUnverifiedMessage is the FIXED status message a device receives when
// its admission is refused because the cap could not be checked. It is sent
// with the same code as GRPCRefusalMessage but must never equal it: an agent
// matches that message exactly to hold for its long retry, and this refusal
// clears as soon as the database answers again, so every other message under
// that code is retried through the agent's ordinary reconnect backoff.
const GRPCUnverifiedMessage = "device cap could not be checked; retry shortly"

// DeviceCapUnverifiedError refuses an admission because the device cap could
// not be checked: the admitted count could not be read, or the admission
// lock could not be taken (a statement or lock timeout included). The cap is
// never lifted by a database that cannot answer, so the admission is refused
// and nothing is written; the refusal is transient, which is what sets it
// apart from DeviceCapError. Raised only while a cap applies — an unlimited
// install has nothing to check.
//
// Cause is the database error, for the server's log. It is deliberately not
// in the message and not unwrapped: the message is shown to the caller, and
// an error from the driver can carry SQL text.
type DeviceCapUnverifiedError struct {
	Cap   int
	Cause error
}

func (e *DeviceCapUnverifiedError) Error() string {
	return "The device limit could not be checked, so nothing was added. Try again in a moment."
}

// The request is fine and so is the install's state as far as anyone knows:
// the server could not answer, briefly. 503 says so, and says retry.
func (e *DeviceCapUnverifiedError) HTTPStatus() int   { return http.StatusServiceUnavailable }
func (e *DeviceCapUnverifiedError) ErrorCode() string { return DeviceCapUnverifiedErrorCode }

// DeviceCapError is the admission refusal. It is raised at the one place that
// inserts a node row under the cap and surfaces through every path that
// admits a device — the operator's create, the browser flash, the SSH
// adoption and the master-token handshake — so it declares its own HTTP
// meaning once, here.
//
// Count is what the install held when it refused; Cap is what it may hold.
// Both are shown TO THE INSTANCE OPERATOR, because "26 of 25" tells an
// operator which device to remove or that the file they pasted has lapsed,
// where a bare refusal tells them nothing.
//
// Count is the INSTALL-WIDE admitted count, and on a shared install it is
// another tenant's business. So the refusal
// carries whose it is: AccountID and AccountDevices (that account's own
// admitted count) and InstanceOperator, filled in by the admission service,
// which knows the account. A tenant is shown its own count and the cap,
// never Count. The zero value is the TENANT view — an error nobody scoped
// discloses nothing it should not.
type DeviceCapError struct {
	Cap             int
	Count           int64
	SubscriptionURL string

	AccountID uuid.UUID
	// AccountDevices is AccountID's own admitted count; meaningful only when
	// AccountDevicesKnown (the count is a second read and may fail).
	AccountDevices      int64
	AccountDevicesKnown bool
	// InstanceOperator: AccountID operates this install. Fails closed —
	// false unless confirmed.
	InstanceOperator bool
}

// TenantRefusalMessage is what a caller who does not operate the install
// reads at the cap: the install's limit is not theirs to lift, and the
// install-wide numbers are not theirs to see.
const TenantRefusalMessage = "This install's device limit is reached; ask its operator."

func (e *DeviceCapError) Error() string {
	if !e.InstanceOperator {
		return TenantRefusalMessage
	}
	url := e.SubscriptionURL
	if url == "" {
		url = DefaultSubscriptionURL
	}
	return fmt.Sprintf("this install already manages %d devices and its entitlement covers %d. "+
		"Remove a device you no longer need, or extend the entitlement at %s. "+
		"Devices already admitted keep working", e.Count, e.Cap, url)
}

// The request is well-formed; the install's state refuses it, and the caller's
// next step is to remove a device or extend the entitlement — never to retry.
func (e *DeviceCapError) HTTPStatus() int   { return http.StatusConflict }
func (e *DeviceCapError) ErrorCode() string { return DeviceCapErrorCode }

// ErrorDetails is the structured half of the refusal, so a client can render
// its own copy around the numbers instead of parsing the sentence.
//
// The instance operator gets the full set; any other caller gets
// `account_devices` (its own admitted count, omitted when that read failed),
// `instance_operator: false` and `cap` — never the install-wide `count`.
// These field names are a contract with the SPA.
func (e *DeviceCapError) ErrorDetails() any {
	details := map[string]any{
		"cap":               e.Cap,
		"instance_operator": e.InstanceOperator,
	}
	if e.AccountDevicesKnown {
		details["account_devices"] = e.AccountDevices
	}
	if !e.InstanceOperator {
		return details
	}
	url := e.SubscriptionURL
	if url == "" {
		url = DefaultSubscriptionURL
	}
	details["count"] = e.Count
	details["subscription_url"] = url
	return details
}
