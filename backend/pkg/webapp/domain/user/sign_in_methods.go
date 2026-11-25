package user

// SignInMethods counts the ways a user can actually START a session, which is
// the only question a "would this remove your last credential?" guard is
// allowed to ask.
//
// It exists because two guards asked it differently and both were wrong, in
// opposite directions. `passkey.Service.Delete` counted password + passkeys and
// ignored external identities, so a passwordless user with a linked Google
// account was refused a passkey deletion they could safely make. The unlink
// guard, as first specified, was to count "the same way reauth.AvailableMethods
// does" — which would have been far worse, because that function counts TOTP.
//
// **TOTP IS NOT A SIGN-IN METHOD.** It is a second factor presented AFTER a
// first credential has already been accepted; nothing anywhere starts a session
// from a TOTP code alone. Counting it would let a passwordless user with one
// OIDC identity, no passkeys and TOTP enrolled remove their only way in — and
// `password_reset` deliberately refuses passwordless accounts, so there would be
// no self-service way back. That user shape is reachable.
//
// Deliberately a plain value type with no repository access: the counts come
// from wherever the caller already has them, and the rule stays one testable
// expression rather than three services agreeing by coincidence.
type SignInMethods struct {
	// HasPassword is true when a usable password hash is stored.
	HasPassword bool
	// Passkeys is the number of registered WebAuthn credentials.
	Passkeys int
	// ExternalIdentities is the number of LIVE federated links. A revoked link
	// must not be counted — it cannot sign in, so counting it would let a user
	// unlink their way to a locked account one identity at a time.
	ExternalIdentities int
}

// Count reports how many distinct credentials could begin a session.
func (m SignInMethods) Count() int {
	n := m.Passkeys + m.ExternalIdentities
	if m.HasPassword {
		n++
	}
	return n
}

// IsLast reports whether the user holds at most one sign-in method, i.e.
// removing one more would leave the account unreachable.
//
// Zero counts as "last" on purpose: a user who already has no way in must not
// have a removal succeed against them and read as if it were fine.
func (m SignInMethods) IsLast() bool {
	return m.Count() <= 1
}
