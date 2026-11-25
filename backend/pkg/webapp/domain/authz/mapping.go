package authz

// commandSlugActions maps node_command runtime slugs to the action required to
// dispatch them over HTTP. Unmapped slugs fail closed (see ActionForCommand).
// The workflow engine reuses this for agent-block dispatch, but engine runs
// are authorized once at StartExecution, not per command.
var commandSlugActions = map[string]Action{
	"reboot": ActionNodeReboot,
	// "restart" is an accepted input spelling the dispatch normalizes to
	// "reboot" (node_command.ResolveCommandName). It is authorized here so the
	// alias reaches the switch that normalizes it. "status-check" was the same
	// idea and reached no switch arm at all — it authorized a name that could
	// only ever answer "unknown command" — so it is gone.
	"restart":       ActionNodeReboot,
	"status_check":  ActionNodeStatusCheck,
	"set_hostname":  ActionNodeSetHostname,
	"shutdown":      ActionNodeShutdown,
	"install_os":    ActionNodeInstallOS,
	"update_agent":  ActionNodeUpdateAgent,
	"patch_file":    ActionNodeFileWrite,
	"update_file":   ActionNodeFileWrite,
	"run_script":    ActionNodeFileWrite,
	"enable_eeprom": ActionNodeEEPROMFlash,
	"erase_drive":   ActionNodeInstallOS,
	// Mirrors of what the node sidebar already gates each control on, so a
	// dispatch the UI offers is not refused as unknown. Their absence made
	// "Install Agent" fail with a bare "unknown command" — fail-closed working
	// exactly as designed, against a map that had simply never been completed.
	"install_agent":        ActionNodeUpdateAgent,
	"provision_connection": ActionNodeFileWrite,
	"update_boot_order":    ActionNodeEEPROMFlash,
	// Reads rank with writes: the agent opens whatever absolute path the caller
	// names, so read_file over the adoption file yields a node-impersonation
	// token. Same reasoning as privilegedBlocks below.
	"read_file":     ActionNodeFileWrite,
	"wait_for_file": ActionNodeFileWrite,
	// Rewrites who may log into the node. Gated on the same action as editing
	// the access model itself: being able to push a config you cannot author
	// (or vice versa) would be a way around either check.
	"apply_access_config": ActionAccessManage,
	// Capturing a disk image reads the whole device (including secrets and the
	// adoption token) and produces a deployable image — same blast radius as
	// installing an OS.
	"capture_image": ActionNodeInstallOS,
	// Software provisioning runs privileged package/service/file operations —
	// arbitrary code execution on the node, ranked with file writes.
	"install_packages": ActionNodeFileWrite,
	"manage_service":   ActionNodeFileWrite,
	"download_file":    ActionNodeFileWrite,
	"write_file":       ActionNodeFileWrite,
	// The offline patch edits the same files as patch_file, just through a
	// mount, so it takes the same action. Unlike apply_access_config_offline —
	// whose payload is entirely platform-authored and therefore kept off the
	// generic route — this command's content IS the caller's, exactly as the
	// live patch_file's is; what the platform must check (the node is really
	// netbooted, a target disk is named) is checked in the payload builder for
	// both dispatch paths.
	"patch_file_offline": ActionNodeFileWrite,
	"delete_file":        ActionNodeFileWrite,
	// Blinking the locate LED changes nothing durable on the device; it ranks
	// with the status probes, and mapping it keeps the generic dispatch path
	// coherent with the dedicated POST /nodes/:id/identify route.
	"identify": ActionNodeStatusCheck,
	// apply_watchdog_config is deliberately NOT mapped. Arming a watchdog must
	// go through PUT /nodes/:id/watchdog, which validates the config against
	// the domain bounds, stores desired state, and dispatches with the forced
	// pre-arm verification. A generic POST /nodes/:id/command dispatch would
	// skip all three and could arm a device with an unvalidated payload, so the
	// slug fails closed here ("unknown command").
	//
	// apply_device_profile is NOT mapped for the same reason: the only
	// legitimate payload is the one the domain renders from a validated
	// equipage (marker block, content hash, typed setup steps), dispatched via
	// POST /nodes/:id/device-profile/apply. A generic dispatch would hand the
	// agent raw config.txt content and arbitrary setup kinds.
	//
	// apply_access_config_offline is NOT mapped either: its access payload is
	// entirely platform-authored (same resolution and lockout gates as
	// apply_access_config), and the only caller-supplied inputs — target
	// device and partition — must go through the dedicated
	// POST /nodes/:id/access/apply-offline route, which checks the node is
	// actually netbooted. A generic dispatch would skip that check and could
	// name an arbitrary payload.
	//
	// apply_utility_config is NOT mapped for the device-profile reason: the
	// only legitimate payload is the set the platform RESOLVED from the
	// account's assignments (registry-validated params, per-utility hashes),
	// dispatched via the utilities apply routes. A generic dispatch would hand
	// the agent arbitrary utility ids and params, bypassing the registry
	// bounds and recording hashes nothing computed.
	//
	// apply_led_config is NOT mapped for the same reason: the legitimate
	// payload carries the stealth/digital GPIO lines the platform resolved
	// from the node's device profile, and the dedicated PUT
	// /nodes/:id/led-config validates the sources against the board. A
	// generic dispatch would let a caller drive an arbitrary GPIO line high.
}

