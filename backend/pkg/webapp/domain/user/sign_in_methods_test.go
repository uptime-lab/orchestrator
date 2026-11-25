package user

import "testing"

// The counted set is password + passkeys + live federated identities.
//
// The case this exists to pin is the LAST one: a TOTP enrolment must not make
// an account look reachable. TOTP is a second factor, presented after a first
// credential has already been accepted, and nothing starts a session from it —
// so a guard that counted it would let a passwordless, single-identity user
// remove their only way in. That the struct has no TOTP field is the fix; this
// test is what stops someone adding one back as a convenience.
func TestSignInMethodsCount(t *testing.T) {
	cases := []struct {
		name   string
		m      SignInMethods
		want   int
		isLast bool
	}{
		{"nothing at all", SignInMethods{}, 0, true},
		{"password only", SignInMethods{HasPassword: true}, 1, true},
		{"one identity only", SignInMethods{ExternalIdentities: 1}, 1, true},
		{"one passkey only", SignInMethods{Passkeys: 1}, 1, true},
		{"password and an identity", SignInMethods{HasPassword: true, ExternalIdentities: 1}, 2, false},
		{"two identities", SignInMethods{ExternalIdentities: 2}, 2, false},
		{"passkey and identity", SignInMethods{Passkeys: 1, ExternalIdentities: 1}, 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.Count(); got != tc.want {
				t.Errorf("Count() = %d, want %d", got, tc.want)
			}
			if got := tc.m.IsLast(); got != tc.isLast {
				t.Errorf("IsLast() = %v, want %v", got, tc.isLast)
			}
		})
	}
}
