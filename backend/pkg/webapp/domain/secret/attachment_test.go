package secret

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestNewSecretAttachmentExactlyOneTarget(t *testing.T) {
	acct := uuid.New()
	sec := uuid.New()
	ou := uuid.New()
	node := uuid.New()

	if _, err := NewSecretAttachment(acct, sec, &ou, nil); err != nil {
		t.Fatalf("OU-only attachment should be valid, got %v", err)
	}
	if _, err := NewSecretAttachment(acct, sec, nil, &node); err != nil {
		t.Fatalf("node-only attachment should be valid, got %v", err)
	}
	if _, err := NewSecretAttachment(acct, sec, nil, nil); !errors.Is(err, ErrInvalidAttachmentTarget) {
		t.Fatalf("no target should be invalid, got %v", err)
	}
	if _, err := NewSecretAttachment(acct, sec, &ou, &node); !errors.Is(err, ErrInvalidAttachmentTarget) {
		t.Fatalf("both targets should be invalid, got %v", err)
	}
}

func TestTagAttachmentSubjectInvariants(t *testing.T) {
	acct := uuid.New()
	ou := uuid.New()

	att, err := NewTagAttachment(acct, " Infra ", &ou, nil)
	if err != nil {
		t.Fatalf("tag->OU attachment should be valid, got %v", err)
	}
	if att.Tag != "infra" {
		t.Fatalf("expected normalized tag, got %q", att.Tag)
	}
	if _, err := NewTagAttachment(acct, "infra", nil, nil); !errors.Is(err, ErrInvalidAttachmentTarget) {
		t.Fatalf("no target should be invalid, got %v", err)
	}
	if _, err := NewTagAttachment(acct, "has space", &ou, nil); err == nil {
		t.Fatal("invalid tag charset should be rejected")
	}
	if _, err := NewTagAttachment(acct, "", &ou, nil); !errors.Is(err, ErrInvalidAttachmentSubject) {
		t.Fatalf("empty tag should be invalid, got %v", err)
	}

	// Both subjects / no subject: rejected via Validate.
	subject := uuid.New()
	both := &SecretAttachment{ID: uuid.New(), AccountID: acct, SecretID: &subject, Tag: "infra", OUID: &ou}
	if err := both.Validate(); !errors.Is(err, ErrInvalidAttachmentSubject) {
		t.Fatalf("both subjects should be invalid, got %v", err)
	}
	none := &SecretAttachment{ID: uuid.New(), AccountID: acct, OUID: &ou}
	if err := none.Validate(); !errors.Is(err, ErrInvalidAttachmentSubject) {
		t.Fatalf("no subject should be invalid, got %v", err)
	}
}
