package organizational_unit

import (
	"testing"

	"github.com/google/uuid"
)

func boolPtr(b bool) *bool { return &b }

func TestNewOrganizationalUnit(t *testing.T) {
	acc := uuid.New()
	if _, err := NewOrganizationalUnit(acc, "  ", nil); err != ErrNameRequired {
		t.Fatalf("expected ErrNameRequired for blank name, got %v", err)
	}
	ou, err := NewOrganizationalUnit(acc, "  Lab  ", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ou.Name != "Lab" {
		t.Errorf("name should be trimmed, got %q", ou.Name)
	}
	if !ou.IsRoot() {
		t.Error("nil parent should be a root unit")
	}
}

func TestPolicyFlagRoundTrip(t *testing.T) {
	cases := []struct {
		flag PolicyFlag
		want *bool
	}{
		{PolicyInherit, nil},
		{PolicyOn, boolPtr(true)},
		{PolicyOff, boolPtr(false)},
		{PolicyFlag("garbage"), nil},
	}
	for _, c := range cases {
		got := c.flag.ToBool()
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s.ToBool() = %v, want %v", c.flag, got, c.want)
		}
	}
	if PolicyFlagFromBool(nil) != PolicyInherit {
		t.Error("nil -> inherit")
	}
	if PolicyFlagFromBool(boolPtr(true)) != PolicyOn {
		t.Error("true -> on")
	}
	if PolicyFlagFromBool(boolPtr(false)) != PolicyOff {
		t.Error("false -> off")
	}
}

func TestResolveEffectivePolicy(t *testing.T) {
	acc := uuid.New()
	root := &OrganizationalUnit{ID: uuid.New(), AccountID: acc, NetworkFirst: boolPtr(true), BootToOS: boolPtr(false)}
	rootID := root.ID
	ou := &OrganizationalUnit{ID: uuid.New(), AccountID: acc, ParentID: &rootID, NetworkFirst: nil, BootToOS: boolPtr(true)}

	// Node override wins over everything.
	got := ResolveEffectivePolicy(NodePolicyInputs{NetworkFirst: boolPtr(false)}, ou, root, nil)
	if got.NetworkFirst.Value != false || got.NetworkFirst.Source != SourceNode {
		t.Errorf("node override: got %+v", got.NetworkFirst)
	}
	// BootToOS: node nil -> OU (true, source ou).
	if got.BootToOS.Value != true || got.BootToOS.Source != SourceOU {
		t.Errorf("boot_to_os from OU: got %+v", got.BootToOS)
	}

	// NetworkFirst: node nil, OU nil -> root (true, source root_ou).
	got = ResolveEffectivePolicy(NodePolicyInputs{}, ou, root, nil)
	if got.NetworkFirst.Value != true || got.NetworkFirst.Source != SourceRootOU {
		t.Errorf("network_first from root: got %+v", got.NetworkFirst)
	}

	// Unassigned node (no OU, no root) -> system default false, source default.
	got = ResolveEffectivePolicy(NodePolicyInputs{}, nil, nil, nil)
	if got.NetworkFirst.Value != true || got.NetworkFirst.Source != SourceDefault {
		t.Errorf("default network_first: got %+v, want true from SourceDefault", got.NetworkFirst)
	}
	if got.BootToOS.Source != SourceDefault {
		t.Errorf("default boot_to_os: got %+v", got.BootToOS)
	}

	// Node directly in a root OU (ou = root, root = nil).
	got = ResolveEffectivePolicy(NodePolicyInputs{}, root, nil, nil)
	if got.NetworkFirst.Value != true || got.NetworkFirst.Source != SourceOU {
		t.Errorf("network_first from direct root-OU: got %+v", got.NetworkFirst)
	}
}

func TestResolveEffectivePolicyAccountFallback(t *testing.T) {
	acc := uuid.New()
	user := "orchestrator"
	account := &AccountPolicy{
		AccountID:       acc,
		NetworkFirst:    boolPtr(true),
		DefaultUsername: &user,
	}

	// Unassigned node: the account policy supplies values the OU chain cannot.
	got := ResolveEffectivePolicy(NodePolicyInputs{}, nil, nil, account)
	if got.NetworkFirst.Value != true || got.NetworkFirst.Source != SourceAccount {
		t.Errorf("network_first from account: got %+v", got.NetworkFirst)
	}
	if !got.DefaultUsername.Present || got.DefaultUsername.Value != user || got.DefaultUsername.Source != SourceAccount {
		t.Errorf("default_username from account: got %+v", got.DefaultUsername)
	}
	// A field the account policy leaves unset still falls to the default.
	if got.BootToOS.Source != SourceDefault {
		t.Errorf("boot_to_os default: got %+v", got.BootToOS)
	}

	// Any OU-chain value still wins over the account policy.
	root := &OrganizationalUnit{ID: uuid.New(), AccountID: acc, NetworkFirst: boolPtr(false)}
	got = ResolveEffectivePolicy(NodePolicyInputs{}, root, nil, account)
	if got.NetworkFirst.Value != false || got.NetworkFirst.Source != SourceOU {
		t.Errorf("ou beats account: got %+v", got.NetworkFirst)
	}
}
