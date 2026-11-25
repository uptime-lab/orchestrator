package node

import "strings"

// BootPathRequestsSerial reports whether this node's EEPROM, as its agent read
// it back over the authenticated stream, asks the boot ROM to fetch exactly
// this serial's boot path (HTTP_PATH=http-boot/<serial>). False when the
// readback is unknown, and false when it names a serial that is not this
// node's own.
//
// The readback is reported by the node's agent, so it proves only what that
// agent chose to say. What keeps it from being a claim on someone else's
// device is that it counts only when the serial it names is this node's own
// bound serial or that serial's low word — the two paths an EEPROM baked for
// this device can ask for. A readback naming any other serial is a report the
// platform cannot square with the node's identity, and it never places the
// node among the candidates for that path.
func (n *Node) BootPathRequestsSerial(serial string) bool {
	want := NormalizeSerial(serial)
	got := bootPathSerial(n.BootHTTPPath())
	return want != "" && got != "" && got == want && n.isOwnBootSerial(got)
}

// bootPathNamesAnotherSerial reports whether the readback is KNOWN and does not
// place this node on this path: it names a different serial, or a serial that
// is not the node's own (see BootPathRequestsSerial). Either way that node's
// device is not to be taken as the one asking.
func (n *Node) bootPathNamesAnotherSerial(serial string) bool {
	got := bootPathSerial(n.BootHTTPPath())
	return got != "" && (got != NormalizeSerial(serial) || !n.isOwnBootSerial(got))
}

// isOwnBootSerial reports whether a normalized boot-path serial is this
// node's bound serial or the low word of it.
func (n *Node) isOwnBootSerial(pathSerial string) bool {
	own := NormalizeSerial(n.GetHardwareSerial())
	if own == "" {
		return false
	}
	return pathSerial == own || pathSerial == LowWord(own)
}

// bootPathSerial extracts the serial segment of an HTTP_PATH value
// ("http-boot/<serial>", with or without surrounding slashes), normalized.
func bootPathSerial(path string) string {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return ""
	}
	return NormalizeSerial(path[strings.LastIndex(path, "/")+1:])
}

// ResolveBootCandidate picks the node a boot request for `requested` belongs
// to, from every node whose stored serial matches it exactly or as its
// low-word suffix (candidates ordered newest first).
//
// A short serial is not unique to one account: it is all a WebUSB harvest
// sees, and any account can stamp a placeholder with it. So an exact stored
// match is never allowed to win on its own across accounts — that would hand
// one account's device the boot policy of another account's row. When the
// candidates span accounts, only nodes whose EEPROM readback names this very
// path stay in (or, when none does, only nodes whose readback is unknown);
// if what is left still spans accounts, or nothing is left, the request is
// ambiguous and resolves to nobody. Refusing is recoverable — the ROM falls
// through to its local boot order — where a wrong netboot decision is not.
//
// Across accounts a row that is not admitted (a placeholder, a node awaiting
// its first connect or approval) never takes the decision from another
// account's ADMITTED node whose serial matches: it may win only when no such
// node exists. Anyone holding a master token can create such a row, and an
// admitted node dropped because its readback names its full-serial path must
// not leave that row as the only candidate standing. An admitted winner does
// not take the decision from another account's admitted node whose readback
// is unknown either: a readback is evidence about the node that reported it,
// never against a node that reported nothing.
//
// Within one account the readback claim wins, then an exact stored match,
// then the newest row.
func ResolveBootCandidate(requested string, candidates []*Node) (resolved *Node, ambiguous bool) {
	if len(candidates) == 0 {
		return nil, false
	}
	if !spansAccounts(candidates) {
		return pickWithinAccount(requested, candidates), false
	}
	pool := filterNodes(candidates, func(n *Node) bool { return n.BootPathRequestsSerial(requested) })
	if len(pool) == 0 {
		pool = filterNodes(candidates, func(n *Node) bool { return !n.bootPathNamesAnotherSerial(requested) })
	}
	if len(pool) == 0 || spansAccounts(pool) {
		return nil, true
	}
	winner := pickWithinAccount(requested, pool)
	for _, n := range candidates {
		if n.AccountID == winner.AccountID || !n.IsAdmitted() {
			continue
		}
		// Another account's ADMITTED node. A winner that is not admitted
		// never takes the decision from it. An admitted winner does not take
		// it either while that node's readback is unknown: its device may be
		// the one asking, and the winner's readback — a report its own agent
		// made — is not evidence the platform can check against another
		// account's device. Refusing costs one netboot; deciding wrongly hands
		// one account's boot policy to another's device.
		if !winner.IsAdmitted() || !n.bootPathNamesAnotherSerial(requested) {
			return nil, true
		}
	}
	return winner, false
}

// pickWithinAccount orders one account's candidates: the readback claim, then
// an exact stored match, then the newest row.
func pickWithinAccount(requested string, pool []*Node) *Node {
	for _, n := range pool {
		if n.BootPathRequestsSerial(requested) {
			return n
		}
	}
	want := NormalizeSerial(requested)
	for _, n := range pool {
		if NormalizeSerial(n.GetHardwareSerial()) == want {
			return n
		}
	}
	return pool[0]
}

func spansAccounts(nodes []*Node) bool {
	for _, n := range nodes[1:] {
		if n.AccountID != nodes[0].AccountID {
			return true
		}
	}
	return false
}

func filterNodes(nodes []*Node, keep func(*Node) bool) []*Node {
	out := make([]*Node, 0, len(nodes))
	for _, n := range nodes {
		if keep(n) {
			out = append(out, n)
		}
	}
	return out
}
