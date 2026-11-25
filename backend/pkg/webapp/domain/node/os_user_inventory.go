package node

import (
	"strconv"
	"strings"
)

// OSUser is one real OS account discovered on a device by an inventory scan,
// distinct from the Orchestrator-managed access entries. Read-only: answering
// "who is actually on this disk", which the managed list deliberately never
// shows.
type OSUser struct {
	Username string `json:"username"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
	Home     string `json:"home"`
	Shell    string `json:"shell"`
	// Managed is true when the user belongs to the orchestrator-managed POSIX
	// group — i.e. Orchestrator owns this account's keys — as opposed to an
	// account baked in at install or added by hand.
	Managed bool `json:"managed"`
}

// OSUserScanResult is the polled outcome of an OS-user scan: still-running,
// failed, or the parsed inventory. Raw getent output is parsed server-side and
// never returned to the caller.
type OSUserScanResult struct {
	Status string   `json:"status"` // "running" | "completed" | "failed"
	Detail string   `json:"detail,omitempty"`
	Users  []OSUser `json:"users,omitempty"`
}

// OSUserScanScript is the fixed, side-effect-free script the scan dispatches.
// A constant (never operator-supplied) so the endpoint is a narrow read, not a
// general run_script: it lists the passwd database and the members of the
// managed group, separated by a sentinel the parser splits on.
const OSUserScanScript = `getent passwd; echo '---ORCH-MANAGED---'; getent group orchestrator-managed`

// osUserScanSeparator delimits the passwd dump from the managed-group line.
const osUserScanSeparator = "---ORCH-MANAGED---"

// loginShell reports whether a shell is a real interactive login shell (not
// nologin/false), one half of the "is this a human-usable account" test.
func loginShell(shell string) bool {
	switch {
	case shell == "", strings.HasSuffix(shell, "/nologin"), strings.HasSuffix(shell, "/false"):
		return false
	default:
		return true
	}
}

// ParseOSUserInventory turns the raw scan stdout into the login accounts worth
// showing: UID >= 1000 (skips system/service accounts that would otherwise
// drown the list) with a real login shell, tagged managed when they are in the
// orchestrator-managed group. The passwd line format is
// name:passwd:uid:gid:gecos:home:shell; the managed group line is
// name:passwd:gid:member,member,...
func ParseOSUserInventory(stdout string) []OSUser {
	passwdPart := stdout
	managed := map[string]bool{}

	if idx := strings.Index(stdout, osUserScanSeparator); idx >= 0 {
		passwdPart = stdout[:idx]
		groupPart := stdout[idx+len(osUserScanSeparator):]
		for _, line := range strings.Split(groupPart, "\n") {
			fields := strings.Split(strings.TrimSpace(line), ":")
			if len(fields) < 4 || fields[0] != "orchestrator-managed" {
				continue
			}
			for _, m := range strings.Split(fields[3], ",") {
				if m = strings.TrimSpace(m); m != "" {
					managed[m] = true
				}
			}
		}
	}

	var users []OSUser
	for _, line := range strings.Split(passwdPart, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}
		uid, err := strconv.Atoi(fields[2])
		if err != nil || uid < 1000 {
			continue
		}
		gid, _ := strconv.Atoi(fields[3])
		shell := fields[6]
		if !loginShell(shell) {
			continue
		}
		name := fields[0]
		users = append(users, OSUser{
			Username: name,
			UID:      uid,
			GID:      gid,
			Home:     fields[5],
			Shell:    shell,
			Managed:  managed[name],
		})
	}
	return users
}
