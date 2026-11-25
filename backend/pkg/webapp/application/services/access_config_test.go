package services

import "testing"

// The two predicates below are the platform's half of a rule the AGENT also
// implements (wouldLockOutAllAccess). They must agree: if the platform is
// stricter it refuses configurations the device would happily apply, and if it
// is laxer it asks remote hardware to lock itself and calls the refusal a
// failure. These cases are written from the agent's rules, not from this code.

func TestGrantsALoginCountsKeysAndCertificates(t *testing.T) {
	keyed := AccessConfig{Users: []AccessConfigUser{{Username: "alice", AuthorizedKeys: []string{"ssh-ed25519 AAAA"}}}}
	if !keyed.GrantsALogin() {
		t.Error("a user with a key can log in")
	}

	// Principals only mean anything when something is trusted to sign for them.
	principalsOnly := AccessConfig{Users: []AccessConfigUser{{Username: "ops", Principals: []string{"ops"}}}}
	if principalsOnly.GrantsALogin() {
		t.Error("principals without a trusted CA grant nothing")
	}
	withCA := AccessConfig{
		Users: []AccessConfigUser{{Username: "ops", Principals: []string{"ops"}}},
		CAs:   []AccessConfigCA{{PublicKey: "ssh-ed25519 CA"}},
	}
	if !withCA.GrantsALogin() {
		t.Error("a trusted CA plus a principal is the certificate login path")
	}

	// Lifecycle is DERIVED from the key set, so a keyless user is a locked user.
	// Counting users rather than logins is the mistake this guards.
	keyless := AccessConfig{Users: []AccessConfigUser{{Username: "pi"}}}
	if keyless.GrantsALogin() {
		t.Error("a user with no keys is locked, not a login")
	}

	// An explicitly locked account cannot be rescued by a certificate either:
	// the expiry lock covers every method.
	lockedWithCert := AccessConfig{
		Users: []AccessConfigUser{{Username: "ops", Principals: []string{"ops"}, Locked: true}},
		CAs:   []AccessConfigCA{{PublicKey: "ssh-ed25519 CA"}},
	}
	if lockedWithCert.GrantsALogin() {
		t.Error("a locked account is locked against certificates too")
	}
}

func TestWouldLockOutOnlyFiresWhenPasswordsAreClosed(t *testing.T) {
	noLogins := []AccessConfigUser{{Username: "pi"}}

	// Passwords left alone: whatever works today keeps working, including
	// accounts the platform does not manage and cannot see. Refusing here would
	// block legitimate configurations on a guess.
	unmanaged := AccessConfig{Users: noLogins}
	if unmanaged.WouldLockOutAllAccess() {
		t.Error("a config that does not manage password auth must never be refused")
	}
	enabled := AccessConfig{Users: noLogins, ManagePasswordAuth: true, PasswordAuthEnabled: true}
	if enabled.WouldLockOutAllAccess() {
		t.Error("password auth left ON is still a way in")
	}

	// Closed passwords and nothing else: this is the lockout.
	closed := AccessConfig{Users: noLogins, ManagePasswordAuth: true}
	if !closed.WouldLockOutAllAccess() {
		t.Error("passwords off with no key and no certificate leaves no way in")
	}

	// Closed passwords but a key survives.
	closedWithKey := AccessConfig{
		Users:              []AccessConfigUser{{Username: "alice", AuthorizedKeys: []string{"ssh-ed25519 AAAA"}}},
		ManagePasswordAuth: true,
	}
	if closedWithKey.WouldLockOutAllAccess() {
		t.Error("a key is a working login with passwords off")
	}
}
