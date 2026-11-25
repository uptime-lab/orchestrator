package secret_create

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/google/uuid"
)

// SecretCreateCommand creates a secret within the account.
type SecretCreateCommand struct {
	ID    uuid.UUID         `json:"-"` // set internally
	Name  string            `json:"name"`
	Kind  secret.SecretKind `json:"kind"`  // env|credential|opaque
	Key   string            `json:"key"`   // env: ENV var name; credential: username; opaque: optional label
	Value string            `json:"value"` // the secret value (stored encrypted)
	// Sensitive defaults to true when omitted. Only env entries may be
	// non-sensitive ("variables", value readable through the API); credential
	// and opaque entries are always sensitive.
	Sensitive *bool `json:"sensitive,omitempty"`
	// Tags are free-form purpose labels (lowercased, deduped, max 10).
	Tags []string `json:"tags,omitempty"`

	AuthContext commands.AuthContext `json:"-"`
}
