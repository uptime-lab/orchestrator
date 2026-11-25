package node

import (
	"testing"

	"github.com/google/uuid"
)

func bootCandidate(account uuid.UUID, serial, bootPath string) *Node {
	n := &Node{ID: uuid.New(), AccountID: account, Fingerprint: &HardwareFingerprint{HardwareSerial: serial}}
	if bootPath != "" {
		n.SystemInfo = &SystemInfo{Hardware: &HardwareInfo{Boot: &BootInfo{BootHTTPPath: bootPath}}}
	}
	return n
}

func admittedAs(n *Node, approval ApprovalStatus, status NodeStatus) *Node {
	n.ApprovalStatus, n.Status = approval, status
	return n
}

func TestBootPathRequestsSerial(t *testing.T) {
	cases := []struct {
		path, serial string
		want         bool
	}{
		{"http-boot/abcd0001", "abcd0001", true},
		{"/http-boot/ABCD0001/", "abcd0001", true},
		{"http-boot/10000000abcd0001", "abcd0001", false},
		{"http-boot/abcd0001", "10000000abcd0001", false},
		{"", "abcd0001", false},
		// A readback naming a serial that is neither the node's own nor its
		// low word is not this node's device asking, whatever path it names.
		{"http-boot/0abcd0001", "0abcd0001", false},
		{"http-boot/ffff0001", "ffff0001", false},
	}
	for _, c := range cases {
		n := bootCandidate(uuid.New(), "10000000abcd0001", c.path)
		if got := n.BootPathRequestsSerial(c.serial); got != c.want {
			t.Errorf("path %q serial %q: got %v, want %v", c.path, c.serial, got, c.want)
		}
	}
}

