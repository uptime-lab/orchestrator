package node_command

import (
	"strconv"
	"strings"
)

// CompareSemver compares two dotted numeric versions (major.minor.patch, with a
// leading "v" and any pre-release/build suffix tolerated and ignored). It
// returns -1 if a < b, 0 if equal, and +1 if a > b. A component that fails to
// parse is treated as 0. This is the project's own comparator: the version-like
// helper in the storage layer sorts CDN paths, not semver.
func CompareSemver(a, b string) int {
	pa := parseSemver(a)
	pb := parseSemver(b)
	for i := 0; i < 3; i++ {
		if pa[i] < pb[i] {
			return -1
		}
		if pa[i] > pb[i] {
			return 1
		}
	}
	return 0
}

// parseSemver extracts up to three numeric components. Missing components are
// zero; a non-numeric component (or a pre-release/build suffix) stops parsing.
func parseSemver(v string) [3]int {
	var out [3]int
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	// Drop any build/pre-release suffix.
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			break
		}
		out[i] = n
	}
	return out
}

// MeetsMinAgentVersion reports whether an agent reporting agentVersion may run a
// command requiring minVersion. It is FAIL-CLOSED: an empty, "latest", or
// unparseable agent version is treated as too old, because dispatching a
// command an old agent cannot decode would strand it (and, for access config,
// could touch sshd on a version that mishandles it). "latest" is not assumed to
// be new — a stale binary can still self-report it.
func MeetsMinAgentVersion(agentVersion, minVersion string) bool {
	agentVersion = strings.TrimSpace(agentVersion)
	if agentVersion == "" || strings.EqualFold(agentVersion, "latest") {
		return false
	}
	if parseSemver(agentVersion) == ([3]int{0, 0, 0}) && agentVersion != "0.0.0" {
		// Unparseable / non-numeric version — fail closed.
		return false
	}
	return CompareSemver(agentVersion, minVersion) >= 0
}

// SameAgentVersion reports whether an explicit requested agent version names
// the version a node reports running. "latest" never does: what it resolves
// to is the CDN's answer, not something the platform can compare.
func SameAgentVersion(requested, running string) bool {
	requested = strings.TrimPrefix(strings.TrimSpace(requested), "v")
	running = strings.TrimPrefix(strings.TrimSpace(running), "v")
	if requested == "" || running == "" || strings.EqualFold(requested, "latest") {
		return false
	}
	return requested == running
}
