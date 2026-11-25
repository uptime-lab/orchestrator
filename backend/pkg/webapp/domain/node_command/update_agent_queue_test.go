package node_command

import "testing"

// An agent update is a desired state, not a moment. It used to fail instantly
// against an offline node, so a node that was dark when the fleet was updated
// stayed on the old agent with nothing recording that it had missed one.
func TestUpdateAgentIsQueueable(t *testing.T) {
	if !IsQueueable(SlugUpdateAgent) {
		t.Fatal("update_agent is not queueable, so it is lost against an offline node")
	}
	// The whole point of queueing it is that the wait is bounded and a newer
	// update wins; both come from the shared queue machinery, so this only
	// pins that the slug did not acquire an override.
	if QueueTTL(SlugUpdateAgent) != defaultQueueTTL {
		t.Fatalf("QueueTTL(update_agent) = %v, want the shared default", QueueTTL(SlugUpdateAgent))
	}
}

// dpkg, a systemd unit to restart and a persistent root all live on the managed
// OS. Applying there could only report a success that evaporates on the next
// boot.
func TestUpdateAgentIsHeldWhileNetbooted(t *testing.T) {
	if !RequiresManagedOS(SlugUpdateAgent) {
		t.Fatal("update_agent would be delivered to the recovery initramfs, which has no dpkg")
	}
}

// install_agent is the recovery-side sibling and must NOT follow: it writes to
// a mounted volume from a netbooted agent, so queueing it would mean holding it
// for exactly the boot mode it needs.
func TestInstallAgentIsNotQueueable(t *testing.T) {
	if IsQueueable(SlugInstallAgent) {
		t.Fatal("install_agent is queueable; it runs FROM recovery, so the managed-OS hold would strand it")
	}
}
