package secret_list

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/google/uuid"
)

// Pagination bounds for the secret list.
const (
	DefaultPerPage = 50
	MaxPerPage     = 200
)

// SecretListQuery lists the account's secrets with optional filtering, sorting
// and pagination.
type SecretListQuery struct {
	Search     string     // substring match on name/key
	Kind       string     // env|credential|opaque; empty = all
	Sensitive  *bool      // nil = all
	Tags       []string   // AND semantics
	OUID       *uuid.UUID // delivered at this OU (incl. its nodes)
	NodeID     *uuid.UUID // delivered to this node
	Unattached bool       // no attachment at all
	Sort       string     // name|created_at|updated_at
	Order      string     // asc|desc
	Page       int        // 1-based
	PerPage    int

	AuthContext commands.AuthContext
}

// Validate normalizes pagination and tags, and rejects unknown filter values.
func (q *SecretListQuery) Validate() error {
	if q.Kind != "" && !secret.ValidKind(secret.SecretKind(q.Kind)) {
		return app_errors.ErrInvalidSecretKind
	}
	tags, err := secret.NormalizeTags(q.Tags)
	if err != nil {
		return err
	}
	q.Tags = tags
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PerPage <= 0 {
		q.PerPage = DefaultPerPage
	}
	if q.PerPage > MaxPerPage {
		q.PerPage = MaxPerPage
	}
	return nil
}
