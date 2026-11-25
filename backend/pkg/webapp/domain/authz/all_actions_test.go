package authz

import "testing"

// These tests walk AllActions() against the role matrix. This is the check
// that would have caught the four read actions (image.read, sshkey.read,
// ou.read, favorite.set) going unenforced: an action nobody can hold, or an
// action granted by the matrix but missing from the inventory, is invisible to
// the permission map and rots silently.

func allRoles() []Role {
	return []Role{RoleViewer, RoleManager, RoleAdmin, RoleSuperAdmin}
}

func inAllActions() map[Action]bool {
	set := make(map[Action]bool, len(AllActions()))
	for _, a := range AllActions() {
		set[a] = true
	}
	return set
}

// Every inventoried action must be grantable by SOME role. super_admin's
// blanket grant makes this the floor, not the whole check — the direction that
// actually catches drift is TestDefaultMatrixActionsAreInventoried below.
func TestEveryActionIsGrantableBySomeRole(t *testing.T) {
	for _, action := range AllActions() {
		if !GrantsAny(allRoles(), action, nil) {
			t.Errorf("action %q is grantable by no role at all", action)
		}
	}
}

func TestAllActionsHasNoDuplicates(t *testing.T) {
	seen := make(map[Action]bool)
	for _, a := range AllActions() {
		if seen[a] {
			t.Errorf("action %q is listed twice in AllActions()", a)
		}
		seen[a] = true
	}
}

// The reverse direction: every action the built-in matrix grants must appear
// in AllActions(). A matrix entry missing from the inventory is a permission a
// user holds but the permission map never reports — the exact shape of the
// unenforced-read-actions gap.
func TestDefaultMatrixActionsAreInventoried(t *testing.T) {
	inventory := inAllActions()
	for role, actions := range defaultMatrix {
		for action := range actions {
			if !inventory[action] {
				t.Errorf("matrix grants %q to %s but AllActions() does not list it", action, role)
			}
		}
	}
}

// Tunable actions are a subset of the inventory, and the stable UI list must
// agree with the tunability map exactly — a tunable action absent from the
// list cannot be reverted from the settings UI once overridden.
func TestTunableActionsAgreeWithTheInventoryAndTheUIList(t *testing.T) {
	inventory := inAllActions()
	for action := range tunableActions {
		if !inventory[action] {
			t.Errorf("tunable action %q is not in AllActions()", action)
		}
	}
	listed := make(map[Action]bool)
	for _, action := range TunableActionList() {
		if !tunableActions[action] {
			t.Errorf("TunableActionList() carries %q, which IsTunable denies", action)
		}
		if listed[action] {
			t.Errorf("TunableActionList() lists %q twice", action)
		}
		listed[action] = true
	}
	if len(listed) != len(tunableActions) {
		t.Errorf("TunableActionList() has %d actions, the tunability map %d — the two drifted",
			len(listed), len(tunableActions))
	}
}

// Every command-slug mapping must point at an inventoried action. A typo'd
// action here would still fail closed for everyone below super_admin, but it
// would do so invisibly: the permission map cannot report an action that does
// not exist.
func TestCommandSlugActionsAreInventoried(t *testing.T) {
	inventory := inAllActions()
	for slug, action := range commandSlugActions {
		if !inventory[action] {
			t.Errorf("command slug %q maps to %q, which AllActions() does not list", slug, action)
		}
	}
}
