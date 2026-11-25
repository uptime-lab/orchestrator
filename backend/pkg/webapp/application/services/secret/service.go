package secret

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_delete"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/secret_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/secret_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

// Service implements the secret CRUD use cases and attachment management
// (subjects: single secrets and tags).
type Service struct {
	repo           repository.SecretRepository
	attachmentRepo repository.SecretAttachmentRepository
	nodeRepo       repository.NodeRepository               // account-scoped ownership check for node targets
	ouRepo         repository.OrganizationalUnitRepository // account-scoped ownership check for OU targets
	audit          *audit.Recorder
	// maxPerAccount is the per-account secret cap (SetMaxSecretsPerAccount,
	// 0 = unlimited).
	maxPerAccount int
	logger        *slog.Logger
}

// NewService wires the secret service. All repositories are required.
func NewService(repo repository.SecretRepository, attachmentRepo repository.SecretAttachmentRepository, nodeRepo repository.NodeRepository, ouRepo repository.OrganizationalUnitRepository, auditRecorder *audit.Recorder, logger *slog.Logger) *Service {
	if repo == nil {
		panic("secret.Service requires a secret repository")
	}
	if attachmentRepo == nil {
		panic("secret.Service requires a secret attachment repository")
	}
	if nodeRepo == nil {
		panic("secret.Service requires a node repository")
	}
	if ouRepo == nil {
		panic("secret.Service requires an organizational unit repository")
	}
	if auditRecorder == nil {
		panic("secret.Service requires an audit recorder")
	}
	return &Service{repo: repo, attachmentRepo: attachmentRepo, nodeRepo: nodeRepo, ouRepo: ouRepo, audit: auditRecorder, logger: logger.With("service", "secret")}
}

// validateAttachmentTarget verifies the attachment's target (an OU or a node)
// belongs to the caller's account, so an attachment can never be wired to another
// tenant's node/OU. Domain validates the target shape (exactly one set); this
// checks ownership of whichever is present via the account-scoped GetByID.
func (s *Service) validateAttachmentTarget(ctx context.Context, accountID uuid.UUID, ouID, nodeID *uuid.UUID) error {
	if nodeID != nil {
		if _, err := s.nodeRepo.GetByID(ctx, accountID, *nodeID); err != nil {
			return err
		}
	}
	if ouID != nil {
		if _, err := s.ouRepo.GetByID(ctx, accountID, *ouID); err != nil {
			return err
		}
	}
	return nil
}

// ValidateTarget reports whether the OU or node a request names exists in the
// account: nil, or the not-found sentinel of the missing one. A read scoped to
// a target resolves it first, so another account's id and an unknown id are
// both a 404 rather than an empty list.
func (s *Service) ValidateTarget(ctx context.Context, accountID uuid.UUID, ouID, nodeID *uuid.UUID) error {
	return s.validateAttachmentTarget(ctx, accountID, ouID, nodeID)
}

// Create creates a secret. Sensitive defaults to true when not specified;
// credential/opaque kinds are always sensitive (enforced in the domain).
// SetMaxSecretsPerAccount wires the per-account secret cap (0 = unlimited).
func (s *Service) SetMaxSecretsPerAccount(max int) { s.maxPerAccount = max }

// assertSecretQuota refuses a NEW secret once the account is at its cap.
//
// It gates creation only, never an update: an account at its ceiling must still
// be able to rotate what it already has, and refusing that would turn a quota
// into an outage on the day someone needs to change a leaked credential.
//
// Worth knowing before tuning the number: node ATTRIBUTES are secrets
// (attr:<nodeID>:<key>), so a fleet with several attributes per node spends this
// budget without anyone visiting the Secrets page.
//
// Fails OPEN on a count error, like every other quota guard here.
func (s *Service) assertSecretQuota(ctx context.Context, accountID uuid.UUID) error {
	if s.maxPerAccount <= 0 {
		return nil
	}
	count, err := s.repo.CountAll(ctx, accountID)
	if err != nil {
		s.logger.WarnContext(ctx, "could not count the account's secrets; allowing the create",
			"error", err, "account_id", accountID)
		return nil
	}
	if count >= int64(s.maxPerAccount) {
		return fmt.Errorf("%w: %d of %d in use. Note that node attributes are stored as secrets and count toward this limit.",
			app_errors.ErrSecretLimitReached, count, s.maxPerAccount)
	}
	return nil
}

