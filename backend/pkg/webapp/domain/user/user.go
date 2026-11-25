package user

import (
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
)

// NormalizeEmail case-folds and trims an email so lookup, uniqueness and
// OIDC/invite link-by-email all agree on one canonical form. Every user
// creation path and GetByEmail run through this.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// MaxNameLength bounds the display name. It matches the max=256 the register
// command carries as a binding tag; the tag is not what enforces it.
const MaxNameLength = 256

// IsValidEmail reports whether an address is parseable and carries a domain
// part. It deliberately returns a bool rather than an error so this package
// stays free of the application error set, matching how the rest of the domain
// exposes its rules.
//
// The bar is "can this address ever receive the verification mail", not RFC
// exhaustiveness: an email that cannot, is a login identifier its owner can
// never prove or recover, which is the failure this guards.
func IsValidEmail(email string) bool {
	normalized := NormalizeEmail(email)
	if normalized == "" {
		return false
	}
	addr, err := mail.ParseAddress(normalized)
	if err != nil {
		return false
	}
	// ParseAddress accepts display-name forms ("Name <a@b>") and bare local
	// parts in some builds; require the parsed address to be exactly what was
	// supplied and to carry a domain.
	if addr.Address != normalized {
		return false
	}
	at := strings.LastIndex(normalized, "@")
	return at > 0 && strings.Contains(normalized[at+1:], ".")
}

// BetaNoticeVersion is the revision stamped on a stored acceptance of the
// operator's notice or terms of use. The server, not the client, owns it: a
// client-supplied version would let a browser claim it had accepted a text it
// never saw. An acceptance stamped below it does not count.
//
// The identifier, like the user columns and the audit action string, keeps the
// name it was introduced under because stored rows carry it. It stays at 2 so
// that every acceptance already stored keeps counting.
const BetaNoticeVersion = 2

// MinPasswordLength is the floor for every path that sets a password:
// registration, invite acceptance, password reset and change-password. It lives
// here rather than in each service because the rule was previously duplicated —
// password_reset carried a local const whose comment claimed it "matches the
// registration and change-password rules", a coupling asserted in prose and
// expressed nowhere in code. Meanwhile invite acceptance enforced no length at
// all, so the only floor on that path was minLength={8} in the browser.
const MinPasswordLength = 8

// MaxPasswordLength is the ceiling for the same paths, in BYTES of the UTF-8
// string, not characters: bcrypt refuses to hash more than 72 bytes, so a
// longer password (an 80-character passphrase, or about 30 characters of
// non-ASCII text) cannot be stored at all. Every path checks it before it
// changes any state — a password reset in particular before it spends its
// single-use link.
const MaxPasswordLength = 72

// User is a global identity. Account association is carried by account
// memberships (domain/account_member), not by the user itself: one person can
// be a member of several accounts under a single identity.
type User struct {
	ID             uuid.UUID
	Name           string
	Email          string
	HashedPassword string
	// PasswordChangedAt invalidates refresh tokens issued before it (nil =
	// never changed, no check).
	PasswordChangedAt *time.Time
	// EmailVerifiedAt records proven mailbox ownership (nil = unverified).
	// Set at creation for OIDC-provisioned users (the IdP asserts a verified
	// email) and when an authenticated user explicitly links a matching
	// verified identity. Silent OIDC→local auto-linking requires it — a
	// password account whose email was never verified can be a squatter's
	// (a pre-hijack of an unverified address).
	EmailVerifiedAt *time.Time
	// BetaNoticeAckAt / BetaNoticeAckVersion record that this person accepted
	// the operator's notice or terms of use, when, and which revision. Both are
	// zero for anyone who never accepted anything — every user of an install
	// that has no terms.
	BetaNoticeAckAt      *time.Time
	BetaNoticeAckVersion int
	// CredentialGeneration counts the credential transitions this identity has
	// been through — every password write and every sign-in email change
	// advances it, atomically, in the column write itself. It rides on every
	// token as the `cg` claim and the row-lock predicates (Rotate,
	// CreateWhileSessionLive, requireLiveSession) compare the two, so a token
	// minted before an email change is refused exactly as one minted before a
	// password change. No service writes the column; only
	// the repository's transition methods do, so a stale whole-row copy can
	// never move it backwards.
	CredentialGeneration int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// AcknowledgeBetaNotice stamps acceptance of the current revision.
// Re-accepting is allowed and simply refreshes the timestamp: the audit trail
// is the append-only event log, not this single field.
func (u *User) AcknowledgeBetaNotice() {
	now := time.Now()
	u.BetaNoticeAckAt = &now
	u.BetaNoticeAckVersion = BetaNoticeVersion
	u.UpdatedAt = now
}

// HasAcknowledgedBetaNotice reports whether this user accepted a revision at
// least as new as the one in force. A stamp against an older revision does not
// count — that is the whole point of versioning it.
func (u *User) HasAcknowledgedBetaNotice() bool {
	return u.BetaNoticeAckAt != nil && u.BetaNoticeAckVersion >= BetaNoticeVersion
}

// MarkEmailVerified stamps proven mailbox ownership (idempotent).
func (u *User) MarkEmailVerified() {
	if u.EmailVerifiedAt == nil {
		now := time.Now()
		u.EmailVerifiedAt = &now
		u.UpdatedAt = now
	}
}

// NewUser creates a new User instance with default values. Email is
// normalized so every identity is stored in canonical form. A user may have an
// empty HashedPassword when their only credential is an external identity
// (OIDC).
func NewUser(email, name, hashedPassword string) *User {
	now := time.Now()
	return &User{
		ID:             uuid.New(),
		Name:           name,
		Email:          NormalizeEmail(email),
		HashedPassword: hashedPassword,
		// Generations start at 1, so that 0 on a token means exactly one
		// thing — minted before the claim existed — and never "a user whose
		// credentials have not changed yet" — with 0 as the start, the
		// email-change predicate would protect nobody until their first
		// transition.
		CredentialGeneration: 1,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
}
