package organizational_unit

import "testing"

// The password-auth policy defaults to ON. Anything else would mean an upgrade
// silently closes the password path — which, on a fleet whose keys are not yet
// verified, is the difference between a routine deploy and a lockout.
func TestSSHPasswordAuthDefaultsToEnabled(t *testing.T) {
	eff := ResolveEffectivePolicy(NodePolicyInputs{}, nil, nil, nil)
	if !eff.SSHPasswordAuth.Value {
		t.Fatal("password auth must default to enabled when no level sets it")
	}
	if eff.SSHPasswordAuth.Source != SourceDefault {
		t.Fatalf("source = %q, want %q", eff.SSHPasswordAuth.Source, SourceDefault)
	}
}

func TestSSHPasswordAuthResolutionPrecedence(t *testing.T) {
	on, off := true, false

	cases := []struct {
		name       string
		node       *bool
		ou         *bool
		root       *bool
		account    *bool
		wantValue  bool
		wantSource PolicySource
	}{
		{"node override wins", &off, &on, &on, &on, false, SourceNode},
		{"ou beats root and account", nil, &off, &on, &on, false, SourceOU},
		{"root beats account", nil, nil, &off, &on, false, SourceRootOU},
		{"account is the last explicit level", nil, nil, nil, &off, false, SourceAccount},
		{"nothing set falls back to the default", nil, nil, nil, nil, true, SourceDefault},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ou, root *OrganizationalUnit
			var account *AccountPolicy
			if tc.ou != nil {
				ou = &OrganizationalUnit{SSHPasswordAuth: tc.ou}
			}
			if tc.root != nil {
				root = &OrganizationalUnit{SSHPasswordAuth: tc.root}
			}
			if tc.account != nil {
				account = &AccountPolicy{SSHPasswordAuth: tc.account}
			}

			eff := ResolveEffectivePolicy(NodePolicyInputs{SSHPasswordAuth: tc.node}, ou, root, account)
			if eff.SSHPasswordAuth.Value != tc.wantValue {
				t.Fatalf("value = %v, want %v", eff.SSHPasswordAuth.Value, tc.wantValue)
			}
			if eff.SSHPasswordAuth.Source != tc.wantSource {
				t.Fatalf("source = %q, want %q", eff.SSHPasswordAuth.Source, tc.wantSource)
			}
		})
	}
}

// "inherit" must round-trip to nil. The storage layer writes nil as NULL, and
// two explicit column lists have to name the column for that to persist — this
// is the domain half of that contract.
func TestPolicyFlagInheritIsNil(t *testing.T) {
	if PolicyInherit.ToBool() != nil {
		t.Fatal("inherit must resolve to nil so the column is written as NULL")
	}
	if got := PolicyFlagFromBool(nil); got != PolicyInherit {
		t.Fatalf("nil should read back as inherit, got %q", got)
	}
}
