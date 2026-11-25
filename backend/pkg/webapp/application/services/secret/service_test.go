package secret

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_delete"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

// fakeSecretRepo is an in-memory SecretRepository for service tests.
type fakeSecretRepo struct {
	// The quota test drives Create from many goroutines, exactly as the workflow
	// engine's attribute_upsert fan-out does. Without the lock the fake's own map
	// races and the test measures Go's map, not the cap.
	mu   sync.Mutex
	byID map[uuid.UUID]secret.Secret
	// countGate runs at the START of CountAll, which is the service's pre-check.
	// The quota race test uses it as a barrier so every caller has read the count
	// before any of them writes — the interleaving the fan-out actually produces,
	// and one a scheduler will not reliably give you.
	countGate func()
}

var _ repository.SecretRepository = (*fakeSecretRepo)(nil)

func newFakeSecretRepo() *fakeSecretRepo {
	return &fakeSecretRepo{byID: make(map[uuid.UUID]secret.Secret)}
}

func (f *fakeSecretRepo) Create(_ context.Context, s *secret.Secret) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createLocked(s)
}

func (f *fakeSecretRepo) createLocked(s *secret.Secret) error {
	for _, existing := range f.byID {
		if existing.AccountID == s.AccountID && existing.Name == s.Name {
			return app_errors.ErrSecretNameTaken
		}
	}
	f.byID[s.ID] = *s
	return nil
}

// CreateUnderCap mirrors the SQL: the count and the insert happen together, and
// the cap is enforced HERE rather than by whatever the caller checked earlier. A
// fake that just delegated to Create would let the cap regress to a read-then-
// write without any test noticing.
func (f *fakeSecretRepo) CreateUnderCap(_ context.Context, s *secret.Secret, maxPerAccount int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if maxPerAccount > 0 {
		count := 0
		for _, existing := range f.byID {
			if existing.AccountID == s.AccountID {
				count++
			}
		}
		if count >= maxPerAccount {
			return app_errors.ErrSecretLimitReached
		}
	}
	return f.createLocked(s)
}

func (f *fakeSecretRepo) Update(_ context.Context, s *secret.Secret) error {
	if _, ok := f.byID[s.ID]; !ok {
		return app_errors.ErrSecretNotFound
	}
	f.byID[s.ID] = *s
	return nil
}

func (f *fakeSecretRepo) GetByID(_ context.Context, accountID, id uuid.UUID) (*secret.Secret, error) {
	s, ok := f.byID[id]
	if !ok || s.AccountID != accountID {
		return nil, app_errors.ErrSecretNotFound
	}
	out := s
	return &out, nil
}

