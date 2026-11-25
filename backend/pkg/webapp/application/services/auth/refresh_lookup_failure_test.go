package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

var errDatabaseHiccup = errors.New("connection reset by peer")

type erringUserRepo struct {
	repository.UserRepository
	usr *user.User
	err error
}

func (r erringUserRepo) GetByID(context.Context, uuid.UUID) (*user.User, error) {
	return r.usr, r.err
}

type erringMemberRepo struct {
	repository.AccountMemberRepository
	err error
}

func (r erringMemberRepo) GetByAccountAndUser(_ context.Context, accountID, userID uuid.UUID) (*account_member.AccountMember, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &account_member.AccountMember{AccountID: accountID, UserID: userID, Status: account_member.StatusActive}, nil
}

// refreshWith redeems a fresh refresh token against the given lookups.
func refreshWith(t *testing.T, users repository.UserRepository, members repository.AccountMemberRepository) error {
	t.Helper()
	svc := newTestAuthServiceWithStore(newFakeRefreshStore())
	usr := &user.User{ID: uuid.New(), Email: "u@example.com"}
	pair, err := svc.GenerateTokens(usr, uuid.New())
	if err != nil {
		t.Fatalf("GenerateTokens: %v", err)
	}
	if u, ok := users.(erringUserRepo); ok && u.err == nil {
		u.usr = usr
		users = u
	}
	svc.userRepo = users
	svc.memberRepo = members
	_, err = svc.RefreshToken(pair.RefreshToken)
	return err
}

// A refresh refused as "user gone" or "not a member" ends the session in the
// client; a lookup that merely failed must not, or a database hiccup signs
// everyone out. Only a definitive absence is a refusal.
func TestRefreshTellsAFailedLookupFromAnAbsence(t *testing.T) {
	refusals := []error{app_errors.ErrUserNotFound, app_errors.ErrNotAMember, app_errors.ErrTokenInvalid}
	isRefusal := func(err error) bool {
		for _, r := range refusals {
			if errors.Is(err, r) {
				return true
			}
		}
		return false
	}

	if err := refreshWith(t, erringUserRepo{err: errDatabaseHiccup}, erringMemberRepo{}); err == nil || isRefusal(err) {
		t.Fatalf("a failed user lookup answered %v, want a server fault", err)
	}
	if err := refreshWith(t, erringUserRepo{}, erringMemberRepo{err: errDatabaseHiccup}); err == nil || isRefusal(err) {
		t.Fatalf("a failed membership lookup answered %v, want a server fault", err)
	}

	if err := refreshWith(t, erringUserRepo{err: app_errors.ErrUserNotFound}, erringMemberRepo{}); !errors.Is(err, app_errors.ErrUserNotFound) {
		t.Fatalf("a user that is gone answered %v, want ErrUserNotFound", err)
	}
	if err := refreshWith(t, erringUserRepo{}, erringMemberRepo{err: app_errors.ErrAccountMemberNotFound}); !errors.Is(err, app_errors.ErrNotAMember) {
		t.Fatalf("a membership that is gone answered %v, want ErrNotAMember", err)
	}
}
