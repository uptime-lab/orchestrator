package node_command

import "testing"

// apply_access_config_offline is the managed-OS hold's mirror image: it is
// meaningful ONLY in recovery. These invariants are what the dispatch path
// assumes; breaking any of them changes who can write a node's disk and when.
func TestApplyAccessConfigOfflineSlugInvariants(t *testing.T) {
	if IsQueueable(SlugApplyAccessConfigOffline) {
		t.Error("apply_access_config_offline must NOT be queueable: 'write this disk now, while netbooted' applied hours later against whatever the node boots next is a different command")
	}
	if RequiresManagedOS(SlugApplyAccessConfigOffline) {
		t.Error("apply_access_config_offline must not be managed-OS-only — it is the recovery-side path; the hold guard would park it forever")
	}
	if !IsDeviceExclusive(SlugApplyAccessConfigOffline) {
		t.Error("apply_access_config_offline mounts and writes the target disk; it must be device-exclusive so it cannot race install_os/erase_drive/capture_image")
	}
	if v, ok := MinAgentVersion(SlugApplyAccessConfigOffline); !ok || v == "" {
		t.Error("apply_access_config_offline needs a version floor: an older agent decodes neither the oneof arm nor the mount machinery")
	}
	if CausesReboot(SlugApplyAccessConfigOffline) {
		t.Error("apply_access_config_offline does not reboot the device")
	}
}
