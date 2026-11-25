package passkey

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/passkey"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// The last-credential guard is a check-then-act and must run under
// the shared per-user lock, so it serializes against the OIDC-unlink path.
// This proves passkey.Delete enters the lock AND runs the whole guard inside
// it (the deletion happens between WithUser's enter and exit).

type spyLocker struct {
	entered  bool
	held     bool // true only while the callback runs
	sawWhile bool // the delete was observed while the lock was held
}

func (s *spyLocker) WithUser(_ uuid.UUID, fn func() error) error {
	s.entered = true
	s.held = true
	defer func() { s.held = false }()
	return fn()
}

type lockTestPasskeyRepo struct {
	repository.PasskeyRepository
	count  int64
	owned  []passkey.Passkey
	locker *spyLocker
}

func (r *lockTestPasskeyRepo) ListByUserID(context.Context, uuid.UUID) ([]passkey.Passkey, error) {
	return r.owned, nil
}

func (r *lockTestPasskeyRepo) CountByUserID(context.Context, uuid.UUID) (int64, error) {
	return r.count, nil
}

func (r *lockTestPasskeyRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	if r.locker.held {
		r.locker.sawWhile = true
	}
	return nil
}

type lockTestUserRepo struct {
	repository.UserRepository
	usr *user.User
}

func (r *lockTestUserRepo) GetByID(context.Context, uuid.UUID) (*user.User, error) {
	return r.usr, nil
}

func TestDeleteRunsTheGuardUnderTheLock(t *testing.T) {
	locker := &spyLocker{}
	usr := &user.User{HashedPassword: "hash"} // a second method exists, so the delete is allowed
	passkeyID := uuid.New()
	svc := &Service{
		repo:     &lockTestPasskeyRepo{count: 2, owned: []passkey.Passkey{{ID: passkeyID}}, locker: locker},
		userRepo: &lockTestUserRepo{usr: usr},
		credLock: locker,
		logger:   slog.Default(),
	}

	if err := svc.Delete(context.Background(), uuid.New(), passkeyID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !locker.entered {
		t.Fatal("Delete did not acquire the per-user credential lock")
	}
	if !locker.sawWhile {
		t.Fatal("the passkey deletion ran OUTSIDE the lock — the count-and-remove is still racy")
	}
}

// A passkey id that is not one of the caller's is a miss (404), answered
// before the last-credential guard. The guard counts "after this one is
// removed", so for a password user with no passkeys it computed -1 and
// refused with "the only way you can sign in" (409) for a key that never
// existed.
func TestDeletingAnUnknownPasskeyIsNotFound(t *testing.T) {
	locker := &spyLocker{}
	svc := &Service{
		repo:     &lockTestPasskeyRepo{count: 0, locker: locker},
		userRepo: &lockTestUserRepo{usr: &user.User{HashedPassword: "hash"}},
		credLock: locker,
		logger:   slog.Default(),
	}
	err := svc.Delete(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, app_errors.ErrPasskeyNotFound) {
		t.Fatalf("err = %v, want ErrPasskeyNotFound", err)
	}
	if locker.sawWhile {
		t.Fatal("a delete ran for a passkey the caller does not hold")
	}
}
