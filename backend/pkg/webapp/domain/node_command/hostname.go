package node_command

import (
	"errors"
	"regexp"
	"strings"
)

// ErrInvalidHostname is returned for a set_hostname value the device would
// reject. It deliberately does NOT name the offending value: a dispatch error
// is written verbatim into node_commands.details, which is plaintext and
// readable by every node.read caller, and the value can be a resolved
// {{secrets.*}}.
var ErrInvalidHostname = errors.New("hostname must be an RFC 1123 label: lowercase a-z, 0-9 and '-', not starting or ending with '-', up to 63 characters")

// hostnameLabel is the RFC 1123 label the agent's handler enforces and the
// editor's input already validates against. The platform holds the same line so
// a rejected value never reaches a device at all — the agent used to answer with
// the value quoted back inside its failure report.
var hostnameLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// NormalizeHostname trims a set_hostname value and reports whether the device
// will accept it.
func NormalizeHostname(hostname string) (string, error) {
	trimmed := strings.TrimSpace(hostname)
	if !hostnameLabel.MatchString(trimmed) {
		return "", ErrInvalidHostname
	}
	return trimmed, nil
}
