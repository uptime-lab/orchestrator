package organizational_unit

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// resolvePasswordAuthPolicy is the single implementation behind both
// ResolveEffectiveProvisioning and ResolvePasswordAuthPolicy. That matters more
// than it looks: these two booleans are the only inputs to the access-config
// hash besides the resolved entry set, so two implementations drifting by one
// level would make the platform's desired hash differ from the command
// builder's — and re-apply access to every converged node on every reconnect,
// forever.

func ptr(b bool) *bool { return &b }

func TestPasswordAuthPolicyPrecedence(t *testing.T) {
	cases := []struct {
		name        string
		nodeFlag    *bool
		ouFlag      *bool
		rootFlag    *bool
		accountFlag *bool
		wantAuth    bool
		wantManaged bool
	}{
		{
			name:        "nothing set anywhere: system default permits password login, unmanaged",
			wantAuth:    true,
			wantManaged: false,
		},
		{
			name:        "account default alone is still a managed policy",
			accountFlag: ptr(false),
			wantAuth:    false,
			wantManaged: true,
		},
		{
			name:     "root loses to the OU below it",
			ouFlag:   ptr(true),
			rootFlag: ptr(false),
			wantAuth: true, wantManaged: true,
		},
		{
			name:     "the node overrides every level above it",
			nodeFlag: ptr(false), ouFlag: ptr(true), rootFlag: ptr(true), accountFlag: ptr(true),
			wantAuth: false, wantManaged: true,
		},
		{
			// The one that is easy to get wrong: an explicit "on" is not the same
			// as an absent flag, even though the resolved value is identical.
			// Managed is what tells the agent to WRITE the sshd directive instead
			// of leaving the distro default alone.
			name:     "an explicit on is managed, unlike an absent flag",
			rootFlag: ptr(true),
			wantAuth: true, wantManaged: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := &node.Node{SSHPasswordAuth: c.nodeFlag}
			var ou, root *organizational_unit.OrganizationalUnit
			if c.ouFlag != nil {
				ou = &organizational_unit.OrganizationalUnit{SSHPasswordAuth: c.ouFlag}
			}
			if c.rootFlag != nil {
				root = &organizational_unit.OrganizationalUnit{SSHPasswordAuth: c.rootFlag}
			}
			var account *organizational_unit.AccountPolicy
			if c.accountFlag != nil {
				account = &organizational_unit.AccountPolicy{SSHPasswordAuth: c.accountFlag}
			}

			auth, managed := resolvePasswordAuthPolicy(n, ou, root, account)
			if auth != c.wantAuth || managed != c.wantManaged {
				t.Errorf("auth=%v managed=%v, want auth=%v managed=%v",
					auth, managed, c.wantAuth, c.wantManaged)
			}
		})
	}
}
