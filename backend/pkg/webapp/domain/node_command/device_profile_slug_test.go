package node_command

import (
	"testing"

	"github.com/google/uuid"
)

// The apply_device_profile slug exists PRECISELY so a queued profile apply and
// a queued overclock apply (patch_file) can never retire each other: the
// supersede key is slug-scoped, so sharing patch_file would have made two
// different declarative targets one queue entry. These tests pin those
// properties.

func TestApplyDeviceProfileSupersedeKeyIsDistinctFromPatchFile(t *testing.T) {
	nodeID := uuid.New()
	if SupersedeKeyFor(SlugApplyDeviceProfile, nodeID) == SupersedeKeyFor(SlugPatchFile, nodeID) {
		t.Fatal("apply_device_profile must not share patch_file's supersede key — " +
			"a queued profile apply and a queued overclock apply would silently retire each other")
	}
}

func TestApplyDeviceProfileQueueSemantics(t *testing.T) {
	if !IsQueueable(SlugApplyDeviceProfile) {
		t.Fatal("apply_device_profile is declarative and must queue for offline nodes")
	}
	if !RequiresManagedOS(SlugApplyDeviceProfile) {
		t.Fatal("apply_device_profile must be held while the node runs the recovery bootstrap — " +
			"an apply there records a hash for a config.txt that evaporates on reboot")
	}
	if IsQueueable(SlugIdentify) {
		t.Fatal("identify is a point-in-time request and must fail fast for offline nodes")
	}
}

// Every managed-OS-only slug must be queueable, or the dispatch guard
// (Queueable && RequiresManagedOS) silently does nothing for it.
func TestManagedOSOnlyIsSubsetOfQueueable(t *testing.T) {
	for slug := range managedOSOnlySlugs {
		if !queueableSlugs[slug] {
			t.Fatalf("%s requires a managed OS but is not queueable — the hold guard never fires for it", slug)
		}
	}
}

func TestDeviceProfileSlugsHaveVersionFloors(t *testing.T) {
	for _, slug := range []string{SlugApplyDeviceProfile, SlugIdentify} {
		if _, ok := MinAgentVersion(slug); !ok {
			t.Fatalf("%s ships with the 0.6.0 agent and must carry a fail-closed floor", slug)
		}
	}
}
