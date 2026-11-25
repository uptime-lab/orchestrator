package node

import (
	"strconv"
	"strings"
)

// AgentVersionLess compares dotted AGENT version strings segment by segment,
// numerically where both segments are numbers ("0.2.9" < "0.2.10") and
// lexicographically otherwise. A version that is a prefix of a longer one
// sorts lower.
//
// This is the one implementation. The CDN listing sorts with it
// (infrastructure/apis/storage/agentpkg.Less delegates here) and the fleet
// attention check asks it whether a node is behind — two answers that must
// agree, because the picker and the "update available" row describe the same
// release. It lived only in the storage layer until the second caller needed
// it; a copy of a version sort is exactly what drifts silently and is noticed
// as "the picker lists 0.4.9 above 0.4.10" long after the fact.
//
// It is for agent versions. The CDN-path comparison in the storage clients and
// the command floor comparison (node_command.CompareSemver, which parses
// exactly three numeric components) are different problems with different
// answers — do not reuse this for either.
func AgentVersionLess(a, b string) bool {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] == bs[i] {
			continue
		}
		an, aErr := strconv.Atoi(as[i])
		bn, bErr := strconv.Atoi(bs[i])
		if aErr == nil && bErr == nil {
			return an < bn
		}
		return as[i] < bs[i]
	}
	return len(as) < len(bs)
}

// BootloaderMatchesBase reports whether a node's reported EEPROM firmware is
// the same build as the signer's base.
//
// It is a prefix match on the hash token, not string equality, and both halves
// of that matter. The signer reports a SHORT hash ("f68405bc") while the agent
// reports the full git hash, so equality calls a current device outdated; and
// vcgencmd appends a channel suffix ("… (release)"), so the token has to be
// taken before the comparison even happens.
func BootloaderMatchesBase(reported, base string) bool {
	r := bootloaderHashToken(reported)
	b := bootloaderHashToken(base)
	if r == "" || b == "" {
		return false
	}
	return strings.HasPrefix(r, b) || strings.HasPrefix(b, r)
}

// bootloaderHashToken reduces a firmware version string to its bare, lowercase
// hash token.
func bootloaderHashToken(v string) string {
	return strings.ToLower(NormalizeBootloaderVersion(v))
}