func (s *Service) Create(ctx context.Context, cmd secret_create.SecretCreateCommand) (*secret.Secret, error) {
	// The pre-check stays: it refuses early, with the same message, before any
	// work is done. What it cannot do is bind, so the cap is ENFORCED in the
	// write below, under a per-account lock.
	if err := s.assertSecretQuota(ctx, cmd.AuthContext.AccountID); err != nil {
		return nil, err
	}

	if err := secret.ValidateName(cmd.Name); err != nil {
		return nil, app_errors.InvalidRequest("invalid_secret_name", err)
	}
	if err := secret.ValidateKey(cmd.Kind, cmd.Key); err != nil {
		return nil, app_errors.InvalidRequest("invalid_secret_key", err)
	}
	if err := secret.ValidateValue(cmd.Kind, cmd.Value); err != nil {
		return nil, app_errors.InvalidRequest("invalid_secret_value", err)
	}
	sensitive := true
	if cmd.Sensitive != nil {
		sensitive = *cmd.Sensitive
	}
	sec, err := secret.NewSecret(cmd.AuthContext.AccountID, cmd.Name, cmd.Kind, cmd.Key, cmd.Value, sensitive)
	if err != nil {
		return nil, err
	}
	tags, err := secret.NormalizeTags(cmd.Tags)
	if err != nil {
		return nil, err
	}
	sec.Tags = tags
	// Under the cap, atomically. The read-then-write this replaces bounded
	// nothing where it mattered most: the workflow engine drives
	// server.attribute_upsert against up to WORKFLOW_MAX_RUNNING_NODES_PER_ACCOUNT
	// execution nodes CONCURRENTLY, and node attributes are stored as secrets, so
	// a single fan-out had hundreds of goroutines reading the same count before
	// any of them inserted.
	if err := s.repo.CreateUnderCap(ctx, sec, s.maxPerAccount); err != nil {
		s.logger.ErrorContext(ctx, "failed to create secret", "error", err)
		return nil, err
	}
	s.logger.InfoContext(ctx, "secret created", "secret_id", sec.ID, "name", sec.Name, "kind", sec.Kind, "sensitive", sec.Sensitive)
	s.audit.RecordDetail(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionSecretCreate, audit_event.TargetSecret, sec.ID.String(),
		secretDetail(sec))
	return sec, nil
}

// Update mutates name/key/value/sensitivity/group; nil fields are left
// unchanged. The sensitive true->false transition is rejected in the domain.
func (s *Service) Update(ctx context.Context, cmd secret_update.SecretUpdateCommand) (*secret.Secret, error) {
	sec, err := s.repo.GetByID(ctx, cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		return nil, err
	}
	// The read degrades an undecryptable value to empty, and Update writes the
	// whole row back — so renaming or re-tagging such a secret would encrypt ""
	// over bytes that a restored SECRETS_ENCRYPTION_KEY_OLD could still recover.
	// Supplying a value is the repair and stays allowed; everything else is
	// refused.
	if sec.ValueUnreadable && cmd.Value == nil {
		return nil, app_errors.ErrSecretValueUnreadable
	}
	if cmd.Name != nil {
		// Checked only when the name actually changes: a client re-sending
		// the stored name must keep working for a name that predates the
		// rule.
		if *cmd.Name != sec.Name {
			if err := secret.ValidateName(*cmd.Name); err != nil {
				return nil, app_errors.InvalidRequest("invalid_secret_name", err)
			}
		}
		sec.Name = *cmd.Name
	}
	if cmd.Key != nil {
		// Like the name, checked only when it changes: a stored key that
		// predates the rule must not make every other edit of the entry fail.
		if *cmd.Key != sec.Key {
			if err := secret.ValidateKey(sec.Kind, *cmd.Key); err != nil {
				return nil, app_errors.InvalidRequest("invalid_secret_key", err)
			}
		}
		sec.Key = *cmd.Key
	}
	if cmd.Value != nil {
		if err := secret.ValidateValue(sec.Kind, *cmd.Value); err != nil {
			return nil, app_errors.InvalidRequest("invalid_secret_value", err)
		}
		sec.Value = *cmd.Value
		sec.ValueUnreadable = false
	}
	if cmd.Sensitive != nil {
		if err := sec.ChangeSensitivity(*cmd.Sensitive); err != nil {
			return nil, err
		}
	}
	if cmd.Tags != nil {
		tags, err := secret.NormalizeTags(*cmd.Tags)
		if err != nil {
			return nil, err
		}
		sec.Tags = tags
	}
	if err := s.repo.Update(ctx, sec); err != nil {
		s.logger.ErrorContext(ctx, "failed to update secret", "error", err, "secret_id", cmd.ID)
		return nil, err
	}
	// Which fields moved, from the command: "value rotated" and "only the tags
	// changed" are the same stored row afterwards and very different events.
	changed := make([]string, 0, 4)
	if cmd.Name != nil {
		changed = append(changed, fmt.Sprintf("name=%q", *cmd.Name))
	}
	if cmd.Key != nil {
		changed = append(changed, fmt.Sprintf("key=%q", *cmd.Key))
	}
	if cmd.Value != nil {
		// Presence only, forever. This is the value every other layer encrypts.
		changed = append(changed, "value replaced")
	}
	if cmd.Sensitive != nil {
		changed = append(changed, fmt.Sprintf("sensitive=%t", *cmd.Sensitive))
	}
	if cmd.Tags != nil {
		changed = append(changed, fmt.Sprintf("tags=[%s]", strings.Join(sec.Tags, " ")))
	}
	if len(changed) == 0 {
		changed = append(changed, "no field changed")
	}
	s.audit.RecordDetail(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionSecretUpdate, audit_event.TargetSecret, sec.ID.String(),
		fmt.Sprintf("name=%q; %s", sec.Name, strings.Join(changed, ", ")))
	return sec, nil
}

