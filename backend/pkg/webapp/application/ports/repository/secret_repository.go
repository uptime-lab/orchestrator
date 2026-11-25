package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/google/uuid"
)

// SecretListFilter narrows, sorts and pages an account's secret list.
type SecretListFilter struct {
	Search    string   // case-insensitive substring match on name/key
	Kind      string   // env|credential|opaque; empty = all
	Sensitive *bool    // nil = all
	Tags      []string // AND semantics; normalized tags only
	// OUID filters to secrets delivered at that OU: attached to the OU itself
	// (directly or via group) or to any node inside it.
	OUID *uuid.UUID
	// NodeID filters to secrets delivered to that node (direct or via group).
	NodeID *uuid.UUID
	// Unattached filters to secrets with no attachment at all (not directly,
	// not via their group).
	Unattached bool
	Sort       string // name|created_at|updated_at (default name)
	Order      string // asc|desc (default asc)
	Offset     int
	Limit      int // 0 = no limit
}

// SortColumn returns the validated sort column. The allowlist lives on the
// filter type itself so every consumer inherits it by construction — the raw
// Sort field is caller input and must never be interpolated into SQL.
func (f SecretListFilter) SortColumn() string {
	switch f.Sort {
	case "name", "created_at", "updated_at":
		return f.Sort
	}
	return "name"
}

// OrderDirection returns "ASC" or "DESC", never anything else.
func (f SecretListFilter) OrderDirection() string {
	if f.Order == "desc" {
		return "DESC"
	}
	return "ASC"
}

// SecretRepository persists secrets. Implementations encrypt the value at rest
// and decrypt it on read.
type SecretRepository interface {
	Create(ctx context.Context, s *secret.Secret) error
	// CreateUnderCap creates a secret only while the account is below its cap,
	// holding the count and the insert under one per-account lock. Required
	// wherever creation can run concurrently — which is everywhere, since the
	// workflow engine drives server.attribute_upsert hundreds of nodes wide and
	// node attributes ARE secrets. maxPerAccount <= 0 means unlimited.
	CreateUnderCap(ctx context.Context, s *secret.Secret, maxPerAccount int) error
	Update(ctx context.Context, s *secret.Secret) error
	GetByID(ctx context.Context, accountID, id uuid.UUID) (*secret.Secret, error)
	// GetByIDs loads the secrets with the given IDs in one query; IDs that no
	// longer exist are silently skipped.
	GetByIDs(ctx context.Context, accountID uuid.UUID, ids []uuid.UUID) ([]secret.Secret, error)
	// GetByName fetches one secret by its (account-unique) name.
	GetByName(ctx context.Context, accountID uuid.UUID, name string) (*secret.Secret, error)
	GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]secret.Secret, error)
	// List returns a filtered, sorted page of the account's secrets plus the
	// total number of rows matching the filter (ignoring pagination).
	List(ctx context.Context, accountID uuid.UUID, filter SecretListFilter) ([]secret.Secret, int64, error)
	// TagCounts returns every tag in the account with the number of secrets
	// carrying it.
	TagCounts(ctx context.Context, accountID uuid.UUID) (map[string]int, error)
	// CountUnattached returns how many secrets have no attachment at all
	// (neither direct nor via their group).
	CountUnattached(ctx context.Context, accountID uuid.UUID) (int64, error)
	// CountAll returns the total number of secrets in the account.
	CountAll(ctx context.Context, accountID uuid.UUID) (int64, error)
	// GetByTags lists every secret carrying at least one of the tags, in one
	// query (dynamic tag-attachment membership).
	GetByTags(ctx context.Context, accountID uuid.UUID, tagList []string) ([]secret.Secret, error)
	// TouchLastUsed stamps last_used_at = now on the given secrets, throttled
	// to at most once per hour per row (best-effort usage tracking).
	TouchLastUsed(ctx context.Context, accountID uuid.UUID, ids []uuid.UUID) error
	Delete(ctx context.Context, accountID, id uuid.UUID) error
	// DeleteNodeAttributes removes every attribute-backing secret of a node
	// (name attr:<nodeID>:*), returning how many rows went. Node deletion
	// cleanup: nothing else ever deletes them, and each one counts against
	// SECRET_MAX_PER_ACCOUNT for as long as it survives.
	DeleteNodeAttributes(ctx context.Context, accountID, nodeID uuid.UUID) (int64, error)
}
