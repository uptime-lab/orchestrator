package results

import (
	"time"
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_key"
)

// SSHKeyResult represents an SSH key in API responses (without key content)
type SSHKeyResult struct {
	ID          uuid.UUID `json:"id"`
	AccountID   uuid.UUID `json:"account_id"`
	Alias       string    `json:"alias"`
	KeyType     string    `json:"key_type"` // "ssh" or "ca"
	Fingerprint string    `json:"fingerprint"`
	IsDefault   bool      `json:"is_default"`
	Tags        []string  `json:"tags"`
	// HasPrivateKey marks a server-generated connection keypair (the private key
	// is held encrypted and never returned; only the public part is downloadable).
	HasPrivateKey bool  `json:"has_private_key"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// NewSSHKeyResult creates a new SSHKeyResult from a domain SSHKey
func NewSSHKeyResult(domainKey *ssh_key.SSHKey) SSHKeyResult {
	return SSHKeyResult{
		ID:            domainKey.ID,
		AccountID:     domainKey.AccountID,
		Alias:         domainKey.Alias,
		KeyType:       string(domainKey.KeyType),
		Fingerprint:   domainKey.Fingerprint,
		IsDefault:     domainKey.IsDefault,
		Tags:          append([]string{}, domainKey.Tags...),
		HasPrivateKey: domainKey.HasPrivateKey,
		CreatedAt:     domainKey.CreatedAt,
		UpdatedAt:     domainKey.UpdatedAt,
	}
}

// SSHKeyListResult is a list of SSH keys in API responses.
//
// It carries a COUNT and no limit/offset. Those two were here, set to
// (len(items), 0) at the only call site — a client reading them as a page size
// and an offset would page forever over the same whole list, believing it was
// paginating. An unpaginated list saying so is the honest shape; when this list
// does gain paging it takes the `page`/`per_page`/`total` envelope the audit,
// execution and secret lists already use, not a second convention.
type SSHKeyListResult struct {
	Keys  []SSHKeyResult `json:"keys"`
	Count int            `json:"count"`
}

// NewSSHKeyListResult builds the list envelope.
func NewSSHKeyListResult(keys []SSHKeyResult) *SSHKeyListResult {
	return &SSHKeyListResult{
		Keys:  keys,
		Count: len(keys),
	}
}
