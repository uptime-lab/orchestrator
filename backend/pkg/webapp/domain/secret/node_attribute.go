package secret

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

// Node attributes (AD-style) are per-node custom key/value pairs. They are
// stored as ordinary env-kind secrets attached to a single node, which makes
// them survive a hardware swap (the attachment is to the logical node id), lets
// them be cleaned up in bulk from the secrets list, and delivers them to the
// agent env for free (keyed by the secret's Key). Two nodes can hold the same
// attribute key with different values because the secret Key is not unique —
// only the secret Name is, so attribute secrets get a reserved, node-scoped
// Name (attr:<nodeID>:<key>) that the user never sees.
const nodeAttributePrefix = "attr:"

// ErrInvalidAttributeKey rejects attribute keys outside the allowed charset.
// The charset is both a valid env-var name and safe to embed in a
// {{node.attributes.<key>}} reference (no dots, which the resolver splits on).
var ErrInvalidAttributeKey = errors.New(
	"attribute key must be 1-64 characters of A-Z, a-z, 0-9 or '_' and cannot start with a digit",
)

const maxAttributeKeyLen = 64

// ValidateAttributeKey enforces the attribute key charset/length: the agent's
// environment variable name rule (ValidEnvKey), bounded in length.
func ValidateAttributeKey(key string) error {
	if len(key) > maxAttributeKeyLen || !ValidEnvKey(key) {
		return ErrInvalidAttributeKey
	}
	return nil
}

// NodeAttributeName builds the reserved, account-unique secret name that backs
// a node attribute. Unique per (node, key) because node ids are globally unique
// and the key charset excludes the ':' separator.
func NodeAttributeName(nodeID uuid.UUID, key string) string {
	return NodeAttributeNamePrefix(nodeID) + key
}

// NodeAttributeNamePrefix is the name prefix shared by every attribute-backing
// secret of one node (attr:<nodeID>:). It is what bulk cleanup matches on when
// the node is deleted: the uuid is hex-and-dashes, so the prefix is safe to use
// verbatim in a SQL LIKE pattern.
func NodeAttributeNamePrefix(nodeID uuid.UUID) string {
	return nodeAttributePrefix + nodeID.String() + ":"
}

// ParseNodeAttributeKey returns the attribute key encoded in a backing secret's
// name for the given node, and whether the name is a node attribute for it.
func ParseNodeAttributeKey(name string, nodeID uuid.UUID) (string, bool) {
	want := NodeAttributeNamePrefix(nodeID)
	if !strings.HasPrefix(name, want) {
		return "", false
	}
	return name[len(want):], true
}

// IsNodeAttributeName reports whether a secret name is a node-attribute backing
// name for any node (used to keep attributes out of the {{node.attributes.*}}
// map unless they belong to the running node).
func IsNodeAttributeName(name string) bool {
	return strings.HasPrefix(name, nodeAttributePrefix)
}

// NodeAttribute is the projection of an attribute-backing secret: the key the
// user sees, its value (empty for the caller to hide when sensitive) and the
// sensitive flag that drives the "secret" toggle.
type NodeAttribute struct {
	SecretID  uuid.UUID
	Key       string
	Value     string
	Sensitive bool
	// Unreadable carries the backing secret's ValueUnreadable: the attribute is
	// set but its value decrypts with no configured key. Without it an
	// unrecoverable per-node token would render exactly like an empty one.
	Unreadable bool
}

// NodeAttributesFromSecrets projects a node's directly-attached secrets to the
// attributes carried for it, skipping any secret that is not a node attribute.
func NodeAttributesFromSecrets(nodeID uuid.UUID, secrets []Secret) []NodeAttribute {
	out := make([]NodeAttribute, 0, len(secrets))
	for i := range secrets {
		key, ok := ParseNodeAttributeKey(secrets[i].Name, nodeID)
		if !ok {
			continue
		}
		out = append(out, NodeAttribute{
			SecretID:   secrets[i].ID,
			Key:        key,
			Value:      secrets[i].Value,
			Sensitive:  secrets[i].Sensitive,
			Unreadable: secrets[i].ValueUnreadable,
		})
	}
	return out
}
