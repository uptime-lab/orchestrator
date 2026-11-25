package authz

import "testing"

func TestRoleHierarchyGrants(t *testing.T) {
	cases := []struct {
		role   Role
		action Action
		want   bool
	}{
		// viewer: read only, no destructive, no secret values, no terminal.
		{RoleViewer, ActionNodeRead, true},
		{RoleViewer, ActionNodeRebootRecovery, false},
		{RoleViewer, ActionNodeReboot, false},
		{RoleViewer, ActionSecretReadValue, false},
		{RoleViewer, ActionNodeTerminal, false},
		// manager: reboot/status/playbook.run, but not destructive/secrets.
		{RoleManager, ActionNodeRead, true},
		{RoleManager, ActionNodeReboot, true},
		{RoleManager, ActionPlaybookRun, true},
		{RoleManager, ActionNodeShutdown, false},
		{RoleManager, ActionNodeInstallOS, false},
		{RoleManager, ActionNodeRebootRecovery, false},
		{RoleManager, ActionSecretManage, false},
		{RoleManager, ActionNodeTerminal, false},
		// admin: destructive + secrets + terminal, but not account-global.
		{RoleViewer, ActionNodeCreate, false},
		{RoleManager, ActionNodeCreate, false},
		{RoleAdmin, ActionNodeCreate, true},
		{RoleAdmin, ActionNodeRebootRecovery, true},
		{RoleAdmin, ActionNodeInstallOS, true},
		{RoleAdmin, ActionSecretManage, true},
		{RoleAdmin, ActionNodeTerminal, true},
		{RoleAdmin, ActionMasterTokenManage, false},
		{RoleAdmin, ActionImageManage, false},
		{RoleAdmin, ActionAccountDelete, false},
		{RoleAdmin, ActionOUManage, false},
		{RoleAdmin, ActionMemberManage, false},
		// super_admin: everything.
		{RoleSuperAdmin, ActionMasterTokenManage, true},
		{RoleSuperAdmin, ActionAccountDelete, true},
		{RoleSuperAdmin, ActionMemberManage, true},
		{RoleSuperAdmin, ActionNodeRebootRecovery, true},
	}
	for _, c := range cases {
		got := Grants(c.role, c.action, nil)
		if got != c.want {
			t.Errorf("Grants(%s, %s) = %v, want %v", c.role, c.action, got, c.want)
		}
	}
}

// image.read, sshkey.read, ou.read and favorite.set spent a long time declared,
// returned by /users/me/permissions and present in this matrix while no route
// checked them. Enforcing them was safe only because every one is granted to
// the LOWEST role and inherited upward — a viewer loses nothing. If a future
// matrix edit takes one away from viewer, it silently revokes a read that four
// handlers now require, so assert the premise rather than the enforcement.
func TestFormerlyUnenforcedReadsStayGrantedToViewer(t *testing.T) {
	for _, action := range []Action{ActionImageRead, ActionSSHKeyRead, ActionOURead, ActionFavoriteSet} {
		for _, role := range []Role{RoleViewer, RoleManager, RoleAdmin, RoleSuperAdmin} {
			if !Grants(role, action, nil) {
				t.Errorf("Grants(%s, %s) = false; enforcing this action on its routes would lock %s out",
					role, action, role)
			}
		}
	}
}

// None of the four is tunable, so no account-level role setting can narrow
// them either. That is what makes enforcing them a no-op today rather than a
// policy change an operator could trip over.
func TestFormerlyUnenforcedReadsAreNotTunable(t *testing.T) {
	for _, action := range []Action{ActionImageRead, ActionSSHKeyRead, ActionOURead, ActionFavoriteSet} {
		if IsTunable(action) {
			t.Errorf("%s became tunable; enforcing it on a route now depends on account settings", action)
		}
	}
}

func TestTunableOverride(t *testing.T) {
	// node.terminal is tunable: an account can grant it to manager.
	ov := NewOverride()
	ov.Set(RoleManager, ActionNodeTerminal, true)
	if !Grants(RoleManager, ActionNodeTerminal, ov) {
		t.Error("override should grant manager node.terminal")
	}
	// A non-tunable action cannot be overridden on.
	ov2 := NewOverride()
	ov2.Set(RoleManager, ActionNodeInstallOS, true) // ignored (not tunable)
	if Grants(RoleManager, ActionNodeInstallOS, ov2) {
		t.Error("non-tunable action must not be grantable via override")
	}
	// An account can also revoke a tunable default (turn admin terminal off).
	ov3 := NewOverride()
	ov3.Set(RoleAdmin, ActionNodeTerminal, false)
	if Grants(RoleAdmin, ActionNodeTerminal, ov3) {
		t.Error("override should revoke admin node.terminal")
	}
}

// audit.read shares account.export's posture: no role below super_admin holds
// it, and it is not tunable. The trail names every member's privileged actions
// across the whole account, so widening this is a product decision — this test
// makes that decision explicit rather than a matrix edit made in passing.
func TestAuditReadIsSuperAdminOnly(t *testing.T) {
	for _, role := range []Role{RoleViewer, RoleManager, RoleAdmin} {
		if Grants(role, ActionAuditRead, nil) {
			t.Errorf("%s must not hold audit.read by default", role)
		}
	}
	if !Grants(RoleSuperAdmin, ActionAuditRead, nil) {
		t.Error("super_admin must hold audit.read")
	}
	if IsTunable(ActionAuditRead) {
		t.Error("audit.read must not be tunable")
	}
}

// entitlement.manage shares the posture: the card names a customer and a cap
// the whole install shares, and applying a file changes what every tenant
// may admit. Widening it is a product decision.
func TestEntitlementManageIsSuperAdminOnly(t *testing.T) {
	for _, role := range []Role{RoleViewer, RoleManager, RoleAdmin} {
		if Grants(role, ActionEntitlementManage, nil) {
			t.Errorf("%s must not hold entitlement.manage by default", role)
		}
	}
	if !Grants(RoleSuperAdmin, ActionEntitlementManage, nil) {
		t.Error("super_admin must hold entitlement.manage")
	}
	if IsTunable(ActionEntitlementManage) {
		t.Error("entitlement.manage must not be tunable")
	}
	if IsAnyScopeRead(ActionEntitlementManage) {
		t.Error("entitlement.manage is a mutation right and must need an account-wide binding")
	}
}

func TestActionForCommandFailsClosed(t *testing.T) {
	if _, ok := ActionForCommand("totally_new_command"); ok {
		t.Error("unknown command slug must be reported as unmapped (fail closed)")
	}
	if a, ok := ActionForCommand("shutdown"); !ok || a != ActionNodeShutdown {
		t.Errorf("shutdown -> %s, %v", a, ok)
	}
}
