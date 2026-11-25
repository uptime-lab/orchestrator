package sql

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
)

// The guarded membership mutations, driven through the scripted driver: what
// is asserted is the statements each one sends inside its transaction. The
// behaviour against Postgres itself is the DSN-gated integration suite's.

func scriptedMemberMutations(t *testing.T, s *scriptedDB) *MemberMutationRepository {
	t.Helper()
	sqlDB := sql.OpenDB(scriptedConnector{s})
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent), SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("opening the scripted database: %v", err)
	}
	return NewMemberMutationRepository(db)
}

func isInviteRevoke(q string) bool {
	return strings.HasPrefix(q, `DELETE FROM "account_invites"`) &&
		strings.Contains(q, "invited_by") && strings.Contains(q, "accepted_at IS NULL")
}

// revokedInsideTheTransaction reports whether the member's pending invitations
// were deleted between BEGIN and COMMIT — in the same transaction as the
// change that took away the right to have issued them.
func revokedInsideTheTransaction(log []string) bool {
	inTx := false
	for _, q := range log {
		switch {
		case q == "BEGIN":
			inTx = true
		case q == "COMMIT" || q == "ROLLBACK":
			inTx = false
		case inTx && isInviteRevoke(q):
			return true
		}
	}
	return false
}

// A removed or disabled member's pending invitations must not outlive them:
// an invitation is the issuer's grant, and a super admin about to be removed
// could otherwise leave a super-admin invitation behind that works for days.
func TestRemovalAndSuspensionRevokeTheMembersPendingInvitations(t *testing.T) {
	ctx := context.Background()
	account, target := uuid.New(), uuid.New()

	t.Run("remove", func(t *testing.T) {
		s := &scriptedDB{count: 2}
		_ = scriptedMemberMutations(t, s).RemoveGuarded(ctx, account, target)
		if !revokedInsideTheTransaction(s.entries()) {
			t.Fatalf("pending invitations were not revoked in the removal transaction: %q", s.entries())
		}
	})
	t.Run("disable", func(t *testing.T) {
		s := &scriptedDB{count: 2}
		_ = scriptedMemberMutations(t, s).SetStatusGuarded(ctx, account, target, account_member.StatusDisabled)
		if !revokedInsideTheTransaction(s.entries()) {
			t.Fatalf("pending invitations were not revoked in the suspension transaction: %q", s.entries())
		}
	})
	t.Run("re-enable leaves nothing to revoke", func(t *testing.T) {
		s := &scriptedDB{count: 2}
		_ = scriptedMemberMutations(t, s).SetStatusGuarded(ctx, account, target, account_member.StatusActive)
		for _, q := range s.entries() {
			if isInviteRevoke(q) {
				t.Fatalf("enabling a member revoked invitations: %s", q)
			}
		}
	})
}

func wroteBindings(log []string) bool {
	for _, q := range log {
		if strings.HasPrefix(q, `DELETE FROM "role_bindings"`) || strings.HasPrefix(q, `INSERT INTO "role_bindings"`) {
			return true
		}
	}
	return false
}

func viewerReplace(account, actor, target uuid.UUID, expected *[]uuid.UUID, keeps bool) BindingsReplace {
	return BindingsReplace{
		ActorID:            actor,
		UserID:             target,
		Bindings:           []role_binding.RoleBinding{*role_binding.NewRoleBinding(account, target, authz.RoleViewer, nil, &actor)},
		KeepsSuperAdmin:    keeps,
		ExpectedBindingIDs: expected,
	}
}

// Demoting a member below super_admin takes away the right to invite, so the
// invitations they issued go in the same transaction; a replace that keeps
// super_admin leaves them alone.
func TestDemotionRevokesTheMembersPendingInvitations(t *testing.T) {
	ctx := context.Background()
	account, actor, target := uuid.New(), uuid.New(), uuid.New()

	s := &scriptedDB{count: 2}
	if err := scriptedMemberMutations(t, s).ReplaceBindingsGuarded(ctx, account, viewerReplace(account, actor, target, nil, false)); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if !revokedInsideTheTransaction(s.entries()) {
		t.Fatalf("a demotion left the member's invitations pending: %q", s.entries())
	}

	s = &scriptedDB{count: 2}
	_ = scriptedMemberMutations(t, s).ReplaceBindingsGuarded(ctx, account, viewerReplace(account, actor, target, nil, true))
	for _, q := range s.entries() {
		if isInviteRevoke(q) {
			t.Fatalf("a replace that keeps super_admin revoked invitations: %s", q)
		}
	}
}

// A write prepared from an older read must not apply: the set of binding ids
// is the version, and a mismatch is refused before anything is written.
func TestReplaceRefusesAStalePrecondition(t *testing.T) {
	ctx := context.Background()
	account, actor, target := uuid.New(), uuid.New(), uuid.New()

	stale := []uuid.UUID{uuid.New()} // the member now holds none (the scripted read answers no rows)
	s := &scriptedDB{count: 2}
	err := scriptedMemberMutations(t, s).ReplaceBindingsGuarded(ctx, account, viewerReplace(account, actor, target, &stale, true))
	if !errors.Is(err, app_errors.ErrMemberBindingsStale) {
		t.Fatalf("got %v, want ErrMemberBindingsStale", err)
	}
	if wroteBindings(s.entries()) {
		t.Fatalf("a stale write touched the bindings: %q", s.entries())
	}

	current := []uuid.UUID{}
	s = &scriptedDB{count: 2}
	if err := scriptedMemberMutations(t, s).ReplaceBindingsGuarded(ctx, account, viewerReplace(account, actor, target, &current, true)); err != nil {
		t.Fatalf("a matching precondition was refused: %v", err)
	}
	if !wroteBindings(s.entries()) {
		t.Fatal("a matching precondition did not write")
	}
}

// The writer's own right is asked under the lock: one demoted or removed
// between the service's check and the transaction writes nothing.
func TestReplaceRefusesAWriterNoLongerSuperAdmin(t *testing.T) {
	ctx := context.Background()
	account, actor, target := uuid.New(), uuid.New(), uuid.New()
	s := &scriptedDB{count: 0}
	err := scriptedMemberMutations(t, s).ReplaceBindingsGuarded(ctx, account, viewerReplace(account, actor, target, nil, true))
	if !errors.Is(err, app_errors.ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
	if wroteBindings(s.entries()) {
		t.Fatalf("an unauthorized writer touched the bindings: %q", s.entries())
	}
}
