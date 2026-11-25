package app_errors

import (
	"errors"
	"fmt"
)

// Application-level errors
var (
	ErrUserNotFound          = errors.New("user not found")
	ErrAccountNotFound       = errors.New("account not found")
	ErrUnauthorized          = errors.New("unauthorized")
	ErrFailedToUpdateUser    = errors.New("failed to update user")
	ErrFailedToUpdateAccount = errors.New("failed to update account")
	ErrInvalidCredentials    = errors.New("invalid credentials")
	ErrEmailAlreadyExists    = errors.New("email already exists")
	ErrAccountNameRequired   = errors.New("account name is required")
	ErrUserEmailRequired     = errors.New("user email is required")
	ErrUserPasswordRequired  = errors.New("user password is required")
	// ErrUserPasswordTooShort is distinct from ErrUserPasswordRequired on
	// purpose: registration returned "password is required" for a password that
	// was present but short, so the sign-up form — which allowed 6 — told the
	// user to supply the password they were looking at.
	//
	// The message states the rule because the sign-up handler passes
	// err.Error() straight to the client. The number is duplicated from
	// user.MinPasswordLength rather than interpolated, to keep this package a
	// leaf that pulls in no domain; TestPasswordTooShortMessageMatchesTheFloor
	// fails if the two ever drift.
	ErrUserPasswordTooShort = errors.New("user password must be at least 8 characters")
	// ErrUserEmailInvalid and ErrUserNameTooLong are returned by profile
	// updates. The messages are shown to the caller: the endpoint is reachable
	// by API clients only (the SPA has no profile-edit call), so the response
	// body is the entire diagnostic the caller gets.
	ErrUserEmailInvalid = errors.New("user email is not a valid address")
	ErrUserNameTooLong  = errors.New("user name must be at most 256 characters")
	// ErrUserNameRequired: a supplied name that is empty or only whitespace.
	// Omitting the field is how a caller leaves the name alone.
	ErrUserNameRequired = errors.New("user name must not be empty")

	// Node errors
	ErrNodeNotFound            = newNotFound("node not found", "node_not_found")
	ErrNodeAlreadyExists       = errors.New("node already exists")
	ErrNodeUnauthorized        = errors.New("unauthorized to access this node")
	ErrNodeAdoptionJobNotFound = errors.New("node adoption job not found")
	// ErrNodeOnlineDeleteRequiresForce: the node's device is ONLINE, and deleting
	// it there is what strands it — the row goes, the agent keeps its adoption
	// file and retries into nothing forever. The caller must either take the
	// device offline first or pass force, which also asks the agent to release
	// its identity on the way out.
	ErrNodeOnlineDeleteRequiresForce = errors.New("this node's device is online: deleting it now would leave it trying to reconnect forever. Shut it down first, or force the delete so the device is told to remove its platform credentials.")
	// ErrNodeAlreadyRestored: the deleted-node record has been used already. The
	// device is either back under that id or the id was re-created some other
	// way; either way the live row wins over a second restore.
	ErrNodeAlreadyRestored = errors.New("this node was already restored")

	// Node command errors
	ErrNodeCommandNotFound = newNotFound("node command not found", "node_command_not_found")

	// Node adoption errors
	ErrNodeNotReadyForAdoption = errors.New("node is not ready for adoption")
	ErrNodeAlreadyAdopted      = errors.New("node has already been adopted")
	ErrInvalidAdoptionData     = errors.New("invalid adoption data")
	// ErrAdoptionCodeInvalid covers unknown, expired AND already-used codes
	// identically — the exchange endpoint is unauthenticated and must not be
	// an oracle for which codes exist.
	ErrAdoptionCodeInvalid = errors.New("adoption code is invalid or expired")
	// ErrHardwareSerialConflict: the device's full hardware serial is already
	// bound to a node in ANOTHER account. Outward-facing messages must stay
	// generic (no tenant identity); details go to server logs only.
	ErrHardwareSerialConflict = errors.New("hardware serial is registered to another account")
	// ErrHardwareSerialAmbiguous: a serial-only lookup matched nodes the
	// platform cannot choose between across accounts, so it resolved to
	// nobody. It IS ErrNodeNotFound (errors.Is holds for both), and it is
	// distinct so a caller that punishes unknown serials does not punish a
	// device whose serial is known, only shared.
	ErrHardwareSerialAmbiguous = fmt.Errorf("%w: hardware serial matches nodes in more than one account", ErrNodeNotFound)

	// Image errors
	ErrImageNotFound              = errors.New("image not found")
	ErrInvalidImageFile           = errors.New("invalid image file")
	ErrImageCompressionInProgress = errors.New("image compression is already in progress")
	// ErrImageLimitReached: the account is at its image count cap
	// (image.max_per_account); delete an image before adding another.
	ErrImageLimitReached = errors.New("image limit reached for this account")

	// Per-tenant quota errors. Separate sentinels rather than
	// one shared ErrQuotaExceeded, because the caller's next step differs: a
	// node cap needs a device removed, a seat cap needs a member removed, a
	// secret cap needs cleanup — and an HTTP layer that cannot tell them apart
	// cannot say which.
	ErrNodeLimitReached   = errors.New("node limit reached for this account")
	ErrMemberLimitReached = errors.New("member limit reached for this account")
	ErrSecretLimitReached = errors.New("secret limit reached for this account")

	// SSH key errors
	ErrSSHKeyNotFound      = newNotFound("ssh key not found", "ssh_key_not_found")
	ErrInvalidSSHKeyFormat = errors.New("invalid ssh key format")
	ErrNoDefaultSSHKey     = errors.New("no default ssh key found")

	// Master token errors
	ErrMasterTokenNotFound    = errors.New("master token not found")
	ErrMasterTokenExpired     = errors.New("master token expired")
	ErrMasterTokenInactive    = errors.New("master token is inactive")
	ErrMasterTokenInvalid     = errors.New("invalid master token")
	ErrNodePendingApproval    = errors.New("node is pending approval")
	ErrNodeRejected           = errors.New("node has been rejected")
	ErrNodeAlreadyApproved    = errors.New("node is already approved")
	ErrNodeNotPendingApproval = errors.New("node is not pending approval")

	// Organizational unit errors
	ErrOrganizationalUnitNotFound = newNotFound("organizational unit not found", "organizational_unit_not_found")
	ErrOrganizationalUnitHasNodes = errors.New("organizational unit still has nodes assigned")
	ErrOrganizationalUnitHasChild = errors.New("organizational unit still has nested units")
	// ErrOrganizationalUnitHasPlaybooks refuses deletion while playbooks are
	// still scoped to the unit — a scoped (and possibly cron-driven) playbook
	// whose OU vanished would keep firing against an empty node set.
	ErrOrganizationalUnitHasPlaybooks = errors.New("organizational unit still has playbooks scoped to it")
	// ErrOrganizationalUnitHasUtilities refuses deletion while utility
	// assignments target the unit — a dangling reference would resolve as
	// unassigned (tolerated) but the assignment's config would silently stop
	// reaching nodes with nothing in the product able to say why.
	ErrOrganizationalUnitHasUtilities = errors.New("organizational unit still has utilities assigned to it")

	// Secret errors
	ErrSecretNotFound    = errors.New("secret not found")
	ErrInvalidSecretKind = errors.New("invalid secret kind")
	ErrSecretNameTaken   = errors.New("a secret with this name already exists")
	// ErrSecretValueUnreadable: the stored value decrypts with none of the
	// configured keys. Reads report the row (secret.Secret.ValueUnreadable) so it
	// can be found and re-set; this error is what the paths that would DELIVER or
	// REWRITE that value answer instead, so a resolution never substitutes an
	// empty credential and an update never encrypts "" over bytes a restored key
	// could still recover.
	ErrSecretValueUnreadable = errors.New("the stored value of this secret cannot be decrypted with any configured key; re-set it")
	// ErrSecretOutsideRunNode: a playbook step may write only a secret that is
	// new or delivered to the run's node alone. Anything wider is managed
	// account-wide, and a step that could rewrite it by name would let whoever
	// may run a playbook on one node change what other nodes receive.
	ErrSecretOutsideRunNode = errors.New("a playbook can only save a secret that is new or delivered to the run's node alone")

	// Secret attachment errors
	ErrSecretAttachmentNotFound = errors.New("secret attachment not found")
	ErrInvalidAttachmentTarget  = errors.New("secret attachment must target exactly one of an organizational unit or a node")

	// Access entry errors
	ErrAccessEntryNotFound      = errors.New("access entry not found")
	ErrAccessEntryUsernameTaken = errors.New("a user with this name already exists in one of the target units")

	// Secret tag (color/lifecycle) errors
	ErrSecretTagNotFound  = errors.New("secret tag not found")
	ErrSecretTagNameTaken = errors.New("a tag with this name already exists")

	// Workflow errors
	ErrWorkflowTemplateInvalid   = errors.New("invalid playbook")
	ErrWorkflowTemplateNotActive = errors.New("playbook is not active")
	// ErrWorkflowTemplateStale: the caller sent expected_updated_at and the
	// stored playbook has moved on since they read it. Maps to HTTP 409, so the
	// client can re-read and re-apply instead of silently reverting whatever the
	// other editor saved — on a cron playbook containing erase_drive, the field
	// most likely to be reverted is its OU scoping.
	ErrWorkflowTemplateStale = errors.New("playbook was changed by someone else; reload it and re-apply your changes")
	// ErrTooManyActiveExecutions: the account hit its in-flight executions cap
	// (workflow.max_active_executions_per_account); finish or wait out running
	// executions before starting more. Maps to HTTP 429.
	ErrTooManyActiveExecutions = errors.New("too many active workflow executions for this account")
	// ErrTooManyRunningNodes: the account's pending+running EXECUTION NODES
	// would exceed its cap. Distinct from the one above because it is a
	// different resource and a different remedy: the execution cap is about how
	// many runs are in flight, this one about how much of the worker pool they
	// occupy — a single run over a large fleet can hit it with the execution
	// count at one.
	ErrTooManyRunningNodes = errors.New("this run would exceed the account's limit on nodes running workflow steps at once")
	// ErrNoValidTargetNodes: none of the requested node ids belong to the
	// account; the created execution is failed immediately. Maps to HTTP 400.
	ErrNoValidTargetNodes = errors.New("no valid target nodes for this execution")
	// ErrExecutionAlreadyCompleted: a full-row execution write arrived for a run
	// that already carries completed_at. The write is refused rather than
	// applied, because it would come from a copy read before the run settled and
	// would reopen a finished run — clearing the field the active-executions cap
	// counts and the retention sweep keys on. Never surfaced to an HTTP caller:
	// the writers are the engine and the settlers, for which this is a no-op.
	ErrExecutionAlreadyCompleted = errors.New("workflow execution has already completed")

	// Membership / RBAC errors
	ErrAccountMemberNotFound = errors.New("account member not found")
	ErrAccountInviteNotFound = errors.New("account invite not found")
	ErrMembershipDisabled    = errors.New("membership is disabled")
	ErrNotAMember            = errors.New("user is not a member of this account")
	ErrAlreadyMember         = errors.New("user is already a member of this account")
	ErrLastSuperAdmin        = errors.New("account must keep at least one active super admin")
	ErrForbidden             = errors.New("you do not have permission to perform this action")
	ErrInvalidRole           = errors.New("invalid role")
	ErrInvalidMemberStatus   = errors.New("invalid member status: use active or disabled")
	ErrInviteEmailMismatch   = errors.New("invite was issued for a different email")
	ErrInviteNotValid        = errors.New("invite is expired, revoked or already accepted")
	// ErrMemberBindingsStale: the writer sent the binding set it read and the
	// member's bindings have changed since. Re-read and re-apply; writing the
	// old set would restore roles another administrator just removed.
	ErrMemberBindingsStale = errors.New("this member's roles changed since they were read; reload and try again")
	// ErrInviteRateLimited: the account has sent too many invitations, or is
	// re-sending one too soon. Distinct from ErrInviteNotValid because it is
	// about the SENDER's rate, not the invitation's state — mapped to 429 so a
	// caller can tell "try later" from "this invitation is dead".
	ErrInviteRateLimited        = errors.New("invitation rate limit reached")
	ErrExternalIdentityNotFound = errors.New("external identity not found")
	ErrExternalIdentityConflict = errors.New("this external identity is already linked to another user")
	// ErrExternalIdentityLastCredential: unlinking it would leave the account
	// with no way to sign in at all. Worded for the user, like
	// ErrPasskeyLastCredential, because it is shown to them verbatim.
	//
	// The counted set is password + passkeys + other LIVE identities. TOTP is
	// deliberately excluded — see domain/user.SignInMethods, which is the one
	// place that rule is written down.
	ErrExternalIdentityLastCredential = errors.New("this is the only way you can sign in; add a password or another sign-in method first")
	ErrOIDCNotConfigured              = errors.New("OIDC login is not configured")
	ErrOIDCVerificationFailed         = errors.New("OIDC verification failed")
	ErrOIDCEmailNotVerified           = errors.New("OIDC email is not verified")
	ErrOIDCNoAccount                  = errors.New("no account for this identity — ask an administrator to invite you")
	// ErrOIDCAccountLinkRequired: a local account with this email exists and
	// the identity is not linked to it — the account's password must prove it
	// before the identity is attached; a sign-in never links on its own.
	ErrOIDCAccountLinkRequired = errors.New("an account with this email exists — sign in with your password and link the identity explicitly")

	// Email verification errors
	// ErrEmailVerificationInvalid covers unknown, expired AND already-used
	// tokens identically — the confirm endpoint is public and must not reveal
	// which tokens exist.
	ErrEmailVerificationInvalid = errors.New("verification link is invalid or expired")
	// ErrEmailVerificationStale: the link was minted for an address the user has
	// since changed, so confirming it would verify the wrong mailbox.
	ErrEmailVerificationStale = errors.New("verification link was issued for a different email address")
	// ErrEmailAlreadyVerified is a no-op condition, reported so the UI can say
	// "already done" instead of showing a failure.
	ErrEmailAlreadyVerified = errors.New("email is already verified")
	// ErrEmailSendThrottled: too many verification emails requested recently.
	ErrEmailSendThrottled = errors.New("verification email was requested too recently")
	// ErrEmailNotConfigured: no mail transport is configured on this
	// deployment, so verification links cannot be delivered.
	ErrEmailNotConfigured = errors.New("email delivery is not configured")

	// Password reset errors
	// ErrPasswordResetInvalid covers unknown, expired AND already-used tokens
	// identically — the reset endpoint is public and must not reveal which
	// tokens exist.
	ErrPasswordResetInvalid = errors.New("password reset link is invalid or expired")
	// ErrPasswordResetStale: the link was minted for an address the user has
	// since changed, so the mailbox that received it no longer owns the account.
	ErrPasswordResetStale = errors.New("password reset link was issued for a different email address")

	// Generic not found error
	ErrNotFound = errors.New("resource not found")

	// Authentication errors
	ErrUserAlreadyExists = errors.New("user already exists")
	ErrTokenExpired      = errors.New("token expired")
	ErrTokenInvalid      = errors.New("invalid token")
	// ErrRefreshTokenReused signals that an already-redeemed refresh token was
	// presented again. A legitimate client never does this, so it means two
	// parties hold the same token: the whole family is revoked.
	ErrRefreshTokenReused = errors.New("refresh token was already used")

	// Two-factor authentication.
	// ErrOIDCLinkNeedsSecondFactor: the link-on-conflict flow proved a password
	// for a user who enrolled a second factor. Shown verbatim: it names the
	// way forward and reveals nothing the caller did not already know (they
	// hold the password).
	ErrOIDCLinkNeedsSecondFactor = errors.New("this account uses two-step verification: sign in with your password and second step first, then link the identity from account settings")
	// ErrTwoFactorNotEnrolled: the user has no second factor at all.
	ErrTwoFactorNotEnrolled = errors.New("two-factor authentication is not enrolled")
	// ErrTwoFactorAlreadyEnabled: refuse to silently replace a CONFIRMED
	// factor — re-enrolling must go through an explicit disable, or a stolen
	// session could quietly swap the factor for its own.
	ErrTwoFactorAlreadyEnabled = errors.New("two-factor authentication is already enabled")
	// ErrTwoFactorEnrollmentChanged: the pending secret this code was
	// verified against is no longer the stored one — a newer enrolment
	// replaced it. Distinct from AlreadyEnabled so the user is told to scan
	// the NEW code rather than to disable a factor they do not have.
	ErrTwoFactorEnrollmentChanged = errors.New("two-factor enrollment was restarted; scan the new code")
	// ErrTwoFactorInvalidCode covers a wrong TOTP code and a wrong or spent
	// recovery code alike.
	ErrTwoFactorInvalidCode = errors.New("invalid two-factor code")
	// ErrTwoFactorCodeAlreadyUsed: the code was correct but has already been
	// spent inside its own time window (RFC 6238 anti-replay).
	//
	// Distinct from ErrTwoFactorInvalidCode on purpose, and deliberately NOT
	// counted as a failed attempt: a user who double-submits a form would
	// otherwise burn their lockout budget on their own valid code. The message
	// says to wait for the next code, because that is the only thing that works.
	ErrTwoFactorCodeAlreadyUsed = errors.New("this two-factor code was already used; wait for the next one")
	// ErrTwoFactorRequired: the account requires a second factor and this user
	// has not enrolled one, so only the enrollment endpoints are available.
	ErrTwoFactorRequired = errors.New("this account requires two-factor authentication")
	// ErrTwoFactorTooManyAttempts: too many wrong codes for this user recently.
	// Counted per user rather than per IP — an attacker holding the password
	// can rotate addresses freely.
	ErrTwoFactorTooManyAttempts = errors.New("too many incorrect codes; try again later")

	// Passkeys (WebAuthn).
	ErrPasskeyNotFound = errors.New("passkey not found")
	// ErrPasskeyCeremonyExpired: the begin/finish pair straddled too long, or
	// the ticket was already spent (challenges are single-use).
	ErrPasskeyCeremonyExpired = errors.New("this passkey request expired; start again")
	// ErrPasskeyRegistrationFailed / ErrPasskeyLoginFailed are deliberately
	// coarse: the underlying library errors describe exactly which check failed,
	// which is useful in logs and dangerous in a response.
	ErrPasskeyRegistrationFailed = errors.New("passkey registration failed")
	ErrPasskeyLoginFailed        = errors.New("passkey login failed")
	// ErrPasskeyCloneWarning: the signature counter did not advance, which the
	// specification defines as evidence of a cloned authenticator.
	ErrPasskeyCloneWarning = errors.New("this passkey appears to have been cloned")
	// ErrPasskeyLastCredential: removing it would leave a passwordless account
	// with no way to sign in at all.
	ErrPasskeyLastCredential = errors.New("this is the only way you can sign in; add a password or another passkey first")

	// Re-authentication (step-up before a sensitive change: sign-in methods,
	// members and roles, the server entitlement).
	// ErrReauthRequired: the operation needs a fresh proof of identity that this
	// request did not carry. The caller should prove and retry — it is NOT a
	// statement about the session, which stays perfectly valid. The sentence is
	// shown on every guarded route, so it names no particular operation.
	ErrReauthRequired = errors.New("confirm your identity to continue")
	// ErrReauthInvalid covers a wrong password and a wrong code alike: which one
	// was wrong is not something an attacker should learn from the response.
	ErrReauthInvalid = errors.New("that did not match; check your password or code")
	// ErrReauthTooManyAttempts: too many failed confirmations for this user
	// recently. Counted per user, not per IP — whoever holds a stolen session
	// can rotate addresses freely.
	ErrReauthTooManyAttempts = errors.New("too many failed attempts; try again later")
	// ErrReauthUnavailable: the user has no credential to re-authenticate with.
	ErrReauthUnavailable = errors.New("no credential available to confirm your identity")

	ErrInviteCodeRequired = errors.New("invite code required")
	ErrInvalidInviteCode  = errors.New("invalid invite code")
	// ErrRegistrationClosed: the instance is at its self-registration ceiling
	// (server.max_accounts). Deliberately says nothing about a limit or a count
	// — an unauthenticated caller learning "23 of 30 taken" learns how many
	// tenants live here, which is nobody's business, and the honest operator
	// remedy (raise the limit) is not something the caller can act on anyway.
	ErrRegistrationClosed = errors.New("registration is currently closed")
	// ErrEmailVerificationRequired: the account was created through open
	// registration and its owner has not confirmed their address yet, so the
	// session may confirm it and nothing else.
	ErrEmailVerificationRequired = errors.New("email_verification_required")
)

// MemberBindingsStaleCode is the machine-readable code of ErrMemberBindingsStale.
const MemberBindingsStaleCode = "member_bindings_stale"