// Delete removes a secret together with its attachments.
func (s *Service) Delete(ctx context.Context, cmd secret_delete.SecretDeleteCommand) error {
	// Read before deleting, purely so the audit row can name what went away: a
	// bare id is unresolvable once the row is gone, and "which secret stopped
	// being delivered to the fleet" is the question asked afterwards. A missing
	// secret is not an error here — Delete below is still the authority on that.
	var doomed *secret.Secret
	if found, err := s.repo.GetByID(ctx, cmd.AuthContext.AccountID, cmd.ID); err == nil {
		doomed = found
	}
	if err := s.repo.Delete(ctx, cmd.AuthContext.AccountID, cmd.ID); err != nil {
		s.logger.ErrorContext(ctx, "failed to delete secret", "error", err, "secret_id", cmd.ID)
		return err
	}
	if err := s.attachmentRepo.DeleteBySecret(ctx, cmd.AuthContext.AccountID, cmd.ID); err != nil {
		s.logger.ErrorContext(ctx, "failed to delete secret attachments", "error", err, "secret_id", cmd.ID)
		return err
	}
	s.audit.RecordDetail(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionSecretDelete, audit_event.TargetSecret, cmd.ID.String(),
		secretDetail(doomed))
	return nil
}

// GetByID fetches one secret.
func (s *Service) GetByID(ctx context.Context, query secret_get.SecretGetQuery) (*secret.Secret, error) {
	return s.repo.GetByID(ctx, query.AuthContext.AccountID, query.ID)
}

// List returns a filtered, sorted page of the account's secrets and the total
// number of matches.
func (s *Service) List(ctx context.Context, query secret_list.SecretListQuery) ([]secret.Secret, int64, error) {
	filter := repository.SecretListFilter{
		Search:     query.Search,
		Kind:       query.Kind,
		Sensitive:  query.Sensitive,
		Tags:       query.Tags,
		OUID:       query.OUID,
		NodeID:     query.NodeID,
		Unattached: query.Unattached,
		Sort:       query.Sort,
		Order:      query.Order,
		Offset:     (query.Page - 1) * query.PerPage,
		Limit:      query.PerPage,
	}
	return s.repo.List(ctx, query.AuthContext.AccountID, filter)
}

