package secret

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxNameLength is the column's width: a longer name fails at the database
// instead of at the request.
const MaxNameLength = 255

// MaxValueBytes bounds one secret's value. Every env-kind value a node
// inherits travels in ONE provision_connection command, inside the 8 MiB gRPC
// frame shared by both ends, and lands as one KEY=value line of the node's env
// file; opaque and credential values are substituted into command fields that
// are themselves capped well below the frame. 64 KiB holds a certificate chain
// or a kubeconfig with room to spare, while a hundred of them still fit a frame.
const MaxValueBytes = 64 * 1024

var (
	// ErrInvalidName: a name a {{secrets.<name>}} reference cannot reach. The
	// run-time resolver takes everything between "{{ secrets." and the first
	// "}", trimmed, so a name is referenceable exactly when it has no "}" —
	// a "}" ends the reference early and the lookup misses.
	ErrInvalidName = errors.New(`secret name must not contain "}": a {{secrets.<name>}} reference ends at the first "}"`)
	// ErrNameTooLong: over MaxNameLength characters.
	ErrNameTooLong = fmt.Errorf("secret name must be at most %d characters", MaxNameLength)
	// ErrReservedName: the prefix node attributes are stored under. A secret
	// named like one would be read as another node's attribute.
	ErrReservedName = fmt.Errorf("secret names starting with %q are reserved for node attributes", nodeAttributePrefix)
	// ErrValueTooLarge: over MaxValueBytes.
	ErrValueTooLarge = fmt.Errorf("secret value must be at most %d KiB", MaxValueBytes/1024)
	// ErrValueLineBreak: an environment value is written as one KEY=value
	// line, and the agent refuses the whole env set when a value carries a
	// line break — which fails every provisioning of every node the value
	// reaches, far from the edit that caused it.
	ErrValueLineBreak = errors.New("an environment variable value must not contain a line break")
	// ErrInvalidEnvKey: the agent writes an env set one KEY=value line per
	// entry and refuses the whole set when a key is not a shell identifier —
	// which, like a line break in a value, fails every provisioning of every
	// node the entry reaches.
	ErrInvalidEnvKey = errors.New("an environment variable name must start with a letter or '_' and contain only letters, digits and '_'")
)

// envKeyPattern is the agent's rule for an environment variable name, kept
// identical to it: a key the API accepts must be one the agent writes.
var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidEnvKey reports whether key is a name the agent accepts as an
// environment variable.
func ValidEnvKey(key string) bool {
	return envKeyPattern.MatchString(key)
}

// ValidateKey checks the key of an entry about to be stored with a new key.
// Only the env kind is constrained: its key becomes a variable name on the
// node, while a credential's key is a username and an opaque one a label. A
// stored key that predates the rule is not re-checked by callers that leave
// it unchanged, so such an entry stays readable, editable and deletable.
func ValidateKey(kind SecretKind, key string) error {
	if kind == KindEnv && !ValidEnvKey(key) {
		return ErrInvalidEnvKey
	}
	return nil
}

// ValidateName checks a name an operator chose, at create and at rename. Only
// there: a stored name that predates the rule keeps resolving, attaching and
// reading, and the reserved node-attribute names are built by the platform,
// never through this check.
func ValidateName(name string) error {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return ErrNameRequired
	case utf8.RuneCountInString(name) > MaxNameLength:
		return ErrNameTooLong
	case strings.Contains(name, "}"):
		return ErrInvalidName
	case IsNodeAttributeName(name):
		return ErrReservedName
	}
	return nil
}

// ValidateValue checks a value about to be stored: the size bound for every
// kind, and the single-line rule for the kind that is written into an env
// file (env secrets and node attributes alike).
func ValidateValue(kind SecretKind, value string) error {
	if len(value) > MaxValueBytes {
		return ErrValueTooLarge
	}
	if kind == KindEnv && strings.ContainsAny(value, "\n\r") {
		return ErrValueLineBreak
	}
	return nil
}