func (f *fakeSecretRepo) GetByIDs(_ context.Context, accountID uuid.UUID, ids []uuid.UUID) ([]secret.Secret, error) {
	var out []secret.Secret
	for _, id := range ids {
		if s, ok := f.byID[id]; ok && s.AccountID == accountID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeSecretRepo) GetByName(_ context.Context, accountID uuid.UUID, name string) (*secret.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.byID {
		if s.AccountID == accountID && s.Name == name {
			out := s
			return &out, nil
		}
	}
	return nil, app_errors.ErrSecretNotFound
}

func (f *fakeSecretRepo) GetAllByAccountID(_ context.Context, accountID uuid.UUID) ([]secret.Secret, error) {
	var out []secret.Secret
	for _, s := range f.byID {
		if s.AccountID == accountID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeSecretRepo) List(_ context.Context, accountID uuid.UUID, filter repository.SecretListFilter) ([]secret.Secret, int64, error) {
	var out []secret.Secret
	for _, s := range f.byID {
		if s.AccountID == accountID {
			out = append(out, s)
		}
	}
	return out, int64(len(out)), nil
}

func (f *fakeSecretRepo) TagCounts(_ context.Context, accountID uuid.UUID) (map[string]int, error) {
	counts := make(map[string]int)
	for _, s := range f.byID {
		if s.AccountID != accountID {
			continue
		}
		for _, tag := range s.Tags {
			counts[tag]++
		}
	}
	return counts, nil
}

func (f *fakeSecretRepo) CountUnattached(_ context.Context, _ uuid.UUID) (int64, error) {
	return 0, nil
}

func (f *fakeSecretRepo) CountAll(_ context.Context, accountID uuid.UUID) (int64, error) {
	f.mu.Lock()
	var count int64
	for _, s := range f.byID {
		if s.AccountID == accountID {
			count++
		}
	}
	f.mu.Unlock()
	// AFTER the count is taken, not before. Gating on arrival only synchronises
	// entry: the callers then contend for the lock one at a time and the late
	// ones read a count that already includes the early ones' inserts, which is
	// exactly the interleaving the test must NOT depend on.
	if f.countGate != nil {
		f.countGate()
	}
	return count, nil
}

func (f *fakeSecretRepo) GetByTags(_ context.Context, accountID uuid.UUID, tagList []string) ([]secret.Secret, error) {
	var out []secret.Secret
	for _, s := range f.byID {
		if s.AccountID != accountID {
			continue
		}
		for _, want := range tagList {
			carried := false
			for _, tag := range s.Tags {
				if tag == want {
					carried = true
					break
				}
			}
			if carried {
				out = append(out, s)
				break
			}
		}
	}
	return out, nil
}

func (f *fakeSecretRepo) TouchLastUsed(_ context.Context, _ uuid.UUID, _ []uuid.UUID) error {
	return nil
}

func (f *fakeSecretRepo) Delete(_ context.Context, accountID, id uuid.UUID) error {
	s, ok := f.byID[id]
	if !ok || s.AccountID != accountID {
		return app_errors.ErrSecretNotFound
	}
	delete(f.byID, id)
	return nil
}

func (f *fakeSecretRepo) DeleteNodeAttributes(_ context.Context, accountID, nodeID uuid.UUID) (int64, error) {
	prefix := secret.NodeAttributeNamePrefix(nodeID)
	var deleted int64
	for id, s := range f.byID {
		if s.AccountID == accountID && strings.HasPrefix(s.Name, prefix) {
			delete(f.byID, id)
			deleted++
		}
	}
	return deleted, nil
}

// fakeAttachmentRepo is an in-memory SecretAttachmentRepository.
type fakeAttachmentRepo struct {
	atts []secret.SecretAttachment
}

var _ repository.SecretAttachmentRepository = (*fakeAttachmentRepo)(nil)

func (f *fakeAttachmentRepo) Attach(_ context.Context, att *secret.SecretAttachment) error {
	for i := range f.atts {
		if eqPtr(f.atts[i].SecretID, att.SecretID) && f.atts[i].Tag == att.Tag &&
			eqPtr(f.atts[i].OUID, att.OUID) && eqPtr(f.atts[i].NodeID, att.NodeID) {
			*att = f.atts[i] // idempotent, mirror the SQL implementation
			return nil
		}
	}
	f.atts = append(f.atts, *att)
	return nil
}

func (f *fakeAttachmentRepo) Detach(_ context.Context, accountID, attachmentID uuid.UUID) error {
	for i := range f.atts {
		if f.atts[i].ID == attachmentID && f.atts[i].AccountID == accountID {
			f.atts = append(f.atts[:i], f.atts[i+1:]...)
			return nil
		}
	}
	return app_errors.ErrSecretAttachmentNotFound
}

func (f *fakeAttachmentRepo) ListBySecret(_ context.Context, accountID, secretID uuid.UUID) ([]secret.SecretAttachment, error) {
	var out []secret.SecretAttachment
	for i := range f.atts {
		if f.atts[i].AccountID == accountID && f.atts[i].SecretID != nil && *f.atts[i].SecretID == secretID {
			out = append(out, f.atts[i])
		}
	}
	return out, nil
}

func (f *fakeAttachmentRepo) ListByTag(_ context.Context, accountID uuid.UUID, tag string) ([]secret.SecretAttachment, error) {
	var out []secret.SecretAttachment
	for i := range f.atts {
		if f.atts[i].AccountID == accountID && f.atts[i].Tag == tag {
			out = append(out, f.atts[i])
		}
	}
	return out, nil
}

func (f *fakeAttachmentRepo) ListByOU(_ context.Context, accountID, ouID uuid.UUID) ([]secret.SecretAttachment, error) {
	var out []secret.SecretAttachment
	for i := range f.atts {
		if f.atts[i].AccountID == accountID && f.atts[i].OUID != nil && *f.atts[i].OUID == ouID {
			out = append(out, f.atts[i])
		}
	}
	return out, nil
}

func (f *fakeAttachmentRepo) ListByNode(_ context.Context, accountID, nodeID uuid.UUID) ([]secret.SecretAttachment, error) {
	var out []secret.SecretAttachment
	for i := range f.atts {
		if f.atts[i].AccountID == accountID && f.atts[i].NodeID != nil && *f.atts[i].NodeID == nodeID {
			out = append(out, f.atts[i])
		}
	}
	return out, nil
}

func (f *fakeAttachmentRepo) ListByOUs(_ context.Context, accountID uuid.UUID, ouIDs []uuid.UUID) ([]secret.SecretAttachment, error) {
	var out []secret.SecretAttachment
	for _, id := range ouIDs {
		atts, _ := f.ListByOU(context.Background(), accountID, id)
		out = append(out, atts...)
	}
	return out, nil
}

func (f *fakeAttachmentRepo) DeliveryCountsByOU(_ context.Context, _ uuid.UUID) (map[uuid.UUID]int, error) {
	return map[uuid.UUID]int{}, nil
}

func (f *fakeAttachmentRepo) CountsBySubject(_ context.Context, accountID uuid.UUID) (map[uuid.UUID]secret.AttachmentCounts, map[string]secret.AttachmentCounts, error) {
	secretCounts := make(map[uuid.UUID]secret.AttachmentCounts)
	tagCounts := make(map[string]secret.AttachmentCounts)
	for i := range f.atts {
		if f.atts[i].AccountID != accountID {
			continue
		}
		bump := func(c secret.AttachmentCounts) secret.AttachmentCounts {
			if f.atts[i].OUID != nil {
				c.OUs++
			}
			if f.atts[i].NodeID != nil {
				c.Nodes++
			}
			return c
		}
		switch {
		case f.atts[i].SecretID != nil:
			secretCounts[*f.atts[i].SecretID] = bump(secretCounts[*f.atts[i].SecretID])
		case f.atts[i].Tag != "":
			tagCounts[f.atts[i].Tag] = bump(tagCounts[f.atts[i].Tag])
		}
	}
	return secretCounts, tagCounts, nil
}

func (f *fakeAttachmentRepo) DeleteBySecret(_ context.Context, accountID, secretID uuid.UUID) error {
	kept := f.atts[:0]
	for i := range f.atts {
		if !(f.atts[i].AccountID == accountID && f.atts[i].SecretID != nil && *f.atts[i].SecretID == secretID) {
			kept = append(kept, f.atts[i])
		}
	}
	f.atts = kept
	return nil
}

func (f *fakeAttachmentRepo) DeleteByTag(_ context.Context, accountID uuid.UUID, tag string) error {
	kept := f.atts[:0]
	for i := range f.atts {
		if !(f.atts[i].AccountID == accountID && f.atts[i].Tag == tag) {
			kept = append(kept, f.atts[i])
		}
	}
	f.atts = kept
	return nil
}

func (f *fakeAttachmentRepo) DeleteByOUID(_ context.Context, accountID, ouID uuid.UUID) error {
	kept := f.atts[:0]
	for i := range f.atts {
		if !(f.atts[i].AccountID == accountID && f.atts[i].OUID != nil && *f.atts[i].OUID == ouID) {
			kept = append(kept, f.atts[i])
		}
	}
	f.atts = kept
	return nil
}

func (f *fakeAttachmentRepo) DeleteByNodeID(_ context.Context, accountID, nodeID uuid.UUID) error {
	kept := f.atts[:0]
	for i := range f.atts {
		if !(f.atts[i].AccountID == accountID && f.atts[i].NodeID != nil && *f.atts[i].NodeID == nodeID) {
			kept = append(kept, f.atts[i])
		}
	}
	f.atts = kept
	return nil
}

func eqPtr(a, b *uuid.UUID) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}

// fakeNodeRepoForSecret / fakeOURepoForSecret satisfy the ownership check by
// treating every target as owned by the caller's account (these tests exercise
// secret/attachment logic, not cross-tenant target rejection).
type fakeNodeRepoForSecret struct{ repository.NodeRepository }

func (fakeNodeRepoForSecret) GetByID(_ context.Context, accountID, nodeID uuid.UUID) (*node.Node, error) {
	return &node.Node{ID: nodeID, AccountID: accountID}, nil
}

type fakeOURepoForSecret struct {
	repository.OrganizationalUnitRepository
}

func (fakeOURepoForSecret) GetByID(_ context.Context, accountID, id uuid.UUID) (*organizational_unit.OrganizationalUnit, error) {
	return &organizational_unit.OrganizationalUnit{ID: id, AccountID: accountID}, nil
}

// rejectingNodeRepo models a node that does not belong to the caller's account
// (account-scoped GetByID returns not-found).
type rejectingNodeRepo struct{ repository.NodeRepository }

func (rejectingNodeRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	return nil, app_errors.ErrNodeNotFound
}

func TestAttachRejectsForeignTarget(t *testing.T) {
	svc := NewService(newFakeSecretRepo(), &fakeAttachmentRepo{}, rejectingNodeRepo{}, fakeOURepoForSecret{}, mustRecorder(), slog.Default())
	nodeID := uuid.New()
	if _, err := svc.AttachTag(context.Background(), uuid.New(), uuid.New(), "team", nil, &nodeID); !errors.Is(err, app_errors.ErrNodeNotFound) {
		t.Fatalf("got %v, want ErrNodeNotFound for a target node in another account", err)
	}
}

func newTestService() (*Service, *fakeSecretRepo, *fakeAttachmentRepo) {
	repo := newFakeSecretRepo()
	atts := &fakeAttachmentRepo{}
	recorder, _ := audittest.New()
	return NewService(repo, atts, fakeNodeRepoForSecret{}, fakeOURepoForSecret{}, recorder, slog.Default()), repo, atts
}

func TestUpsertForNodeCreatesAndAttaches(t *testing.T) {
	svc, repo, atts := newTestService()
	accountID, nodeID := uuid.New(), uuid.New()

	if err := svc.UpsertForNode(context.Background(), audit.SystemActor(accountID), accountID, nodeID, "api-token", secret.KindOpaque, "", "tok-1", secret.UpsertOptions{}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	created, err := repo.GetByName(context.Background(), accountID, "api-token")
	if err != nil {
		t.Fatalf("expected secret created, got %v", err)
	}
	if created.Value != "tok-1" {
		t.Errorf("expected value tok-1, got %q", created.Value)
	}
	nodeAtts, _ := atts.ListByNode(context.Background(), accountID, nodeID)
	if len(nodeAtts) != 1 {
		t.Fatalf("expected 1 node attachment, got %d", len(nodeAtts))
	}
}

func TestUpsertForNodeUpdatesExistingByNameAndStaysIdempotent(t *testing.T) {
	svc, repo, atts := newTestService()
	accountID, nodeID := uuid.New(), uuid.New()

	if err := svc.UpsertForNode(context.Background(), audit.SystemActor(accountID), accountID, nodeID, "api-token", secret.KindOpaque, "", "tok-1", secret.UpsertOptions{}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := svc.UpsertForNode(context.Background(), audit.SystemActor(accountID), accountID, nodeID, "api-token", secret.KindOpaque, "", "tok-2", secret.UpsertOptions{}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	all, _ := repo.GetAllByAccountID(context.Background(), accountID)
	if len(all) != 1 {
		t.Fatalf("expected a single secret after re-upsert, got %d", len(all))
	}
	if all[0].Value != "tok-2" {
		t.Errorf("expected updated value tok-2, got %q", all[0].Value)
	}
	nodeAtts, _ := atts.ListByNode(context.Background(), accountID, nodeID)
	if len(nodeAtts) != 1 {
		t.Fatalf("expected attachment to stay unique, got %d", len(nodeAtts))
	}
}

func TestListByNodeSkipsDeletedSecrets(t *testing.T) {
	svc, repo, _ := newTestService()
	accountID, nodeID := uuid.New(), uuid.New()

	if err := svc.UpsertForNode(context.Background(), audit.SystemActor(accountID), accountID, nodeID, "a", secret.KindOpaque, "", "1", secret.UpsertOptions{}); err != nil {
		t.Fatalf("upsert a: %v", err)
	}
	if err := svc.UpsertForNode(context.Background(), audit.SystemActor(accountID), accountID, nodeID, "b", secret.KindOpaque, "", "2", secret.UpsertOptions{}); err != nil {
		t.Fatalf("upsert b: %v", err)
	}
	// Delete one secret; its attachment row remains but must be skipped.
	sec, _ := repo.GetByName(context.Background(), accountID, "a")
	if err := repo.Delete(context.Background(), accountID, sec.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	secrets, err := svc.ListByNode(context.Background(), accountID, nodeID)
	if err != nil {
		t.Fatalf("list by node: %v", err)
	}
	if len(secrets) != 1 || secrets[0].Name != "b" {
		t.Fatalf("expected only secret b, got %v", secrets)
	}
}

func TestUpdateRejectsDesensitize(t *testing.T) {
	svc, _, _ := newTestService()
	auth := commands.NewAuthContext(uuid.New(), uuid.New())

	created, err := svc.Create(context.Background(), secret_create.SecretCreateCommand{
		ID: uuid.New(), Name: "token", Kind: secret.KindEnv, Key: "TOKEN", Value: "v", AuthContext: auth,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !created.Sensitive {
		t.Fatal("expected sensitive to default to true")
	}

	insensitive := false
	if _, err := svc.Update(context.Background(), secret_update.SecretUpdateCommand{
		ID: created.ID, Sensitive: &insensitive, AuthContext: auth,
	}); !errors.Is(err, secret.ErrCannotDesensitize) {
		t.Fatalf("expected ErrCannotDesensitize, got %v", err)
	}
}

func TestVariableCanBecomeSensitiveButNotBack(t *testing.T) {
	svc, _, _ := newTestService()
	auth := commands.NewAuthContext(uuid.New(), uuid.New())

	insensitive := false
	created, err := svc.Create(context.Background(), secret_create.SecretCreateCommand{
		ID: uuid.New(), Name: "host", Kind: secret.KindEnv, Key: "HOST", Value: "10.0.0.1",
		Sensitive: &insensitive, AuthContext: auth,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Sensitive {
		t.Fatal("expected an env entry created with sensitive=false to be a variable")
	}

	sensitive := true
	updated, err := svc.Update(context.Background(), secret_update.SecretUpdateCommand{
		ID: created.ID, Sensitive: &sensitive, AuthContext: auth,
	})
	if err != nil {
		t.Fatalf("promote to sensitive: %v", err)
	}
	if !updated.Sensitive {
		t.Fatal("expected the variable to become sensitive")
	}

	if _, err := svc.Update(context.Background(), secret_update.SecretUpdateCommand{
		ID: created.ID, Sensitive: &insensitive, AuthContext: auth,
	}); !errors.Is(err, secret.ErrCannotDesensitize) {
		t.Fatalf("expected ErrCannotDesensitize on the way back, got %v", err)
	}
}

func TestTagAttachmentLifecycle(t *testing.T) {
	svc, _, atts := newTestService()
	auth := commands.NewAuthContext(uuid.New(), uuid.New())
	accountID := auth.AccountID
	ouID := uuid.New()

	// Attach a tag to an OU; attaching twice is idempotent.
	att, err := svc.AttachTag(context.Background(), accountID, actorUserID, " Rack-1 ", &ouID, nil)
	if err != nil {
		t.Fatalf("attach tag: %v", err)
	}
	if att.Tag != "rack-1" {
		t.Fatalf("expected normalized tag, got %q", att.Tag)
	}
	again, err := svc.AttachTag(context.Background(), accountID, actorUserID, "rack-1", &ouID, nil)
	if err != nil {
		t.Fatalf("re-attach: %v", err)
	}
	if again.ID != att.ID {
		t.Fatal("expected idempotent tag attach to return the existing row")
	}

	listed, err := svc.ListTagAttachments(context.Background(), accountID, "rack-1")
	if err != nil || len(listed) != 1 {
		t.Fatalf("expected 1 tag attachment, got %v (%v)", listed, err)
	}
	if err := atts.DeleteByTag(context.Background(), accountID, "rack-1"); err != nil {
		t.Fatalf("delete by tag: %v", err)
	}
	listed, _ = svc.ListTagAttachments(context.Background(), accountID, "rack-1")
	if len(listed) != 0 {
		t.Fatalf("expected attachments gone, got %d", len(listed))
	}
}

func TestReplaceAttachmentsForTarget(t *testing.T) {
	svc, _, atts := newTestService()
	auth := commands.NewAuthContext(uuid.New(), uuid.New())
	accountID, nodeID := auth.AccountID, uuid.New()

	mkSecret := func(name string) uuid.UUID {
		created, err := svc.Create(context.Background(), secret_create.SecretCreateCommand{
			ID: uuid.New(), Name: name, Kind: secret.KindOpaque, Value: "v", AuthContext: auth,
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return created.ID
	}
	a, b, c := mkSecret("a"), mkSecret("b"), mkSecret("c")

	// Start with {a, b}.
	if _, err := svc.ReplaceAttachmentsForTarget(context.Background(), accountID, actorUserID, nil, &nodeID, []uuid.UUID{a, b}, nil); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	// Replace with {b, c} + tag.
	final, err := svc.ReplaceAttachmentsForTarget(context.Background(), accountID, actorUserID, nil, &nodeID, []uuid.UUID{b, c}, []string{"Rack-1"})
	if err != nil {
		t.Fatalf("second replace: %v", err)
	}
	if len(final) != 3 {
		t.Fatalf("expected 3 attachments (b, c, tag), got %d", len(final))
	}
	gotSecrets := map[uuid.UUID]bool{}
	gotTags := map[string]bool{}
	for _, att := range final {
		if att.SecretID != nil {
			gotSecrets[*att.SecretID] = true
		}
		if att.Tag != "" {
			gotTags[att.Tag] = true
		}
	}
	if gotSecrets[a] || !gotSecrets[b] || !gotSecrets[c] || !gotTags["rack-1"] {
		t.Fatalf("unexpected final set: secrets=%v tags=%v", gotSecrets, gotTags)
	}

	// Unknown subject fails the whole call and changes nothing.
	if _, err := svc.ReplaceAttachmentsForTarget(context.Background(), accountID, actorUserID, nil, &nodeID, []uuid.UUID{uuid.New()}, nil); !errors.Is(err, app_errors.ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound for unknown subject, got %v", err)
	}
	after, _ := atts.ListByNode(context.Background(), accountID, nodeID)
	if len(after) != 3 {
		t.Fatalf("failed call must not mutate attachments; got %d", len(after))
	}

	// Both targets rejected.
	ouID := uuid.New()
	if _, err := svc.ReplaceAttachmentsForTarget(context.Background(), accountID, actorUserID, &ouID, &nodeID, nil, nil); !errors.Is(err, secret.ErrInvalidAttachmentTarget) {
		t.Fatalf("expected ErrInvalidAttachmentTarget, got %v", err)
	}
}

func TestUpsertForNodeWithOptions(t *testing.T) {
	svc, repo, _ := newTestService()
	accountID, nodeID := uuid.New(), uuid.New()

	insensitive := false
	opts := secret.UpsertOptions{Sensitive: &insensitive, Tags: []string{"Captured"}}
	if err := svc.UpsertForNode(context.Background(), audit.SystemActor(accountID), accountID, nodeID, "host", secret.KindEnv, "HOST", "10.0.0.1", opts); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	created, err := repo.GetByName(context.Background(), accountID, "host")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if created.Sensitive {
		t.Error("expected a non-sensitive variable")
	}
	if len(created.Tags) != 1 || created.Tags[0] != "captured" {
		t.Fatalf("expected normalized tag, got %v", created.Tags)
	}

	// An explicit sensitive=false must NOT desensitize an entry that was later
	// promoted; re-upsert tags union with existing.
	sensitive := true
	if err := svc.UpsertForNode(context.Background(), audit.SystemActor(accountID), accountID, nodeID, "host", secret.KindEnv, "HOST", "10.0.0.2", secret.UpsertOptions{Sensitive: &sensitive, Tags: []string{"extra"}}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := svc.UpsertForNode(context.Background(), audit.SystemActor(accountID), accountID, nodeID, "host", secret.KindEnv, "HOST", "10.0.0.3", opts); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	after, _ := repo.GetByName(context.Background(), accountID, "host")
	if !after.Sensitive {
		t.Error("an upsert with sensitive=false must not desensitize an existing sensitive entry")
	}
	if len(after.Tags) != 2 {
		t.Errorf("expected unioned tags, got %v", after.Tags)
	}
	if after.Value != "10.0.0.3" {
		t.Errorf("expected updated value, got %q", after.Value)
	}
}

func TestNodeAttributeUpsertListAndUniquePerNodeKey(t *testing.T) {
	svc, repo, atts := newTestService()
	accountID := uuid.New()
	node1, node2 := uuid.New(), uuid.New()
	ctx := context.Background()

	// Create an attribute on node1.
	if _, err := svc.UpsertNodeAttribute(ctx, audit.UserActor(accountID, actorUserID), accountID, node1, "ci_agent_token", "A", true); err != nil {
		t.Fatalf("upsert node1: %v", err)
	}
	// The same key on node2 is a DIFFERENT attribute with its own value — the
	// per-node uniqueness the whole feature exists for.
	if _, err := svc.UpsertNodeAttribute(ctx, audit.UserActor(accountID, actorUserID), accountID, node2, "ci_agent_token", "B", true); err != nil {
		t.Fatalf("upsert node2: %v", err)
	}

	list1, err := svc.ListNodeAttributes(ctx, accountID, node1)
	if err != nil || len(list1) != 1 || list1[0].Key != "ci_agent_token" || list1[0].Value != "A" {
		t.Fatalf("node1 attributes = %+v (err %v)", list1, err)
	}
	list2, _ := svc.ListNodeAttributes(ctx, accountID, node2)
	if len(list2) != 1 || list2[0].Value != "B" {
		t.Fatalf("node2 attributes = %+v", list2)
	}

	// Re-upsert the same key updates in place (no duplicate secret/attachment).
	if _, err := svc.UpsertNodeAttribute(ctx, audit.UserActor(accountID, actorUserID), accountID, node1, "ci_agent_token", "A2", true); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	all, _ := repo.GetAllByAccountID(ctx, accountID)
	if len(all) != 2 {
		t.Fatalf("expected 2 backing secrets total, got %d", len(all))
	}
	node1Atts, _ := atts.ListByNode(ctx, accountID, node1)
	if len(node1Atts) != 1 {
		t.Fatalf("expected node1 attachment to stay unique, got %d", len(node1Atts))
	}
	list1, _ = svc.ListNodeAttributes(ctx, accountID, node1)
	if list1[0].Value != "A2" {
		t.Errorf("expected updated value A2, got %q", list1[0].Value)
	}
}

func TestNodeAttributeSensitiveGatingAndDesensitizeRejected(t *testing.T) {
	svc, _, _ := newTestService()
	accountID, node := uuid.New(), uuid.New()
	ctx := context.Background()

	// A non-sensitive attribute keeps its visible value.
	if _, err := svc.UpsertNodeAttribute(ctx, audit.UserActor(accountID, actorUserID), accountID, node, "host", "10.0.0.1", false); err != nil {
		t.Fatalf("upsert variable: %v", err)
	}
	// Promote to sensitive is allowed.
	if _, err := svc.UpsertNodeAttribute(ctx, audit.UserActor(accountID, actorUserID), accountID, node, "host", "10.0.0.2", true); err != nil {
		t.Fatalf("promote: %v", err)
	}
	// Turning it back non-sensitive is rejected (recreate instead).
	if _, err := svc.UpsertNodeAttribute(ctx, audit.UserActor(accountID, actorUserID), accountID, node, "host", "x", false); !errors.Is(err, secret.ErrCannotDesensitize) {
		t.Fatalf("expected ErrCannotDesensitize, got %v", err)
	}
}

func TestNodeAttributeInvalidKeyRejected(t *testing.T) {
	svc, _, _ := newTestService()
	if _, err := svc.UpsertNodeAttribute(context.Background(), audit.UserActor(uuid.New(), uuid.New()), uuid.New(), uuid.New(), "bad.key", "v", false); !errors.Is(err, secret.ErrInvalidAttributeKey) {
		t.Fatalf("expected ErrInvalidAttributeKey, got %v", err)
	}
}

func TestDeleteNodeAttribute(t *testing.T) {
	svc, _, atts := newTestService()
	accountID, node := uuid.New(), uuid.New()
	ctx := context.Background()

	if _, err := svc.UpsertNodeAttribute(ctx, audit.UserActor(accountID, actorUserID), accountID, node, "token", "v", true); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := svc.DeleteNodeAttribute(ctx, audit.UserActor(accountID, actorUserID), accountID, node, "token"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, _ := svc.ListNodeAttributes(ctx, accountID, node)
	if len(list) != 0 {
		t.Fatalf("expected no attributes after delete, got %d", len(list))
	}
	remaining, _ := atts.ListByNode(ctx, accountID, node)
	if len(remaining) != 0 {
		t.Fatalf("expected attachment removed, got %d", len(remaining))
	}
	// Deleting a missing attribute reports not-found.
	if err := svc.DeleteNodeAttribute(ctx, audit.UserActor(accountID, actorUserID), accountID, node, "token"); !errors.Is(err, app_errors.ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound, got %v", err)
	}
}

func TestDeleteSecretRemovesAttachments(t *testing.T) {
	svc, repo, atts := newTestService()
	accountID, nodeID := uuid.New(), uuid.New()
	auth := commands.NewAuthContext(accountID, uuid.New())

	if err := svc.UpsertForNode(context.Background(), audit.SystemActor(accountID), accountID, nodeID, "a", secret.KindOpaque, "", "1", secret.UpsertOptions{}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	sec, _ := repo.GetByName(context.Background(), accountID, "a")
	if err := svc.Delete(context.Background(), secret_delete.SecretDeleteCommand{ID: sec.ID, AuthContext: auth}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	remaining, _ := atts.ListByNode(context.Background(), accountID, nodeID)
	if len(remaining) != 0 {
		t.Fatalf("expected attachments to be removed with the secret, got %d", len(remaining))
	}
}

// actorUserID stands in for the operator making the change in tests that do not
// care which user it was, only that one was recorded.
var actorUserID = uuid.New()

// mustRecorder is the audit recorder for tests that do not assert on the trail.
func mustRecorder() *audit.Recorder {
	recorder, _ := audittest.New()
	return recorder
}
