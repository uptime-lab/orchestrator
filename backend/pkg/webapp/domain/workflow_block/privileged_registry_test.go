package workflow_block

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
)

// authz.privilegedBlocks is keyed by hardcoded strings ("command.install_agent")
// while this registry composes its types as "command." + node_command.SlugX.
// The two have NO compile-time link, and an unlisted block defaults to
// UNPRIVILEGED — it fails OPEN, so a block added to the registry and not to
// the privileged list would silently skip the privilege gate.
//
// The test lives here rather than in domain/authz because only this package can
// see both: authz does not import the registry, so a test there would have to
// mirror the block list by hand — which is the very duplication that caused the
// bug.
//
// The rule: a manager may author and run non-privileged playbooks. So if a
// block's slug maps to an action a manager does NOT hold, the playbook path
// would grant what POST /nodes/:id/command refuses, and the block must be
// privileged.
func TestEveryAgentBlockAManagerCannotDispatchIsPrivileged(t *testing.T) {
	noOverrides := authz.NewOverride()

	for _, def := range Registry() {
		if def.Executor != ExecutorAgent || def.Slug == "" {
			continue
		}
		action, known := authz.ActionForCommand(def.Slug)
		if !known {
			// Deliberately unmapped slugs (apply_watchdog_config) fail closed at
			// the generic endpoint and reach the device only through their own
			// validated route. They still need the privileged tier here, because
			// a playbook bypasses that route.
			if !authz.IsPrivilegedBlock(def.Type) {
				t.Errorf("block %q has slug %q, which is deliberately unmapped in "+
					"commandSlugActions, so a playbook is the ONLY way to dispatch it — "+
					"it must be in privilegedBlocks", def.Type, def.Slug)
			}
			continue
		}
		if authz.Grants(authz.RoleManager, action, noOverrides) {
			continue
		}
		if !authz.IsPrivilegedBlock(def.Type) {
			t.Errorf("block %q (slug %q) maps to %q, which RoleManager does NOT hold, "+
				"but the block is not privileged — a manager could author and run it while "+
				"the same slug over POST /nodes/:id/command returns 403 for them",
				def.Type, def.Slug, action)
		}
	}
}

// install_agent is the entry this test was written for; update_agent is its
// sibling that was already correct. Pinned by name so a refactor cannot drop
// either silently.
func TestAgentInstallAndUpdateBlocksArePrivileged(t *testing.T) {
	for _, blockType := range []string{"command.install_agent", "command.update_agent"} {
		if _, ok := FindByType(blockType); !ok {
			t.Fatalf("%s is no longer a registered block — update this test", blockType)
		}
		if !authz.IsPrivilegedBlock(blockType) {
			t.Errorf("%s must be privileged: it chooses the agent version (install_agent has no "+
				"anti-rollback floor) and the target device", blockType)
		}
	}
}

// deliberatelyUnprivileged names every NON-agent block that is allowed to be
// absent from authz.privilegedBlocks, with the reason. Adding a block here is a
// decision; forgetting to is what the test below prevents.
//
// The reasoning that puts a block on this list: it must touch no device, resolve
// no secret, and change nothing durable — it may only decide when and whether
// the blocks around it run.
var deliberatelyUnprivileged = map[string]string{
	TypeStart:     "manual entry point; carries no params and dispatches nothing",
	TypeSchedule:  "cron entry point; the privileged tier is decided by the blocks it leads to, and the scheduler re-checks the author at fire time",
	TypeCondition: "compares two resolved values and picks a port; touches no device",
	TypeWait:      "sleeps the run; touches no device",
	TypeJoin:      "merges branches; touches no device",
	TypeWaitForNode: "polls the node row and waits; reads status and boot mode, " +
		"dispatches nothing, changes nothing, and resolves no secret",
}

// The agent-block test above SKIPS every non-agent block, so the four
// privileged SERVER blocks sit on authz.privilegedBlocks by hand with nothing
// asserting it. That means the next server.* or integration.* block — the class
// with access to the secret resolver — is unprivileged BY DEFAULT, which is the
// same fail-open shape that missed read_file, wait_for_file and install_agent.
//
// So: every non-agent block must be either privileged or listed above.
func TestEveryNonAgentBlockIsPrivilegedOrDeliberatelyNot(t *testing.T) {
	for _, def := range Registry() {
		if def.Executor == ExecutorAgent {
			continue
		}
		privileged := authz.IsPrivilegedBlock(def.Type)
		reason, excused := deliberatelyUnprivileged[def.Type]

		if privileged && excused {
			t.Errorf("block %q is BOTH privileged and listed as deliberately unprivileged (%q) — "+
				"decide which", def.Type, reason)
		}
		if !privileged && !excused {
			t.Errorf("block %q (%s) is neither in authz.privilegedBlocks nor in "+
				"deliberatelyUnprivileged. A server block runs inside the backend with the "+
				"secret resolver in reach, and an unlisted block fails OPEN: a manager could "+
				"author and run it. Add it to one list or the other, with a reason.",
				def.Type, def.Executor)
		}
	}

	// The excuse list must not outlive the blocks it excuses, or it silently
	// becomes a place where a deleted block's name grants nothing to nobody
	// while looking like coverage.
	for blockType := range deliberatelyUnprivileged {
		if _, ok := FindByType(blockType); !ok {
			t.Errorf("deliberatelyUnprivileged names %q, which is no longer a registered block", blockType)
		}
	}
}

// Pinned by name: these four are privileged for reasons that are easy to
// re-argue away — none of them touches a device directly.
func TestTheServerBlocksThatReachSecretsArePrivileged(t *testing.T) {
	for blockType, why := range map[string]string{
		TypeSecretUpsert:    "writes the account's secret store",
		TypeAttributeUpsert: "writes the account's secret store (an attribute IS a node-attached secret)",
		TypeHTTPRequest:     "can exfiltrate resolved {{secrets.*}} to any URL",
		TypeSetBootPolicy:   "flips OS vs netboot-recovery boot, like update_boot_order",
		TypeApplyWatchdog:   "makes a device reboot itself unattended",
	} {
		if !authz.IsPrivilegedBlock(blockType) {
			t.Errorf("%s must stay privileged: %s", blockType, why)
		}
	}
}