func TestResolveBootCandidate(t *testing.T) {
	owner := uuid.New()
	other := uuid.New()
	const short = "abcd0001"
	const full = "10000000abcd0001"

	t.Run("another account's exact short stamp does not take over the device that asks for it", func(t *testing.T) {
		// Newest first, as the repository orders them: the foreign placeholder
		// was created after the owner's device adopted.
		squat := bootCandidate(other, short, "")
		device := bootCandidate(owner, full, "http-boot/"+short)
		got, ambiguous := ResolveBootCandidate(short, []*Node{squat, device})
		if ambiguous || got != device {
			t.Fatalf("got %v (ambiguous=%v), want the owner's node whose EEPROM asks for this path", got, ambiguous)
		}
	})

	t.Run("across accounts with no readback naming the path, nobody is chosen", func(t *testing.T) {
		squat := bootCandidate(other, short, "")
		device := bootCandidate(owner, full, "")
		got, ambiguous := ResolveBootCandidate(short, []*Node{squat, device})
		if !ambiguous || got != nil {
			t.Fatalf("got %v (ambiguous=%v), want a refusal", got, ambiguous)
		}
	})

	t.Run("two accounts both claiming the path is a refusal", func(t *testing.T) {
		a := bootCandidate(other, short, "http-boot/"+short)
		b := bootCandidate(owner, full, "http-boot/"+short)
		if got, ambiguous := ResolveBootCandidate(short, []*Node{a, b}); !ambiguous || got != nil {
			t.Fatalf("got %v (ambiguous=%v), want a refusal", got, ambiguous)
		}
	})

	t.Run("a node whose EEPROM asks for another path is not a candidate across accounts", func(t *testing.T) {
		// The full-serial node's device was baked with its full serial, so it
		// never asks for the short path; the new placeholder is the asker.
		placeholder := bootCandidate(other, short, "")
		rebaked := bootCandidate(owner, full, "http-boot/"+full)
		got, ambiguous := ResolveBootCandidate(short, []*Node{placeholder, rebaked})
		if ambiguous || got != placeholder {
			t.Fatalf("got %v (ambiguous=%v), want the placeholder", got, ambiguous)
		}
	})

	t.Run("an admitted node never loses the short path to another account's placeholder", func(t *testing.T) {
		// The owner's readback names its full-serial path, which drops it from
		// the pool; the foreign row that never connected must not be what is
		// left standing.
		for name, placeholder := range map[string]*Node{
			"ready for adoption": admittedAs(bootCandidate(other, short, ""), ApprovalStatusApproved, StatusReadyForAdoption),
			"flashing":           admittedAs(bootCandidate(other, short, ""), ApprovalStatusApproved, StatusFlashing),
			"pending approval":   admittedAs(bootCandidate(other, short, ""), ApprovalStatusPendingApproval, StatusOffline),
		} {
			t.Run(name, func(t *testing.T) {
				owned := admittedAs(bootCandidate(owner, full, "http-boot/"+full), ApprovalStatusApproved, StatusOnline)
				got, ambiguous := ResolveBootCandidate(short, []*Node{placeholder, owned})
				if !ambiguous || got != nil {
					t.Fatalf("got %v (ambiguous=%v), want a refusal", got, ambiguous)
				}
			})
		}
	})

	t.Run("an admitted short-serial node in another account still boots by its path", func(t *testing.T) {
		// A WebUSB-flashed device legitimately asks for the 8-hex path; once
		// admitted, it is not displaced by the full-serial node elsewhere.
		flashed := admittedAs(bootCandidate(other, short, ""), ApprovalStatusApproved, StatusOnline)
		owned := admittedAs(bootCandidate(owner, full, "http-boot/"+full), ApprovalStatusApproved, StatusOnline)
		got, ambiguous := ResolveBootCandidate(short, []*Node{flashed, owned})
		if ambiguous || got != flashed {
			t.Fatalf("got %v (ambiguous=%v), want the admitted short-serial node", got, ambiguous)
		}
	})

	t.Run("within one account an unadmitted row still resolves", func(t *testing.T) {
		placeholder := admittedAs(bootCandidate(owner, short, ""), ApprovalStatusApproved, StatusReadyForAdoption)
		owned := admittedAs(bootCandidate(owner, full, "http-boot/"+full), ApprovalStatusApproved, StatusOnline)
		if got, ambiguous := ResolveBootCandidate(short, []*Node{placeholder, owned}); ambiguous || got != placeholder {
			t.Fatalf("got %v (ambiguous=%v), want the exact match", got, ambiguous)
		}
	})

	t.Run("one account keeps the exact-then-newest order", func(t *testing.T) {
		newer := bootCandidate(owner, full, "")
		exact := bootCandidate(owner, short, "")
		if got, _ := ResolveBootCandidate(short, []*Node{newer, exact}); got != exact {
			t.Fatalf("got %v, want the exact match", got)
		}
		a := bootCandidate(owner, full, "")
		b := bootCandidate(owner, full, "")
		if got, _ := ResolveBootCandidate(full, []*Node{a, b}); got != a {
			t.Fatalf("got %v, want the newest", got)
		}
	})

	// The readback is reported by the node's own agent. Across accounts it may
	// rule out the node that reported it, never a node that reported nothing:
	// another account's admitted node whose readback is unknown may be the
	// device asking, so a claim from elsewhere is a refusal, not a win.
	t.Run("a readback never takes another account's admitted node's boot decision", func(t *testing.T) {
		const colliding = "00000000abcd0001"
		silent := admittedAs(bootCandidate(owner, full, ""), ApprovalStatusApproved, StatusOnline)
		claimer := admittedAs(bootCandidate(other, colliding, "http-boot/"+short), ApprovalStatusApproved, StatusOnline)
		for _, order := range [][]*Node{{silent, claimer}, {claimer, silent}} {
			if got, ambiguous := ResolveBootCandidate(short, order); !ambiguous || got != nil {
				t.Fatalf("got %v (ambiguous=%v), want a refusal", got, ambiguous)
			}
		}
	})

	t.Run("a readback naming a serial that is not the node's own does not place it", func(t *testing.T) {
		// Seen through a request of an unusual width, where the candidate's
		// serial ends in the request without the request being its low word.
		const odd = "0abcd0001"
		owned := admittedAs(bootCandidate(owner, full, ""), ApprovalStatusApproved, StatusOnline)
		forger := admittedAs(bootCandidate(other, "ffffff0abcd0001", "http-boot/"+odd), ApprovalStatusApproved, StatusOnline)
		got, ambiguous := ResolveBootCandidate(odd, []*Node{forger, owned})
		if got == forger {
			t.Fatalf("the node whose readback names a serial not its own won the decision (ambiguous=%v)", ambiguous)
		}
	})

	t.Run("within one account a WebUSB-flashed device boots by its low-word path", func(t *testing.T) {
		newer := admittedAs(bootCandidate(owner, full, ""), ApprovalStatusApproved, StatusOnline)
		flashed := admittedAs(bootCandidate(owner, full, "http-boot/"+short), ApprovalStatusApproved, StatusOnline)
		if got, ambiguous := ResolveBootCandidate(short, []*Node{newer, flashed}); ambiguous || got != flashed {
			t.Fatalf("got %v (ambiguous=%v), want the node whose EEPROM asks for its own low word", got, ambiguous)
		}
	})

	t.Run("no candidates", func(t *testing.T) {
		if got, ambiguous := ResolveBootCandidate(short, nil); got != nil || ambiguous {
			t.Fatalf("got %v (ambiguous=%v), want nothing", got, ambiguous)
		}
	})
}
