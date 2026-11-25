package results

import (
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
)

// EffectiveSecretSourceResult reports where an effective secret came from.
type EffectiveSecretSourceResult struct {
	Level string     `json:"level"` // node | ou | root
	Via   string     `json:"via"`   // direct | tag
	Tag   string     `json:"tag,omitempty"`
	OUID  *uuid.UUID `json:"ou_id,omitempty"`
}

// EffectiveSecretResult is one entry of a target's resolved secret set. Value
// is included only for non-sensitive entries.
type EffectiveSecretResult struct {
	ID        uuid.UUID                   `json:"id"`
	Name      string                      `json:"name"`
	Kind      string                      `json:"kind"`
	Key       string                      `json:"key"`
	Sensitive bool                        `json:"sensitive"`
	Value     *string                     `json:"value,omitempty"`
	Source    EffectiveSecretSourceResult `json:"source"`
}

// EffectiveSecretListResult is the resolved effective secret set of a node or
// an organizational unit.
type EffectiveSecretListResult struct {
	Secrets []EffectiveSecretResult `json:"secrets"`
	Count   int                     `json:"count"`
}

// NewEffectiveSecretListResult maps resolved secrets to the DTO, gating value
// visibility on the sensitive flag.
func NewEffectiveSecretListResult(secrets []services.ResolvedSecret) EffectiveSecretListResult {
	items := make([]EffectiveSecretResult, len(secrets))
	for i := range secrets {
		s := &secrets[i]
		items[i] = EffectiveSecretResult{
			ID:        s.ID,
			Name:      s.Name,
			Kind:      s.Kind,
			Key:       s.Key,
			Sensitive: s.Sensitive,
			Source: EffectiveSecretSourceResult{
				Level: s.Source.Level,
				Via:   s.Source.Via,
				Tag:   s.Source.Tag,
				OUID:  s.Source.OUID,
			},
		}
		if !s.Sensitive {
			value := s.Value
			items[i].Value = &value
		}
	}
	return EffectiveSecretListResult{Secrets: items, Count: len(items)}
}

// RedactValues strips plaintext values for callers lacking secret.read_value.
func (r *EffectiveSecretListResult) RedactValues() {
	for i := range r.Secrets {
		r.Secrets[i].Value = nil
	}
}