// MappedCommandSlugs returns the slugs commandSlugActions has an entry for.
//
// It exists for one guard: this map is keyed by hardcoded strings, and a typo
// or a rename leaves an entry that authorizes nothing while the REAL slug goes
// unmapped and fails closed — a 403 nobody can explain from either side. The
// check has to live where both this package and the slug constants are visible
// (domain/workflow_block), because importing the constants HERE is what the map
// deliberately avoids: the values are the contract, and a shared constant would
// make a rename agree with itself.
func MappedCommandSlugs() []string {
	out := make([]string, 0, len(commandSlugActions))
	for slug := range commandSlugActions {
		out = append(out, slug)
	}
	return out
}

// ActionForCommand returns the action required to dispatch a node command by
// slug, and whether the slug is known. Unknown slugs must be denied (fail
// closed) so a newly added agent command is not silently ungated.
func ActionForCommand(slug string) (Action, bool) {
	a, ok := commandSlugActions[slug]
	return a, ok
}

// privilegedBlocks are workflow block types whose presence in a template makes
// running or authoring it a privileged action (run_script/install_os/etc. can
// do anything the destructive node actions can, plus secret materialization).
var privilegedBlocks = map[string]bool{
	"command.run_script":  true,
	"command.install_os":  true,
	"command.update_file": true,
	"command.patch_file":  true,
	// The offline variant edits the same files through a mount, on a
	// filesystem the node is not running — if anything a stronger case.
	"command.patch_file_offline": true,
	// Reads are privileged for the same reason the writes above are: the agent
	// handler opens whatever absolute path the block names, with no
	// confinement. An unprivileged author could otherwise exfiltrate
	// /etc/shadow or the agent's adoption file — whose unique_token is a
	// node-impersonation credential — from every node in scope, while the
	// WRITE to those same files is admin-only.
	"command.read_file":     true,
	"command.wait_for_file": true,
	"command.shutdown":      true,
	"command.erase_drive":   true,
	"command.enable_eeprom": true,
	"command.update_agent":  true,
	// install_agent is update_agent's sibling and was missed. The engine
	// authorizes ONCE at StartExecution, so an unprivileged block dispatches its
	// slug with no per-command check — meaning a manager could author and run
	// "Install agent" while the same slug over POST /nodes/:id/command returns
	// 403 for them (it maps to ActionNodeUpdateAgent, which is admin-only and
	// not tunable). It also picks the agent VERSION with no anti-rollback floor
	// (that check lives on update_agent), and its payload builder copies every
	// caller param, so target_device reaches a mount with no validation.
	"command.install_agent":        true,
	"command.update_boot_order":    true,
	"command.provision_connection": true,
	"server.secret_upsert":         true,
	// attribute_upsert is secret_upsert with a reserved name: an attribute IS a
	// node-attached secret (attr:<nodeID>:<key>), written through the same
	// store. Same tier for the same reason.
	"server.attribute_upsert":  true,
	"integration.http_request": true, // can exfiltrate resolved {{secrets.*}}
	"command.capture_image":    true, // reads the whole disk incl. the adoption token
	"command.install_packages": true, // arbitrary code execution via package hooks
	"command.manage_service":   true, // writes+runs a systemd unit
	"command.download_file":    true, // fetches arbitrary bytes onto the node
	"command.write_file":       true, // writes any path with any mode/owner
	"server.set_boot_policy":   true, // flips OS vs netboot-recovery boot, like update_boot_order
	// Reboot to recovery takes the device down and hands it to the netboot
	// image. It is strictly more than set_boot_policy above (which only records
	// an intent) and strictly more than a plain reboot, so it cannot rank below
	// either; over HTTP the same operation lives on a route gated by the node
	// actions a manager does not hold.
	"server.reboot_to_recovery": true,
	// Arming a watchdog makes a device reboot ITSELF unattended, and a badly
	// chosen preset can do so in a loop across a whole fleet. The API path for
	// the same thing is gated on node.file_write; running it unattended from a
	// scheduled playbook deserves the privileged tier, not less.
	"system.apply_watchdog": true,
	// set_hostname is privileged for the same reason integration.http_request
	// is, and only inside a playbook: the engine resolves {{secrets.*}} into the
	// hostname param, and the value then becomes nodes.hostname, which every
	// node.read caller can list. So an author holding playbook.author +
	// node.set_hostname (both MANAGER grants) could read out any secret in
	// scope, while reading the same value directly needs secret.read_value
	// (admin). Over HTTP the slug stays on node.set_hostname: there the caller
	// types the value, so nothing is resolved for them.
	"command.set_hostname": true,
}

// IsPrivilegedBlock reports whether a block type requires privileged rights to
// run or author.
func IsPrivilegedBlock(blockType string) bool {
	return privilegedBlocks[blockType]
}

// PrivilegedBlockTypes returns the block types marked privileged.
//
// Same guard as MappedCommandSlugs, and the same failure: an entry naming a
// block type the registry does not have protects nothing, silently, while the
// real block defaults to UNPRIVILEGED. Checked from domain/workflow_block,
// which can see both lists.
func PrivilegedBlockTypes() []string {
	out := make([]string, 0, len(privilegedBlocks))
	for blockType := range privilegedBlocks {
		out = append(out, blockType)
	}
	return out
}
