package authz

import "testing"

// dispatchableSlugs are every node_command slug the node service can dispatch
// over HTTP. Kept as literals rather than imported from the node_command
// package: this file is the contract between the two, and importing the
// constants would make a slug that was renamed on one side silently agree with
// itself on the other.
//
// Add a slug here the moment the service learns to dispatch it. Nothing else
// couples the two lists, and the failure mode is quiet — dispatch answers a
// bare "unknown command" with no hint that a map is incomplete, which is what
// shipped for install_agent.
var dispatchableSlugs = []string{
	"reboot",
	"restart",
	"shutdown",
	"status_check",
	"install_os",
	"install_agent",
	"update_agent",
	"set_hostname",
	"provision_connection",
	"update_boot_order",
	"update_file",
	"patch_file",
	"read_file",
	"wait_for_file",
	"run_script",
	"enable_eeprom",
	"erase_drive",
	"capture_image",
	"install_packages",
	"manage_service",
	"download_file",
	"write_file",
	"identify",
}

// TestEveryDispatchableSlugIsMapped is the regression guard: an unmapped slug
// fails closed, so a command the UI offers is refused as "unknown command"
// rather than merely being ungated. Fail-closed is right; an incomplete map is
// not.
func TestEveryDispatchableSlugIsMapped(t *testing.T) {
	for _, slug := range dispatchableSlugs {
		if _, ok := ActionForCommand(slug); !ok {
			t.Errorf("slug %q is dispatchable but maps to no action, so dispatching it answers "+
				"\"unknown command\"; add it to commandSlugActions", slug)
		}
	}
}

// TestMappedSlugsMatchTheUIsGating pins the actions the node sidebar gates each
// control on. A mismatch means the UI shows a button the backend then refuses,
// or hides one the backend would have allowed.
func TestMappedSlugsMatchTheUIsGating(t *testing.T) {
	expected := map[string]Action{
		"install_agent":        ActionNodeUpdateAgent,
		"provision_connection": ActionNodeFileWrite,
		"update_boot_order":    ActionNodeEEPROMFlash,
		"erase_drive":          ActionNodeInstallOS,
	}
	for slug, want := range expected {
		got, ok := ActionForCommand(slug)
		if !ok {
			t.Errorf("slug %q is unmapped", slug)
			continue
		}
		if got != want {
			t.Errorf("slug %q maps to %s, expected %s", slug, got, want)
		}
	}
}

// TestWatchdogSlugFailsClosedOnGenericDispatch: arming a watchdog must go
// through PUT /nodes/:id/watchdog (domain validation + stored desired state +
// forced pre-arm verification). The generic POST /nodes/:id/command path
// derives its authz from this map and refuses unmapped slugs as "unknown
// command", so the slug's absence here IS the deny.
func TestWatchdogSlugFailsClosedOnGenericDispatch(t *testing.T) {
	if _, ok := ActionForCommand("apply_watchdog_config"); ok {
		t.Fatal("apply_watchdog_config is generically dispatchable; it must only be reachable " +
			"through the watchdog endpoint, which validates the config and forces pre-arm verification")
	}
}

// The recovery-mode access apply follows the same rule: its only entry is
// POST /nodes/:id/access/apply-offline, which checks the node is actually
// netbooted. Mapping the slug would let a generic dispatch skip that check.
func TestAccessOfflineSlugFailsClosedOnGenericDispatch(t *testing.T) {
	if _, ok := ActionForCommand("apply_access_config_offline"); ok {
		t.Fatal("apply_access_config_offline is generically dispatchable; it must only be reachable " +
			"through the access apply-offline endpoint, which enforces the netboot precondition")
	}
}

// TestReadsAreGatedLikeWrites: the agent opens whatever absolute path a caller
// names, so reading the adoption file yields a node-impersonation token. A read
// that is cheaper to obtain than the equivalent write would be a way around the
// write gate.
func TestReadsAreGatedLikeWrites(t *testing.T) {
	write, _ := ActionForCommand("update_file")
	for _, slug := range []string{"read_file", "wait_for_file"} {
		got, ok := ActionForCommand(slug)
		if !ok {
			t.Errorf("slug %q is unmapped", slug)
			continue
		}
		if got != write {
			t.Errorf("slug %q maps to %s but the equivalent write needs %s", slug, got, write)
		}
	}
}

func TestApplyWatchdogBlockIsPrivileged(t *testing.T) {
	// Arming a watchdog makes a device reboot ITSELF unattended, and a scheduled
	// playbook runs with no per-run authorization — only the author's rights,
	// re-checked at fire time. Anything less than the privileged tier would let
	// a non-admin author schedule a fleet-wide self-reboot.
	if !IsPrivilegedBlock("system.apply_watchdog") {
		t.Error("system.apply_watchdog must require playbook.author_privileged")
	}
}

// A playbook resolves {{secrets.*}} into the hostname param and the result lands
// in nodes.hostname, which node.read lists. Authoring and running a playbook are
// both MANAGER grants while reading a secret value is ADMIN, so leaving this
// block unprivileged is a read-out channel for every secret in scope — the same
// reason integration.http_request is on the list.
func TestSetHostnameBlockIsPrivileged(t *testing.T) {
	if !IsPrivilegedBlock("command.set_hostname") {
		t.Error("command.set_hostname must require playbook.author_privileged")
	}
	// The HTTP dispatch is deliberately NOT raised with it: there the caller
	// types the value, so nothing is resolved on their behalf.
	if got, _ := ActionForCommand("set_hostname"); got != ActionNodeSetHostname {
		t.Errorf("the set_hostname slug must stay on %s over HTTP, got %s", ActionNodeSetHostname, got)
	}
}

func TestApplyWatchdogConfigStaysUnmappedForGenericCommand(t *testing.T) {
	// The generic POST /nodes/:id/command must keep failing closed on this slug:
	// it would skip validation, desired state and the forced pre-arm checks.
	if _, ok := ActionForCommand("apply_watchdog_config"); ok {
		t.Error("apply_watchdog_config must not be dispatchable through the generic command endpoint")
	}
}

func TestApplyDeviceProfileStaysUnmappedForGenericCommand(t *testing.T) {
	// Same reasoning as the watchdog slug: the only legitimate payload is the
	// domain-rendered one (validated equipage -> marker block + typed setup
	// steps), dispatched via POST /nodes/:id/device-profile/apply. Generic
	// dispatch would hand the agent raw config.txt content.
	if _, ok := ActionForCommand("apply_device_profile"); ok {
		t.Error("apply_device_profile must not be dispatchable through the generic command endpoint")
	}
}

func TestIdentifyIsMappedAsStatusCheck(t *testing.T) {
	// Identify is harmless (nothing durable changes on the device) and the UI
	// gates its button on node.status_check; the generic path must agree with
	// the dedicated route.
	if got, ok := ActionForCommand("identify"); !ok || got != ActionNodeStatusCheck {
		t.Errorf("identify must map to %s, got %v (mapped=%v)", ActionNodeStatusCheck, got, ok)
	}
}
