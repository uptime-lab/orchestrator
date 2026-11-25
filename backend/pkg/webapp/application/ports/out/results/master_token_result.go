package results

import (
	"time"
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/master_token"
)

// MasterTokenResult represents the API response for a master token
type MasterTokenResult struct {
	ID              uuid.UUID `json:"id"`
	AccountID       uuid.UUID `json:"account_id"`
	Name            string    `json:"name"`
	Token           string    `json:"token,omitempty"`         // Only included on creation
	AdoptionFile    string    `json:"adoption_file,omitempty"` // Base64 encoded, only on creation
	IsActive        bool      `json:"is_active"`
	DefaultUsername string    `json:"default_username,omitempty"`
	SSHKeyID        *string   `json:"ssh_key_id,omitempty"`
	OUID            *string   `json:"ou_id,omitempty"`
	Tags            []string  `json:"tags"`
	ExpiresAt       *int64    `json:"expires_at,omitempty"`
	LastUsedAt      *int64    `json:"last_used_at,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

// NewMasterTokenResult creates a result from a domain model
func NewMasterTokenResult(mt *master_token.MasterToken) MasterTokenResult {
	result := MasterTokenResult{
		ID:              mt.ID,
		AccountID:       mt.AccountID,
		Name:            mt.Name,
		Token:           mt.Token, // Only set on creation
		IsActive:        mt.IsActive,
		DefaultUsername: mt.DefaultUsername,
		CreatedAt:       mt.CreatedAt,
		UpdatedAt:       mt.UpdatedAt,
	}

	if mt.SSHKeyID != nil {
		sshKeyID := mt.SSHKeyID.String()
		result.SSHKeyID = &sshKeyID
	}
	if mt.OUID != nil {
		ouID := mt.OUID.String()
		result.OUID = &ouID
	}
	result.Tags = append([]string{}, mt.Tags...)

	if mt.ExpiresAt != nil {
		expiresAt := mt.ExpiresAt.Unix()
		result.ExpiresAt = &expiresAt
	}

	if mt.LastUsedAt != nil {
		lastUsedAt := mt.LastUsedAt.Unix()
		result.LastUsedAt = &lastUsedAt
	}

	return result
}

// MasterTokenListResult represents the API response for a list of master tokens
type MasterTokenListResult struct {
	Tokens []MasterTokenResult `json:"tokens"`
	Count  int                 `json:"count"`
}

// NewMasterTokenListResult creates a list result from domain models
func NewMasterTokenListResult(tokens []master_token.MasterToken) MasterTokenListResult {
	results := make([]MasterTokenResult, len(tokens))
	for i, t := range tokens {
		results[i] = NewMasterTokenResult(&t)
		results[i].Token = "" // Never include token in list responses
	}

	return MasterTokenListResult{
		Tokens: results,
		Count:  len(results),
	}
}
