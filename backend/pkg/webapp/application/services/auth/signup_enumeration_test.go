package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_register"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// emailProbe answers every address as registered and counts the lookups.
type emailProbe struct {
	stubUserRepo
	lookups int
}

func (p *emailProbe) GetByEmail(ctx context.Context, email string) (*user.User, error) {
	p.lookups++
	return user.NewUser(email, "Someone", "hash"), nil
}

// A caller who may not register at all gets the admission's refusal whether
// or not the address is taken, and the address is never looked up: the
// public sign-up form used to answer 409 "user already exists" for a
// registered email even on a closed instance or with a wrong invite code.
func TestSignupRefusesBeforeLookingTheAddressUp(t *testing.T) {
	cases := []struct {
		name       string
		inviteCode string
		limit      int
		presented  string
		check      func(error) bool
	}{
		{"closed: no invite code configured", "", 0, "", func(err error) bool {
			return isClosed(err, app_errors.ReasonNoInviteCode)
		}},
		{"closed: account ceiling reached", "the-code", 1, "the-code", func(err error) bool {
			return isClosed(err, app_errors.ReasonAccountCeiling)
		}},
		{"wrong invite code", "the-code", 0, "wrong", func(err error) bool {
			return errors.Is(err, app_errors.ErrInvalidInviteCode)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &countingAccountRepo{accounts: 1}
			svc := admissionService(repo, tc.limit, tc.inviteCode)
			users := &emailProbe{}
			svc.userRepo = users

			_, err := svc.Register(user_register.RegisterCommand{
				Email: "registered@example.com", Password: "long-enough-pw", InviteCode: tc.presented,
			})

			if !tc.check(err) {
				t.Fatalf("Register = %v, want the admission's refusal", err)
			}
			if errors.Is(err, app_errors.ErrUserAlreadyExists) {
				t.Fatal("a caller who may not register learned the address is taken")
			}
			if users.lookups != 0 {
				t.Fatalf("the address was looked up %d times before admission refused", users.lookups)
			}
		})
	}
}

// An admitted caller is still told the address is taken — the sign-up form's
// own answer to someone who already has an account.
func TestSignupTellsAnAdmittedCallerTheAddressIsTaken(t *testing.T) {
	repo := &countingAccountRepo{accounts: 1}
	svc := admissionService(repo, 0, "the-code")
	svc.userRepo = &emailProbe{}

	_, err := svc.Register(user_register.RegisterCommand{
		Email: "registered@example.com", Password: "long-enough-pw", InviteCode: "the-code",
	})
	if !errors.Is(err, app_errors.ErrUserAlreadyExists) {
		t.Fatalf("Register = %v, want ErrUserAlreadyExists", err)
	}
}

// A count that cannot be read refuses up front, as the locked admission does.
func TestSignupFailsClosedWhenTheCountIsUnreadable(t *testing.T) {
	repo := &countingAccountRepo{err: errors.New("database unavailable")}
	svc := admissionService(repo, 0, "the-code")
	users := &emailProbe{}
	svc.userRepo = users

	_, err := svc.Register(user_register.RegisterCommand{
		Email: "registered@example.com", Password: "long-enough-pw", InviteCode: "the-code",
	})
	if !isClosed(err, app_errors.ReasonCountUnavailable) {
		t.Fatalf("Register = %v, want registration closed (count unavailable)", err)
	}
	if users.lookups != 0 {
		t.Fatal("the address was looked up although the admission could not be decided")
	}
}
