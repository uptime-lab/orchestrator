package secret

import (
	"errors"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/tags"
	"github.com/google/uuid"
)

// Attachment validation errors.
var (
	// ErrInvalidAttachmentTarget is returned when an attachment does not target
	// exactly one of an organizational unit or a node.
	ErrInvalidAttachmentTarget = errors.New("secret attachment must target exactly one of an organizational unit or a node")
	// ErrInvalidAttachmentSubject is returned when an attachment does not carry
	// exactly one subject — a secret or a tag.
	ErrInvalidAttachmentSubject = errors.New("secret attachment must reference exactly one of a secret or a tag")
)

// AttachmentCounts is how many targets of each type a subject (secret or tag)
// is directly attached to.
type AttachmentCounts struct {
	OUs   int
	Nodes int
}

// SecretAttachment binds a subject — a single Secret (SecretID) or a Tag
// (every secret carrying it, dynamic membership) — to a target: an
// organizational unit (OUID) or a single node (NodeID). Exactly one subject
// and exactly one target must be set. Attachments are the many-to-many link
// that drives effective-set resolution.
type SecretAttachment struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	SecretID  *uuid.UUID // set when the subject is a single secret
	Tag       string     // set when the subject is a tag ("" = not a tag attachment)
	OUID      *uuid.UUID // set when attached to an organizational unit
	NodeID    *uuid.UUID // set when attached directly to a node
}

// NewSecretAttachment creates a secret-subject attachment, validating that
// exactly one target (ouID XOR nodeID) is provided.
func NewSecretAttachment(accountID, secretID uuid.UUID, ouID, nodeID *uuid.UUID) (*SecretAttachment, error) {
	att := &SecretAttachment{
		ID:        uuid.New(),
		AccountID: accountID,
		SecretID:  &secretID,
		OUID:      ouID,
		NodeID:    nodeID,
	}
	if err := att.Validate(); err != nil {
		return nil, err
	}
	return att, nil
}

// NewTagAttachment creates a tag-subject attachment (dynamic membership: the
// attachment delivers every secret carrying the tag, including secrets tagged
// later). The tag is normalized with the shared rules.
func NewTagAttachment(accountID uuid.UUID, tag string, ouID, nodeID *uuid.UUID) (*SecretAttachment, error) {
	normalized, err := tags.Normalize([]string{tag})
	if err != nil {
		return nil, err
	}
	if len(normalized) != 1 {
		return nil, ErrInvalidAttachmentSubject
	}
	att := &SecretAttachment{
		ID:        uuid.New(),
		AccountID: accountID,
		Tag:       normalized[0],
		OUID:      ouID,
		NodeID:    nodeID,
	}
	if err := att.Validate(); err != nil {
		return nil, err
	}
	return att, nil
}

// Validate enforces the exactly-one-subject and exactly-one-target invariants.
func (a *SecretAttachment) Validate() error {
	if (a.SecretID == nil) == (a.Tag == "") {
		return ErrInvalidAttachmentSubject
	}
	if (a.OUID == nil) == (a.NodeID == nil) {
		return ErrInvalidAttachmentTarget
	}
	return nil
}
