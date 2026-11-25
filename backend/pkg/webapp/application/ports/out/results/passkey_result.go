package results

import (
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/passkey"
)

// PasskeyResult is one registered credential in API responses.
//
// The credential id and public key are deliberately absent: they are not
// secret, but nothing in the UI needs them, and the smallest useful shape is
// the right one for a credential list.
type PasskeyResult struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Synced reports whether the credential lives in a cloud keychain rather
	// than on one device. It changes the recovery story enough to show.
	Synced     bool       `json:"synced"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// NewPasskeyResult maps a credential into its wire shape.
func NewPasskeyResult(p *passkey.Passkey) PasskeyResult {
	return PasskeyResult{
		ID:         p.ID.String(),
		Name:       p.Name,
		Synced:     p.BackupState,
		CreatedAt:  p.CreatedAt,
		LastUsedAt: p.LastUsedAt,
	}
}

// NewPasskeyResults maps credentials into their wire shape.
func NewPasskeyResults(items []passkey.Passkey) []PasskeyResult {
	out := make([]PasskeyResult, 0, len(items))
	for i := range items {
		out = append(out, NewPasskeyResult(&items[i]))
	}
	return out
}
