package node_command

import "testing"

// A non-disruptive command must not move the node's status at all. The set is
// deliberately tiny and every addition is a claim about the DEVICE, so these
// tests pin the properties rather than the membership.

func TestIdentifyIsNonDisruptive(t *testing.T) {
	if !IsNonDisruptive(SlugIdentify) {
		t.Fatal("identify blinks an LED and changes nothing on the device — pinning the node " +
			"to maintenance for it flashes the whole list out of online and back for nothing")
	}
}

// The commands that take the machine out of service, and the ones that run code
// on it, must keep their maintenance pin: it is how a second operator learns
// not to start something else on the same node.
func TestConsequentialSlugsStayDisruptive(t *testing.T) {
	for _, slug := range []string{
		SlugReboot,
		SlugShutdown,
		SlugInstallOS,
		SlugEraseDrive,
		SlugRunScript,
		SlugApplyAccessConfig,
		SlugUpdateAgent,
	} {
		if IsNonDisruptive(slug) {
			t.Fatalf("%s must keep the maintenance pin — it changes the device", slug)
		}
	}
}

// A rebooting command pins the node to `restarting` at dispatch; a
// non-disruptive one pins nothing. A slug in both sets would be a direct
// contradiction, and the reboot pin is the one that must win — so the overlap
// has to be empty rather than ordered.
func TestNonDisruptiveNeverReboots(t *testing.T) {
	for slug := range nonDisruptiveSlugs {
		if CausesReboot(slug) {
			t.Fatalf("%s cannot be both non-disruptive and a reboot", slug)
		}
	}
}

// NonDisruptiveSlugs feeds a SQL NOT IN, so it must list exactly the map and
// stay ordered — an unstable list churns the generated query for no reason.
func TestNonDisruptiveSlugsListMatchesTheMap(t *testing.T) {
	listed := NonDisruptiveSlugs()
	if len(listed) != len(nonDisruptiveSlugs) {
		t.Fatalf("NonDisruptiveSlugs returned %d entries for a map of %d", len(listed), len(nonDisruptiveSlugs))
	}
	for i, slug := range listed {
		if !nonDisruptiveSlugs[slug] {
			t.Fatalf("NonDisruptiveSlugs returned %q, which is not in the map", slug)
		}
		if i > 0 && listed[i-1] >= slug {
			t.Fatalf("NonDisruptiveSlugs must be sorted: %q came before %q", listed[i-1], slug)
		}
	}
}