// TagFacets returns the account's tag vocabulary with usage counts plus the
// delivery target counts of tags that are attached somewhere.
func (s *Service) TagFacets(ctx context.Context, accountID uuid.UUID) (map[string]int, map[string]secret.AttachmentCounts, error) {
	usage, err := s.repo.TagCounts(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	_, tagAttachments, err := s.attachmentRepo.CountsBySubject(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	return usage, tagAttachments, nil
}

// OUDeliveryCounts returns per-OU delivered-secret counts plus the number of
// fully unattached secrets (for the filter tree).
func (s *Service) OUDeliveryCounts(ctx context.Context, accountID uuid.UUID) (map[uuid.UUID]int, int64, int64, error) {
	counts, err := s.attachmentRepo.DeliveryCountsByOU(ctx, accountID)
	if err != nil {
		return nil, 0, 0, err
	}
	unattached, err := s.repo.CountUnattached(ctx, accountID)
	if err != nil {
		return nil, 0, 0, err
	}
	total, err := s.repo.CountAll(ctx, accountID)
	if err != nil {
		return nil, 0, 0, err
	}
	return counts, unattached, total, nil
}

// AttachmentCounts returns direct-attachment target counts (OUs/nodes) for
// every secret and tag subject in the account, in one query.
func (s *Service) AttachmentCounts(ctx context.Context, accountID uuid.UUID) (map[uuid.UUID]secret.AttachmentCounts, map[string]secret.AttachmentCounts, error) {
	return s.attachmentRepo.CountsBySubject(ctx, accountID)
}

// Attach attaches a secret to exactly one target (an OU or a node). It verifies
// the secret exists in the account before creating the attachment.
func (s *Service) Attach(ctx context.Context, accountID, actorUserID, secretID uuid.UUID, ouID, nodeID *uuid.UUID) (*secret.SecretAttachment, error) {
	if _, err := s.repo.GetByID(ctx, accountID, secretID); err != nil {
		return nil, err
	}
	if err := s.validateAttachmentTarget(ctx, accountID, ouID, nodeID); err != nil {
		return nil, err
	}
	att, err := secret.NewSecretAttachment(accountID, secretID, ouID, nodeID)
	if err != nil {
		return nil, err
	}
	if err := s.attachmentRepo.Attach(ctx, att); err != nil {
		s.logger.ErrorContext(ctx, "failed to attach secret", "error", err, "secret_id", secretID)
		return nil, err
	}
	s.logger.InfoContext(ctx, "secret attached", "secret_id", secretID, "attachment_id", att.ID)
	// An attachment is what makes a secret reach a device, so it is the event that
	// matters operationally — the secret existing changes nothing on its own.
	s.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionSecretAttach, audit_event.TargetSecret, secretID.String(),
		attachmentTargetDetail(ouID, nodeID))
	return att, nil
}

// AttachTag attaches a tag subject to exactly one target (an OU or a node):
// every secret carrying the tag — now or later — is delivered there.
func (s *Service) AttachTag(ctx context.Context, accountID, actorUserID uuid.UUID, tag string, ouID, nodeID *uuid.UUID) (*secret.SecretAttachment, error) {
	if err := s.validateAttachmentTarget(ctx, accountID, ouID, nodeID); err != nil {
		return nil, err
	}
	att, err := secret.NewTagAttachment(accountID, tag, ouID, nodeID)
	if err != nil {
		return nil, err
	}
	if err := s.attachmentRepo.Attach(ctx, att); err != nil {
		s.logger.ErrorContext(ctx, "failed to attach tag", "error", err, "tag", tag)
		return nil, err
	}
	s.logger.InfoContext(ctx, "tag attached", "tag", att.Tag, "attachment_id", att.ID)
	// A tag attachment has DYNAMIC membership: it also delivers secrets tagged
	// later. That makes it a standing grant rather than a one-time wiring, which
	// is why the target of the row is the tag and not any particular secret.
	s.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionSecretTagAttach, audit_event.TargetSecretTag, att.Tag,
		attachmentTargetDetail(ouID, nodeID))
	return att, nil
}

// ListTagAttachments lists a tag's attachments.
func (s *Service) ListTagAttachments(ctx context.Context, accountID uuid.UUID, tag string) ([]secret.SecretAttachment, error) {
	return s.attachmentRepo.ListByTag(ctx, accountID, tag)
}

