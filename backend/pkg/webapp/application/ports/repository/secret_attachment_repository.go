package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/google/uuid"
)

// SecretAttachmentRepository persists the many-to-many links between subjects
// (secrets or secret groups) and their targets (organizational units or nodes).
type SecretAttachmentRepository interface {
	// Attach persists an attachment. Implementations are idempotent: attaching
	// an existing (subject, target) pair succeeds and reports the existing row.
	Attach(ctx context.Context, att *secret.SecretAttachment) error
	// Detach removes an attachment by its ID, scoped to the account.
	Detach(ctx context.Context, accountID, attachmentID uuid.UUID) error
	// ListBySecret lists all attachments whose subject is the given secret.
	ListBySecret(ctx context.Context, accountID, secretID uuid.UUID) ([]secret.SecretAttachment, error)
	// ListByTag lists all attachments whose subject is the given tag.
	ListByTag(ctx context.Context, accountID uuid.UUID, tag string) ([]secret.SecretAttachment, error)
	// ListByOU lists attachments (any subject) targeting a single organizational unit.
	ListByOU(ctx context.Context, accountID, ouID uuid.UUID) ([]secret.SecretAttachment, error)
	// ListByNode lists attachments (any subject) targeting a single node.
	ListByNode(ctx context.Context, accountID, nodeID uuid.UUID) ([]secret.SecretAttachment, error)
	// ListByOUs is a batch helper listing attachments targeting any of the OUs.
	ListByOUs(ctx context.Context, accountID uuid.UUID, ouIDs []uuid.UUID) ([]secret.SecretAttachment, error)
	// DeliveryCountsByOU returns, per OU, how many distinct secrets are
	// delivered at it: attached to the OU itself (directly or via a group) or
	// to any node inside it.
	DeliveryCountsByOU(ctx context.Context, accountID uuid.UUID) (map[uuid.UUID]int, error)
	// CountsBySubject returns, for every attachment subject in the account,
	// how many OUs and nodes it is directly attached to. Secret subjects and
	// tag subjects are returned in separate maps.
	CountsBySubject(ctx context.Context, accountID uuid.UUID) (secrets map[uuid.UUID]secret.AttachmentCounts, tagCounts map[string]secret.AttachmentCounts, err error)
	// DeleteBySecret removes every attachment of a secret.
	DeleteBySecret(ctx context.Context, accountID, secretID uuid.UUID) error
	// DeleteByTag removes every attachment of a tag subject.
	DeleteByTag(ctx context.Context, accountID uuid.UUID, tag string) error
	// DeleteByOUID removes every attachment targeting an organizational unit
	// (OU deletion cleanup). Attachments to the OU's nodes are untouched.
	DeleteByOUID(ctx context.Context, accountID, ouID uuid.UUID) error
	// DeleteByNodeID removes every attachment targeting a node (node deletion
	// cleanup). The subjects themselves are untouched: an attachment is a link,
	// and the secret behind it is an account-level object.
	DeleteByNodeID(ctx context.Context, accountID, nodeID uuid.UUID) error
}
