package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret_tag"
	"github.com/google/uuid"
)

// SecretTagRepository persists tag color overrides + lifecycle. Membership
// stays the string on secrets.tags / secret_attachments.tag; this repository
// only stores the color and cascades rename/delete across those tables.
type SecretTagRepository interface {
	// List returns the account's explicit tag rows (color overrides).
	List(ctx context.Context, accountID uuid.UUID) ([]secret_tag.Tag, error)
	// GetByName fetches one tag row; returns ErrSecretTagNotFound if absent.
	GetByName(ctx context.Context, accountID uuid.UUID, name string) (*secret_tag.Tag, error)
	// Upsert creates the tag row or updates its color (idempotent).
	Upsert(ctx context.Context, tag *secret_tag.Tag) error
	// Rename cascades a tag rename across secrets, attachments and the tag row
	// in one transaction. Returns ErrSecretTagNameTaken if the new name is
	// already in use anywhere.
	Rename(ctx context.Context, accountID uuid.UUID, oldName, newName string) error
	// Delete cascades a tag removal: strips it from every secret, drops its
	// attachments and deletes the tag row, in one transaction.
	Delete(ctx context.Context, accountID uuid.UUID, name string) error
}
