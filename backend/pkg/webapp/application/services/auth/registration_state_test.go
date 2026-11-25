package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
)

// The sign-up page shows what RegistrationState answers, and the
// registration then decides through admitSignup. If the two disagree the
// page promises a sign-up that is refused, or hides one that would work, so
// this walks the whole matrix and asks both.
func TestRegistrationStateAgreesWithAdmitSignup(t *testing.T) {
	const code = "the-code"
	for _, tc := range []struct {
		name     string
		accounts int64
		limit    int
		invite   string
		open     bool
		want     services.RegistrationState
	}{
		{"empty install, no code", 0, 0, "", false, services.RegistrationFirstOwner},
		{"empty install, code", 0, 0, code, false, services.RegistrationFirstOwner},
		{"accounts, no code", 3, 0, "", false, services.RegistrationClosed},
		{"accounts, code", 3, 0, code, false, services.RegistrationInviteCode},
		{"at the ceiling, code", 5, 5, code, false, services.RegistrationClosed},
		{"ceiling of one, empty install", 0, 1, "", false, services.RegistrationFirstOwner},
		{"below the ceiling, code", 4, 5, code, false, services.RegistrationInviteCode},
		{"open, empty install", 0, 0, "", true, services.RegistrationFirstOwner},
		{"open, accounts, no code", 3, 0, "", true, services.RegistrationOpen},
		{"open wins over a code", 3, 0, code, true, services.RegistrationOpen},
		{"open below the ceiling", 4, 5, "", true, services.RegistrationOpen},
		{"open at the ceiling", 5, 5, "", true, services.RegistrationClosed},
		{"open and a code at the ceiling", 5, 5, code, true, services.RegistrationClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := admissionService(&countingAccountRepo{accounts: tc.accounts}, tc.limit, tc.invite)
			if tc.open {
				s.openRegistration = openPolicy
				s.emailVerifier = &mailFake{enabled: true}
			}
			got, err := s.RegistrationState(context.Background())
			if err != nil {
				t.Fatalf("RegistrationState: %v", err)
			}
			if got != tc.want {
				t.Fatalf("RegistrationState = %q, want %q", got, tc.want)
			}

			// The same instance asked by the registration itself: with the
			// configured code where one is needed, and with no code at all.
			withCode := admitWith(s.admitSignup(tc.invite), tc.accounts)
			withoutCode := admitWith(s.admitSignup(""), tc.accounts)
			switch got {
			case services.RegistrationFirstOwner, services.RegistrationOpen:
				if withoutCode != nil {
					t.Fatalf("page says %q, registration without a code refused: %v", got, withoutCode)
				}
			case services.RegistrationInviteCode:
				if withCode != nil {
					t.Fatalf("page asks for the code, registration with it refused: %v", withCode)
				}
				if withoutCode == nil {
					t.Fatal("page asks for a code, registration admitted none")
				}
			case services.RegistrationClosed:
				if withCode == nil || withoutCode == nil {
					t.Fatal("page says closed, registration admitted a sign-up")
				}
			default:
				t.Fatalf("RegistrationState answered %q, which this matrix does not know", got)
			}
		})
	}
}

// A count that cannot be read is an error, never a guess: the handler
// chooses what a page shows without the count, not the service.
func TestRegistrationStateReportsAnUnreadableCount(t *testing.T) {
	s := admissionService(&countingAccountRepo{err: errors.New("pool exhausted")}, 0, "x")
	if _, err := s.RegistrationState(context.Background()); err == nil {
		t.Fatal("an unreadable count produced a registration state")
	}
}