// ReplaceAttachmentsForTarget declaratively replaces the direct attachments of
// one target (an OU or a node): subjects listed are attached (idempotently),
// subjects missing from the lists are detached. Unknown subject IDs fail the
// whole call so a UI race cannot silently drop entries.
func (s *Service) ReplaceAttachmentsForTarget(ctx context.Context, accountID, actorUserID uuid.UUID, ouID, nodeID *uuid.UUID, secretIDs []uuid.UUID, tagList []string) ([]secret.SecretAttachment, error) {
	if (ouID == nil) == (nodeID == nil) {
		return nil, secret.ErrInvalidAttachmentTarget
	}
	if err := s.validateAttachmentTarget(ctx, accountID, ouID, nodeID); err != nil {
		return nil, err
	}

	// Verify every requested secret exists; normalize the tags.
	foundSecrets, err := s.repo.GetByIDs(ctx, accountID, secretIDs)
	if err != nil {
		return nil, err
	}
	if len(foundSecrets) != len(uniqueIDs(secretIDs)) {
		return nil, app_errors.ErrSecretNotFound
	}
	normalizedTags, err := secret.NormalizeTags(tagList)
	if err != nil {
		return nil, err
	}

	current, err := s.listAttachmentsForTarget(ctx, accountID, ouID, nodeID)
	if err != nil {
		return nil, err
	}

	wantSecrets := idSet(secretIDs)
	wantTags := make(map[string]bool, len(normalizedTags))
	for _, tag := range normalizedTags {
		wantTags[tag] = true
	}

	// Detach subjects that are no longer listed.
	for i := range current {
		att := &current[i]
		keep := (att.SecretID != nil && wantSecrets[*att.SecretID]) ||
			(att.Tag != "" && wantTags[att.Tag])
		if keep {
			// Already attached — no need to re-attach below.
			if att.SecretID != nil {
				delete(wantSecrets, *att.SecretID)
			}
			if att.Tag != "" {
				delete(wantTags, att.Tag)
			}
			continue
		}
		if err := s.attachmentRepo.Detach(ctx, accountID, att.ID); err != nil {
			return nil, err
		}
	}

	// Attach the new subjects.
	for id := range wantSecrets {
		att, err := secret.NewSecretAttachment(accountID, id, ouID, nodeID)
		if err != nil {
			return nil, err
		}
		if err := s.attachmentRepo.Attach(ctx, att); err != nil {
			return nil, err
		}
	}
	for tag := range wantTags {
		att, err := secret.NewTagAttachment(accountID, tag, ouID, nodeID)
		if err != nil {
			return nil, err
		}
		if err := s.attachmentRepo.Attach(ctx, att); err != nil {
			return nil, err
		}
	}

	// One row for the whole declarative replace, targeted at the OU or node whose
	// delivered set changed. Per-subject rows would be more granular and less
	// useful: the operator performed one action, and the counts say how big it was.
	s.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionSecretAttachmentsReplace, attachmentTargetType(ouID),
		attachmentTargetID(ouID, nodeID),
		fmt.Sprintf("secrets=%d tags=[%s]", len(uniqueIDs(secretIDs)), strings.Join(normalizedTags, " ")))

	return s.listAttachmentsForTarget(ctx, accountID, ouID, nodeID)
}

// ListAttachmentsForTarget lists the direct attachments (secrets and tags) of
// exactly one target, resolved in the caller's account first. It is the read
// an editor needs before changing one attachment: the effective set cannot
// stand in for it, because a tag no secret carries yet delivers nothing and so
// never appears there.
func (s *Service) ListAttachmentsForTarget(ctx context.Context, accountID uuid.UUID, ouID, nodeID *uuid.UUID) ([]secret.SecretAttachment, error) {
	if (ouID == nil) == (nodeID == nil) {
		return nil, secret.ErrInvalidAttachmentTarget
	}
	if err := s.validateAttachmentTarget(ctx, accountID, ouID, nodeID); err != nil {
		return nil, err
	}
	return s.listAttachmentsForTarget(ctx, accountID, ouID, nodeID)
}

