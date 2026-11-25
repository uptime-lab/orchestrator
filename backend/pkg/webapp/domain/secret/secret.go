package secret

import (
	"errors"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/tags"
	"github.com/google/uuid"
)

// Domain validation errors.
var (
	ErrNameRequired = errors.New("secret name is required")
	ErrInvalidKind  = errors.New("invalid secret kind")
	// ErrTooManyTags / ErrInvalidTag are the shared tag rules (domain/tags).
	ErrTooManyTags = tags.ErrTooManyTags
	ErrInvalidTag  = tags.ErrInvalidTag
	// ErrCannotDesensitize is returned when a sensitive secret is being turned
	// into a non-sensitive variable. The transition would expose a value that
	// was stored write-only; re-create the entry instead.
	ErrCannotDesensitize = errors.New("a sensitive secret cannot be made non-sensitive; create a new variable instead")
)

// SecretKind is the type of a secret. It disambiguates how Key/Value are
// interpreted by consumers.
type SecretKind string

const (
	// KindEnv models an environment variable: Key is the ENV var name, Value is
	// its value.
	KindEnv SecretKind = "env"
	// KindCredential models a username/password pair: Key is the username, Value
	// is the password.
	KindCredential SecretKind = "credential"
	// KindOpaque models an arbitrary secret: Key is an optional label, Value is
	// the secret.
	KindOpaque SecretKind = "opaque"
)

// ValidKind reports whether k is one of the supported secret kinds.
func ValidKind(k SecretKind) bool {
	switch k {
	case KindEnv, KindCredential, KindOpaque:
		return true
	default:
		return false
	}
}

// Secret is an account-scoped named entry. Value is stored encrypted at rest.
// Sensitive entries ("secrets") are write-only through the API; non-sensitive
// entries ("variables", env kind only) return their value for display and
// inline editing. Name is unique per account — it is the public identifier
// used by {{secrets.<name>}} workflow references.
type Secret struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	Name      string     // human label (required, unique per account)
	Kind      SecretKind // env | credential | opaque
	Key       string     // env: ENV var name; credential: username; opaque: optional label
	Value     string     // env: value; credential: password; opaque: the secret
	Sensitive bool       // credential/opaque: always true; env: false makes it a visible variable
	Tags      []string   // free-form purpose labels (normalized: lowercase, unique, max 10)
	// ValueUnreadable marks a stored value that exists but decrypts with none of
	// the configured keys (a SECRETS_ENCRYPTION_KEY_OLD dropped one boot early, a
	// dump restored without its key). Value is empty then, and the two are read
	// together: an empty Value alone means "none set", an empty Value with this
	// flag means "set but unrecoverable". Read paths report it so the row can be
	// found and re-set; every path that DELIVERS a value must refuse it.
	ValueUnreadable bool

	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastUsedAt *time.Time // last materialization into an agent command / workflow context (hour-granular)
}

// NewSecret creates a secret, validating a non-empty name and a known kind.
// Credential and opaque kinds are forced sensitive regardless of the argument.
func NewSecret(accountID uuid.UUID, name string, kind SecretKind, key, value string, sensitive bool) (*Secret, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrNameRequired
	}
	if !ValidKind(kind) {
		return nil, ErrInvalidKind
	}
	if kind != KindEnv {
		sensitive = true
	}
	now := time.Now()
	return &Secret{
		ID:        uuid.New(),
		AccountID: accountID,
		Name:      name,
		Kind:      kind,
		Key:       key,
		Value:     value,
		Sensitive: sensitive,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// HasValue reports whether the secret carries a non-empty value. An unreadable
// value is still a value that is set — reporting it as absent would tell an
// operator the entry is empty and invite them to leave it alone.
func (s *Secret) HasValue() bool {
	return s.Value != "" || s.ValueUnreadable
}

// NormalizeTags applies the shared tag rules (domain/tags) to a secret's tag
// list.
func NormalizeTags(raw []string) ([]string, error) {
	return tags.Normalize(raw)
}

// MergeTags unions two normalized tag lists, preserving order and the cap.
func MergeTags(existing, extra []string) ([]string, error) {
	return tags.Merge(existing, extra)
}

// UpsertOptions tunes UpsertForNode (the workflow secret.upsert path).
type UpsertOptions struct {
	// Sensitive: nil keeps an existing entry's flag (or defaults a new entry to
	// true). An explicit false only applies to env entries and never
	// desensitizes an existing sensitive entry.
	Sensitive *bool
	// Tags are unioned into the secret's existing tag list (normalized).
	Tags []string
	// RunID names the playbook execution that wrote the value, for the audit
	// record (uuid.Nil when none).
	RunID uuid.UUID
}

// ChangeSensitivity applies a sensitivity transition. Turning a sensitive
// entry into a non-sensitive one is forbidden (it would expose a write-only
// value); the reverse is allowed for env entries, and credential/opaque
// entries are always sensitive.
func (s *Secret) ChangeSensitivity(sensitive bool) error {
	if !sensitive && (s.Sensitive || s.Kind != KindEnv) {
		return ErrCannotDesensitize
	}
	s.Sensitive = sensitive
	return nil
}
