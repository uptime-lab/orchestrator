package results

import (
	"time"
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

// SecretResult is the API DTO for a secret. For sensitive entries the
// decrypted value is never returned (HasValue reports whether one is set);
// non-sensitive entries ("variables") include the value for display.
type SecretResult struct {
	ID        uuid.UUID         `json:"id"`
	AccountID uuid.UUID         `json:"account_id"`
	Name      string            `json:"name"`
	Kind      secret.SecretKind `json:"kind"`
	Key       string            `json:"key"`
	HasValue  bool              `json:"has_value"`
	Sensitive bool              `json:"sensitive"`
	// Unreadable: a value IS stored but decrypts with none of the configured
	// keys. It ships alongside has_value=true because an empty value and an
	// unrecoverable one are otherwise the same JSON, and the operator's next
	// step (re-set it or delete it) depends on which it is.
	Unreadable bool     `json:"unreadable,omitempty"`
	Value      *string  `json:"value,omitempty"` // only for sensitive == false
	Tags       []string `json:"tags"`
	// Direct attachment target counts (group-provided reach is not included).
	OUCount    int    `json:"ou_count"`
	NodeCount  int    `json:"node_count"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	LastUsedAt *int64 `json:"last_used_at,omitempty"`
}

// NewSecretResult maps a domain secret to its DTO, gating value visibility on
// the sensitive flag.
func NewSecretResult(s *secret.Secret) SecretResult {
	res := SecretResult{
		ID:         s.ID,
		AccountID:  s.AccountID,
		Name:       s.Name,
		Kind:       s.Kind,
		Key:        s.Key,
		HasValue:   s.HasValue(),
		Sensitive:  s.Sensitive,
		Unreadable: s.ValueUnreadable,
		Tags:       append([]string{}, s.Tags...),
		CreatedAt:  s.CreatedAt,
		UpdatedAt:  s.UpdatedAt,
	}
	if !s.Sensitive {
		value := s.Value
		res.Value = &value
	}
	if s.LastUsedAt != nil {
		lastUsed := s.LastUsedAt.Unix()
		res.LastUsedAt = &lastUsed
	}
	return res
}

// SecretListResult is the list DTO. Total is the number of rows matching the
// filter regardless of pagination; Count is the number of items in this page.
type SecretListResult struct {
	Secrets []SecretResult `json:"secrets"`
	Count   int            `json:"count"`
	Total   int64          `json:"total"`
	Page    int            `json:"page"`
	PerPage int            `json:"per_page"`
}

// NewSecretListResult maps domain secrets to the list DTO.
func NewSecretListResult(secrets []secret.Secret, total int64, page, perPage int) SecretListResult {
	items := make([]SecretResult, len(secrets))
	for i := range secrets {
		items[i] = NewSecretResult(&secrets[i])
	}
	return SecretListResult{Secrets: items, Count: len(items), Total: total, Page: page, PerPage: perPage}
}

// RedactValues strips plaintext values (leaving has_value/metadata) for
// callers who lack secret.read_value. Sensitive secrets never carry a value
// anyway, so this only affects visible "variables".
func (r *SecretListResult) RedactValues() {
	for i := range r.Secrets {
		r.Secrets[i].Value = nil
	}
}

// CarriesValues reports whether any entry holds a plaintext value — whether
// serving the list unredacted would serve secret material at all.
func (r *SecretListResult) CarriesValues() bool {
	for i := range r.Secrets {
		if r.Secrets[i].Value != nil {
			return true
		}
	}
	return false
}

// ApplyAttachmentCounts fills the per-secret direct-attachment counts.
func (r *SecretListResult) ApplyAttachmentCounts(counts map[uuid.UUID]secret.AttachmentCounts) {
	for i := range r.Secrets {
		if c, ok := counts[r.Secrets[i].ID]; ok {
			r.Secrets[i].OUCount = c.OUs
			r.Secrets[i].NodeCount = c.Nodes
		}
	}
}

// SecretAttachmentResult is the API DTO for a secret attachment. Exactly one of
// secret_id / tag (the subject) and exactly one of ou_id / node_id (the
// target) is present.
type SecretAttachmentResult struct {
	ID       uuid.UUID  `json:"id"`
	SecretID *uuid.UUID `json:"secret_id,omitempty"`
	Tag      string     `json:"tag,omitempty"`
	OUID     *uuid.UUID `json:"ou_id,omitempty"`
	NodeID   *uuid.UUID `json:"node_id,omitempty"`
}

// NewSecretAttachmentResult maps a domain attachment to its DTO.
func NewSecretAttachmentResult(a *secret.SecretAttachment) SecretAttachmentResult {
	return SecretAttachmentResult{
		ID:       a.ID,
		SecretID: a.SecretID,
		Tag:      a.Tag,
		OUID:     a.OUID,
		NodeID:   a.NodeID,
	}
}

// SecretAttachmentListResult is the list DTO for attachments.
type SecretAttachmentListResult struct {
	Attachments []SecretAttachmentResult `json:"attachments"`
	Count       int                      `json:"count"`
}

// NewSecretAttachmentListResult maps domain attachments to the list DTO.
func NewSecretAttachmentListResult(atts []secret.SecretAttachment) SecretAttachmentListResult {
	items := make([]SecretAttachmentResult, len(atts))
	for i := range atts {
		items[i] = NewSecretAttachmentResult(&atts[i])
	}
	return SecretAttachmentListResult{Attachments: items, Count: len(items)}
}