// listAttachmentsForTarget lists direct attachments of an OU or a node.
func (s *Service) listAttachmentsForTarget(ctx context.Context, accountID uuid.UUID, ouID, nodeID *uuid.UUID) ([]secret.SecretAttachment, error) {
	if ouID != nil {
		return s.attachmentRepo.ListByOU(ctx, accountID, *ouID)
	}
	return s.attachmentRepo.ListByNode(ctx, accountID, *nodeID)
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func idSet(ids []uuid.UUID) map[uuid.UUID]bool {
	set := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// Detach removes an attachment by ID.
func (s *Service) Detach(ctx context.Context, accountID, actorUserID, attachmentID uuid.UUID) error {
	if err := s.attachmentRepo.Detach(ctx, accountID, attachmentID); err != nil {
		s.logger.ErrorContext(ctx, "failed to detach secret", "error", err, "attachment_id", attachmentID)
		return err
	}
	// The attachment id is all the caller gave us and the row is gone, so that is
	// what the trail carries. It still answers "when did this stop being
	// delivered, and who decided that" — the pairing attach row names the subject.
	s.audit.Record(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionSecretDetach, audit_event.TargetSecret, attachmentID.String())
	return nil
}

// ListAttachments lists a secret's attachments (verifying the secret exists).
func (s *Service) ListAttachments(ctx context.Context, accountID, secretID uuid.UUID) ([]secret.SecretAttachment, error) {
	if _, err := s.repo.GetByID(ctx, accountID, secretID); err != nil {
		return nil, err
	}
	return s.attachmentRepo.ListBySecret(ctx, accountID, secretID)
}

// ListByOU returns the secrets attached to an OU (values are never returned to
// callers; the handler maps to value-free DTOs).
func (s *Service) ListByOU(ctx context.Context, accountID, ouID uuid.UUID) ([]secret.Secret, error) {
	atts, err := s.attachmentRepo.ListByOU(ctx, accountID, ouID)
	if err != nil {
		return nil, err
	}
	return s.loadSecretsForAttachments(ctx, accountID, atts)
}

// ListByNode returns the secrets attached directly to a node.
func (s *Service) ListByNode(ctx context.Context, accountID, nodeID uuid.UUID) ([]secret.Secret, error) {
	atts, err := s.attachmentRepo.ListByNode(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	return s.loadSecretsForAttachments(ctx, accountID, atts)
}

// UpsertForNode creates or updates an account secret by name and ensures it is
// attached to the node. Used by the workflow secret.upsert block to capture a
// value (e.g. an externally-issued token) for reuse in later runs.
//
// The write is bound to the run's node, because the right that reaches it is
// the right to run a playbook on that node, not secret management (which is
// account-wide). So it may create a secret, or update one that is delivered to
// this node ALONE — directly attached here and nowhere else, and carrying no
// tag that is attached elsewhere — and nothing more. Names are unique per
// account, so a name taken by a wider secret is refused rather than answered
// with a node-local duplicate: there is no second name to create, and a
// silent fallback would leave the run reading a value nobody wrote. The
// reserved node-attribute names are refused like on every operator path; the
// attribute block writes the run node's own attributes.
func (s *Service) UpsertForNode(ctx context.Context, actor audit.Actor, accountID, nodeID uuid.UUID, name string, kind secret.SecretKind, key, value string, opts secret.UpsertOptions) error {
	name = strings.TrimSpace(name)
	if err := secret.ValidateName(name); err != nil {
		return app_errors.InvalidRequest("invalid_secret_name", err)
	}
	if err := secret.ValidateKey(kind, key); err != nil {
		return app_errors.InvalidRequest("invalid_secret_key", err)
	}
	if err := secret.ValidateValue(kind, value); err != nil {
		return app_errors.InvalidRequest("invalid_secret_value", err)
	}
	if err := s.validateAttachmentTarget(ctx, accountID, nil, &nodeID); err != nil {
		return err
	}
	newTags, err := secret.NormalizeTags(opts.Tags)
	if err != nil {
		return err
	}
	found, err := s.repo.GetByName(ctx, accountID, name)
	if err != nil && !errors.Is(err, app_errors.ErrSecretNotFound) {
		return err
	}
	if err := s.assertDeliveredOnlyTo(ctx, accountID, nodeID, name, found, newTags); err != nil {
		return err
	}

	// Only a genuinely NEW row spends quota; an upsert onto an existing secret
	// is a rotation and must keep working at the ceiling.
	if found == nil {
		if err := s.assertSecretQuota(ctx, accountID); err != nil {
			return err
		}
	}

	var secretID uuid.UUID
	if found != nil {
		found.Kind = kind
		found.Key = key
		found.Value = value
		// Never desensitize an existing entry; promoting to sensitive is fine.
		if opts.Sensitive != nil && *opts.Sensitive && !found.Sensitive {
			found.Sensitive = true
		}
		merged, err := secret.MergeTags(found.Tags, opts.Tags)
		if err != nil {
			return err
		}
		found.Tags = merged
		if err := s.repo.Update(ctx, found); err != nil {
			return err
		}
		secretID = found.ID
	} else {
		sensitive := true
		if opts.Sensitive != nil {
			sensitive = *opts.Sensitive
		}
		sec, err := secret.NewSecret(accountID, name, kind, key, value, sensitive)
		if err != nil {
			return err
		}
		sec.Tags = newTags
		// Under the cap, atomically, as Create does: the engine runs this block
		// on many nodes at once, and each of them passed the pre-check above
		// against the same count.
		if err := s.repo.CreateUnderCap(ctx, sec, s.maxPerAccount); err != nil {
			return err
		}
		secretID = sec.ID
	}

	// Ensure a node attachment exists (the repository attach is idempotent).
	att, err := secret.NewSecretAttachment(accountID, secretID, nil, &nodeID)
	if err != nil {
		return err
	}
	if err := s.attachmentRepo.Attach(ctx, att); err != nil {
		return err
	}
	// The actor is passed in rather than assumed: the engine names the member
	// the run acts for, and the run id says which execution wrote it.
	detail := fmt.Sprintf("name=%q kind=%s for node %s", name, kind, nodeID)
	if opts.RunID != uuid.Nil {
		detail += fmt.Sprintf(" by playbook run %s", opts.RunID)
	}
	s.audit.RecordDetail(ctx, actor,
		audit_event.ActionSecretUpsert, audit_event.TargetSecret, secretID.String(), detail)
	return nil
}

// assertDeliveredOnlyTo refuses a playbook write that would change what any
// target other than nodeID receives: an existing secret must be attached to
// nodeID and nowhere else (an unattached one is account inventory, not this
// node's), and no tag it carries or gains may be attached to another target.
func (s *Service) assertDeliveredOnlyTo(ctx context.Context, accountID, nodeID uuid.UUID, name string, found *secret.Secret, newTags []string) error {
	refuse := func(why string) error {
		return fmt.Errorf("%w: secret %q %s; use a per-node name or the Set node attribute block",
			app_errors.ErrSecretOutsideRunNode, name, why)
	}
	onlyThisNode := func(atts []secret.SecretAttachment) bool {
		for i := range atts {
			if atts[i].OUID != nil || atts[i].NodeID == nil || *atts[i].NodeID != nodeID {
				return false
			}
		}
		return true
	}
	tags := newTags
	if found != nil {
		atts, err := s.attachmentRepo.ListBySecret(ctx, accountID, found.ID)
		if err != nil {
			return err
		}
		if len(atts) == 0 {
			return refuse("already exists and is not attached to this node")
		}
		if !onlyThisNode(atts) {
			return refuse("is delivered beyond this node")
		}
		tags = append(append([]string{}, found.Tags...), newTags...)
	}
	for _, tag := range tags {
		atts, err := s.attachmentRepo.ListByTag(ctx, accountID, tag)
		if err != nil {
			return err
		}
		if !onlyThisNode(atts) {
			return refuse(fmt.Sprintf("would be delivered beyond this node through tag %q", tag))
		}
	}
	return nil
}

// ListNodeAttributes returns the AD-style custom attributes of a node: its
// directly-attached attribute-backing secrets, projected to key/value/sensitive.
func (s *Service) ListNodeAttributes(ctx context.Context, accountID, nodeID uuid.UUID) ([]secret.NodeAttribute, error) {
	secrets, err := s.ListByNode(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	return secret.NodeAttributesFromSecrets(nodeID, secrets), nil
}

// UpsertNodeAttribute creates or updates one custom attribute of a node. It is
// backed by an env-kind secret named attr:<nodeID>:<key> attached to the node,
// so the value lands in the node's agent env (keyed by the attribute key) and
// resolves as {{node.attributes.<key>}} in playbooks. Enforces one attribute
// per (node, key); the sensitive true->false transition is rejected in the
// domain (re-create instead).
func (s *Service) UpsertNodeAttribute(ctx context.Context, actor audit.Actor, accountID, nodeID uuid.UUID, key, value string, sensitive bool) (*secret.NodeAttribute, error) {
	if err := secret.ValidateAttributeKey(key); err != nil {
		return nil, err
	}
	// An attribute is an env-kind secret delivered into the node's env file:
	// a line break in it would fail every provisioning of this node.
	if err := secret.ValidateValue(secret.KindEnv, value); err != nil {
		return nil, app_errors.InvalidRequest("invalid_attribute_value", err)
	}
	if err := s.validateAttachmentTarget(ctx, accountID, nil, &nodeID); err != nil {
		return nil, err
	}
	name := secret.NodeAttributeName(nodeID, key)

	found, err := s.repo.GetByName(ctx, accountID, name)
	if err != nil && !errors.Is(err, app_errors.ErrSecretNotFound) {
		return nil, err
	}

	// An attribute IS a secret, so it spends the same budget — and only when it
	// is new. Editing an existing attribute keeps working at the ceiling.
	if found == nil {
		if err := s.assertSecretQuota(ctx, accountID); err != nil {
			return nil, err
		}
	}

	var secretID uuid.UUID
	if found != nil {
		found.Key = key
		found.Value = value
		if found.Sensitive != sensitive {
			if err := found.ChangeSensitivity(sensitive); err != nil {
				return nil, err
			}
		}
		if err := s.repo.Update(ctx, found); err != nil {
			s.logger.ErrorContext(ctx, "failed to update node attribute", "error", err, "node_id", nodeID, "key", key)
			return nil, err
		}
		secretID = found.ID
	} else {
		sec, err := secret.NewSecret(accountID, name, secret.KindEnv, key, value, sensitive)
		if err != nil {
			return nil, err
		}
		// Under the cap, atomically — the attribute block fans out like the
		// secret block does.
		if err := s.repo.CreateUnderCap(ctx, sec, s.maxPerAccount); err != nil {
			s.logger.ErrorContext(ctx, "failed to create node attribute", "error", err, "node_id", nodeID, "key", key)
			return nil, err
		}
		secretID = sec.ID
	}

	// Ensure the node attachment exists (the repository attach is idempotent).
	att, err := secret.NewSecretAttachment(accountID, secretID, nil, &nodeID)
	if err != nil {
		return nil, err
	}
	if err := s.attachmentRepo.Attach(ctx, att); err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "node attribute upserted", "node_id", nodeID, "key", key, "sensitive", sensitive)
	// Targeted at the NODE, not the backing secret: an operator asking "who
	// changed this node's CI token" is not thinking about the secrets table, and
	// the attribute key is the name they will search for. The value is never here.
	s.audit.RecordDetail(ctx, actor,
		audit_event.ActionNodeAttributeSet, audit_event.TargetNode, nodeID.String(),
		fmt.Sprintf("key=%q sensitive=%t", key, sensitive))
	return &secret.NodeAttribute{SecretID: secretID, Key: key, Value: value, Sensitive: sensitive}, nil
}

// DeleteNodeAttribute removes one custom attribute of a node (the backing
// secret and its attachments). Returns ErrSecretNotFound if the node has no
// attribute with that key.
func (s *Service) DeleteNodeAttribute(ctx context.Context, actor audit.Actor, accountID, nodeID uuid.UUID, key string) error {
	name := secret.NodeAttributeName(nodeID, key)
	found, err := s.repo.GetByName(ctx, accountID, name)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, accountID, found.ID); err != nil {
		return err
	}
	if err := s.attachmentRepo.DeleteBySecret(ctx, accountID, found.ID); err != nil {
		return err
	}
	s.audit.RecordDetail(ctx, actor,
		audit_event.ActionNodeAttributeDelete, audit_event.TargetNode, nodeID.String(),
		fmt.Sprintf("key=%q", key))
	return nil
}

// loadSecretsForAttachments loads the secrets referenced by secret-subject
// attachments in one query; group-subject attachments and attachments whose
// secret no longer exists are skipped.
func (s *Service) loadSecretsForAttachments(ctx context.Context, accountID uuid.UUID, atts []secret.SecretAttachment) ([]secret.Secret, error) {
	ids := make([]uuid.UUID, 0, len(atts))
	for i := range atts {
		if atts[i].SecretID != nil {
			ids = append(ids, *atts[i].SecretID)
		}
	}
	return s.repo.GetByIDs(ctx, accountID, ids)
}

// --- audit detail helpers -------------------------------------------------
//
// A secret's NAME and KIND are identifiers — the name is the
// {{secrets.<name>}} reference operators write in playbooks — and belong in the
// trail. Its VALUE never does: it is encrypted at rest under
// SECRETS_ENCRYPTION_KEY, and a copy sitting in audit_events would be a
// plaintext leak that no rotation and no rewrap list would ever find.

// secretDetail names a secret without revealing it. Tolerates nil, because the
// delete path records from a best-effort pre-read.
func secretDetail(sec *secret.Secret) string {
	if sec == nil {
		return ""
	}
	detail := fmt.Sprintf("name=%q kind=%s sensitive=%t", sec.Name, sec.Kind, sec.Sensitive)
	if len(sec.Tags) > 0 {
		detail += " tags=[" + strings.Join(sec.Tags, " ") + "]"
	}
	return detail
}

// attachmentTargetDetail says where a subject was attached. Exactly one of the
// two is set (the domain enforces it); an "unknown target" reading means that
// invariant broke and is worth seeing rather than hiding.
func attachmentTargetDetail(ouID, nodeID *uuid.UUID) string {
	switch {
	case nodeID != nil:
		return "-> node " + nodeID.String()
	case ouID != nil:
		return "-> ou " + ouID.String()
	default:
		return "-> unknown target"
	}
}

// attachmentTargetType / attachmentTargetID point a replace row at the thing
// whose delivered set changed, so the row is found by looking up that node or OU
// rather than by knowing which secrets happened to be involved.
func attachmentTargetType(ouID *uuid.UUID) string {
	if ouID != nil {
		return audit_event.TargetOU
	}
	return audit_event.TargetNode
}

func attachmentTargetID(ouID, nodeID *uuid.UUID) string {
	if ouID != nil {
		return ouID.String()
	}
	if nodeID != nil {
		return nodeID.String()
	}
	return ""
}
