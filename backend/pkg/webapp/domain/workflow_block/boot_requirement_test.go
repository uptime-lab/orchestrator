package workflow_block

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// deferred_to_managed_os is not a claim, it is the platform's own behaviour, so
// it must be DERIVED from the map that produces it rather than typed twice.
// Pinned in both directions: a slug the dispatcher holds must say so, and a
// block must not claim a deferral the dispatcher does not perform.
func TestDeferredBlocksMatchTheDispatchersOwnList(t *testing.T) {
	for _, def := range Registry() {
		deferred := def.Boot == BootDeferredToManagedOS
		held := def.Slug != "" && node_command.RequiresManagedOS(def.Slug)

		if held && !deferred {
			t.Errorf("block %q dispatches %q, which the platform HOLDS until the node "+
				"is on a managed OS, but the block does not declare it",
				def.Type, def.Slug)
		}
		if deferred && def.Slug != "" && !held {
			t.Errorf("block %q claims the platform defers it, but %q is not in "+
				"node_command.managedOSOnlySlugs", def.Type, def.Slug)
		}
	}
}

// The recovery requirement CANNOT be derived: the gate lives in the agent
// module, which the backend does not import. It is pinned by name instead, so
// adding a second recovery-only handler over there forces a decision here.
//
// The live gates are hardware.RootIsEphemeral() checks in the agent:
// capture_image_linux.go, patch_file_offline_linux.go and
// access_config_linux.go (the offline access apply — now the "Provision
// access" block, whose stored type keeps the legacy name).
// device_mounts_linux.go is a helper, not a block.
func TestRecoveryOnlyBlocksAreExactlyCaptureImage(t *testing.T) {
	want := map[string]bool{
		"command." + node_command.SlugCaptureImage:        true,
		"command." + node_command.SlugPatchFileOffline:    true,
		"command." + node_command.SlugProvisionConnection: true,
	}

	for _, def := range Registry() {
		if def.Boot != BootRecovery {
			continue
		}
		if !want[def.Type] {
			t.Errorf("block %q declares it needs recovery — if the agent really "+
				"refuses it on a live OS, add it to this test's list with the "+
				"handler that does so; if not, the declaration is wrong",
				def.Type)
		}
		delete(want, def.Type)
	}
	for missing := range want {
		t.Errorf("block %q no longer declares BootRecovery, but the agent still "+
			"refuses it on a live root", missing)
	}
}

// A note earns its place only where the requirement cannot say it all. Blocks
// whose real precondition is a PARAMETER (which disk) must not be given a boot
// requirement, or the editor would warn on the supported flow of installing
// onto a second disk from a running OS.
func TestTargetDeviceBlocksStayUnrestrictedAndExplainWhy(t *testing.T) {
	for _, slug := range []string{
		node_command.SlugInstallOS,
		node_command.SlugEraseDrive,
		node_command.SlugInstallAgent,
	} {
		def, ok := FindBySlug(slug)
		if !ok {
			t.Errorf("slug %q is no longer a block — update this test", slug)
			continue
		}
		if def.Boot != BootAny {
			t.Errorf("block %q declares %q; its precondition is the target disk, "+
				"not the boot mode, and installing onto a second disk from a "+
				"running OS is supported", def.Type, def.Boot)
		}
		if def.BootNote == "" {
			t.Errorf("block %q has no BootNote; without it the absence of a "+
				"requirement reads as 'anywhere is fine'", def.Type)
		}
	}
}
