package sql

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/google/uuid"
)

// TestAccessEntryModel_RoundTrip guards the fromDomain/toDomain mapping. The
// password is intentionally NOT mapped here (the repository encrypts it) and
// neither are OU memberships (they live in the access_entry_ous join table and
// are hydrated separately), so a round trip must preserve everything else
// including principals and the node/account-wide target.
func TestAccessEntryModel_RoundTrip(t *testing.T) {
	node := uuid.New()
	in := &access_entry.AccessEntry{
		ID:             uuid.New(),
		AccountID:      uuid.New(),
		Type:           access_entry.TypeCA,
		NodeID:         &node,
		Username:       "",
		Sudo:           true,
		AdoptUnmanaged: true,
		Principals:     []string{"deploy", "ci"},
	}
	var m AccessEntryModel
	m.fromDomain(in)
	out := m.toDomain()

	if out.Type != in.Type || out.Sudo != in.Sudo || out.AdoptUnmanaged != in.AdoptUnmanaged {
		t.Fatalf("scalar fields not preserved: %+v", out)
	}
	if out.NodeID == nil || *out.NodeID != node || len(out.OUIDs) != 0 {
		t.Fatalf("target not preserved: %+v", out)
	}
	if len(out.Principals) != 2 || out.Principals[0] != "deploy" {
		t.Fatalf("principals not preserved: %+v", out.Principals)
	}
	if out.Password != nil {
		t.Fatalf("model mapping must not carry a password, got %v", out.Password)
	}
}

// TestOUModel_SSHPasswordAuthTristate guards the tri-state column mapping: nil
// (inherit), true (on) and false (off) must all round-trip distinctly. A
// dropped mapping would silently collapse "off" into "inherit".
func TestOUModel_SSHPasswordAuthTristate(t *testing.T) {
	on, off := true, false
	for _, want := range []*bool{nil, &on, &off} {
		d := &organizational_unit.OrganizationalUnit{
			ID:              uuid.New(),
			AccountID:       uuid.New(),
			Name:            "unit",
			SSHPasswordAuth: want,
		}
		var m OrganizationalUnitModel
		m.FromDomain(d)
		got := m.ToDomain().SSHPasswordAuth
		switch {
		case want == nil && got != nil:
			t.Fatalf("inherit collapsed to concrete value %v", *got)
		case want != nil && got == nil:
			t.Fatalf("concrete value %v collapsed to inherit", *want)
		case want != nil && got != nil && *want != *got:
			t.Fatalf("value flipped: want %v got %v", *want, *got)
		}
	}
}
