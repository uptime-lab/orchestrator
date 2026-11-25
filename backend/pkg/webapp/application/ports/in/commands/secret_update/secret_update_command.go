package secret_update

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// SecretUpdateCommand updates a secret. Nil fields are left unchanged. For
// Value, nil means "unchanged" while a pointer to "" sets an empty value. Kind
// is immutable and cannot be changed after creation.
type SecretUpdateCommand struct {
	ID    uuid.UUID `json:"-"`
	Name  *string   `json:"name,omitempty"`
	Key   *string   `json:"key,omitempty"`
	Value *string   `json:"value,omitempty"` // nil = unchanged, "" = set empty
	// Sensitive: nil = unchanged. The true->false transition is rejected
	// (a write-only value must never become readable); false->true is allowed.
	Sensitive *bool `json:"sensitive,omitempty"`
	// Tags: nil = unchanged, [] = clear, list = replace.
	Tags *[]string `json:"tags,omitempty"`

	AuthContext commands.AuthContext `json:"-"`
}
