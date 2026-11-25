package master_token

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/master_token_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/master_token_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/master_token"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// A master token is a GRANT, not a setting: any device holding it can join the
// account. Minting one, reviving a deactivated one and pushing out an expiry are
// all the same class of change, and each of them is recorded.

type auditTokenRepo struct {
	repository.MasterTokenRepository
	byID map[uuid.UUID]*master_token.MasterToken
}

func newAuditTokenRepo() *auditTokenRepo {
	return &auditTokenRepo{byID: map[uuid.UUID]*master_token.MasterToken{}}
}

func (r *auditTokenRepo) Create(_ context.Context, tok *master_token.MasterToken) error {
	copied := *tok
	r.byID[tok.ID] = &copied
	return nil
}

func (r *auditTokenRepo) Update(_ context.Context, tok *master_token.MasterToken) error {
	copied := *tok
	r.byID[tok.ID] = &copied
	return nil
}

func (r *auditTokenRepo) GetByID(_ context.Context, _, id uuid.UUID) (*master_token.MasterToken, error) {
	tok, ok := r.byID[id]
	if !ok {
		return nil, app_errors.ErrMasterTokenNotFound
	}
	copied := *tok
	return &copied, nil
}

func (r *auditTokenRepo) Delete(_ context.Context, _, id uuid.UUID) error {
	delete(r.byID, id)
	return nil
}

// acceptAllOURepo satisfies the adoption-OU reference check for tests whose
// subject is auditing, not tenancy.
type acceptAllOURepo struct {
	repository.OrganizationalUnitRepository
}

func (acceptAllOURepo) GetByID(_ context.Context, accountID, id uuid.UUID) (*organizational_unit.OrganizationalUnit, error) {
	return &organizational_unit.OrganizationalUnit{ID: id, AccountID: accountID}, nil
}

func auditTokenService() (*Service, *audittest.Sink) {
	recorder, sink := audittest.New()
	return NewService(newAuditTokenRepo(), nil, acceptAllOURepo{}, recorder,
		slog.New(slog.NewTextHandler(io.Discard, nil))), sink
}

// A rejecting OU repo stands in for "that unit is another tenant's".
type rejectAllOURepo struct {
	repository.OrganizationalUnitRepository
}

func (rejectAllOURepo) GetByID(_ context.Context, _, _ uuid.UUID) (*organizational_unit.OrganizationalUnit, error) {
	return nil, app_errors.ErrOrganizationalUnitNotFound
}

func TestMasterTokenCreateRefusesAForeignAdoptionOU(t *testing.T) {
	recorder, _ := audittest.New()
	svc := NewService(newAuditTokenRepo(), nil, rejectAllOURepo{}, recorder,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ouID := uuid.New()
	_, err := svc.Create(context.Background(), master_token_create.MasterTokenCreateCommand{
		Name:        "foreign-ou",
		OUID:        &ouID,
		AuthContext: commands.NewAuthContext(uuid.New(), uuid.New()),
	})
	if err == nil {
		t.Fatal("a master token must not be created with an adoption OU outside the account")
	}
}

func TestMasterTokenCreateIsAuditedWithItsExpiry(t *testing.T) {
	svc, sink := auditTokenService()
	accountID, userID, ouID := uuid.New(), uuid.New(), uuid.New()

	tok, err := svc.Create(context.Background(), master_token_create.MasterTokenCreateCommand{
		Name:        "rack-1 provisioning",
		OUID:        &ouID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	event, ok := sink.Find(audit_event.ActionMasterTokenCreate)
	if !ok {
		t.Fatalf("no master_token.create event; recorded %v", sink.Actions())
	}
	if event.TargetType != audit_event.TargetMasterToken || event.TargetID != tok.ID.String() {
		t.Errorf("target = %s/%s, want master_token/%s", event.TargetType, event.TargetID, tok.ID)
	}
	if event.ActorUserID == nil || *event.ActorUserID != userID {
		t.Errorf("actor = %v, want %v", event.ActorUserID, userID)
	}
	// The expiry bounds the grant, and the OU decides which unit adopted devices
	// land in — both are the substance of what was granted.
	for _, want := range []string{"rack-1 provisioning", "expires=", ouID.String()} {
		if !strings.Contains(event.Detail, want) {
			t.Errorf("detail %q is missing %q", event.Detail, want)
		}
	}
	// Only the hash of a token is ever stored; the plaintext value must not appear
	// here. Asserted unconditionally on purpose: guarding this on `tok.Token != ""`
	// would let a refactor that stopped returning the plaintext silently retire the
	// check instead of failing.
	if tok.Token == "" {
		t.Fatal("expected Create to return the plaintext token to assert against")
	}
	if strings.Contains(event.Detail, tok.Token) {
		t.Fatalf("the token value leaked into audit detail: %q", event.Detail)
	}
}

// Re-activating a token or pushing out its expiry revives an adoption credential
// that had stopped working — in effect indistinguishable from minting a new one.
func TestMasterTokenUpdateRecordsReactivationAndExpiry(t *testing.T) {
	svc, sink := auditTokenService()
	accountID, userID := uuid.New(), uuid.New()
	auth := commands.NewAuthContext(accountID, userID)
	ctx := context.Background()

	tok, err := svc.Create(ctx, master_token_create.MasterTokenCreateCommand{
		Name: "spare", AuthContext: auth,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	active := true
	later := time.Now().Add(90 * 24 * time.Hour)
	if _, err := svc.Update(ctx, master_token_update.MasterTokenUpdateCommand{
		ID: tok.ID, IsActive: &active, ExpiresAt: &later, AuthContext: auth,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	event, ok := sink.Find(audit_event.ActionMasterTokenUpdate)
	if !ok {
		t.Fatalf("no master_token.update event; recorded %v", sink.Actions())
	}
	for _, want := range []string{"is_active=true", "expires="} {
		if !strings.Contains(event.Detail, want) {
			t.Errorf("detail %q is missing %q", event.Detail, want)
		}
	}
}

func TestMasterTokenDeleteIsAudited(t *testing.T) {
	svc, sink := auditTokenService()
	accountID, userID := uuid.New(), uuid.New()
	tokenID := uuid.New()

	if err := svc.Delete(context.Background(), accountID, userID, tokenID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	event, ok := sink.Find(audit_event.ActionMasterTokenDelete)
	if !ok {
		t.Fatalf("no master_token.delete event; recorded %v", sink.Actions())
	}
	if event.TargetID != tokenID.String() {
		t.Errorf("target id = %s, want %s", event.TargetID, tokenID)
	}
}

func TestMasterTokenNewServiceRefusesWithoutAnAuditRecorder(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic without an audit recorder")
		}
	}()
	NewService(newAuditTokenRepo(), nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}
