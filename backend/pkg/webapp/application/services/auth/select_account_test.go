package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
)

// Why these tests exist. The pre-auth account-selection token
// was a stateless JWT: valid for its whole TTL however many times it was
// presented, and unaware of a password change. A challenge captured during a
// login could therefore mint a fresh, refreshable session AFTER the user had
// reset the password and revoked every session — the one moment a revocation
// is supposed to be total.

// newSelectFixture logs a two-account user in with a password and returns the
// account-selection challenge.
func newSelectFixture(t *testing.T) (*mandatoryFixture, *services.LoginResult) {
	t.Helper()
	f := newMandatoryFixture(t, passkeyCount{n: 0}, noTwoFactor{}, false, false)
	// requireActiveMembership reads one membership by account; the list stub
	// does not answer that, so use the always-active one.
	f.svc.memberRepo = twoAccountMembers{stubMemberRepo: f.svc.memberRepo.(*stubMemberRepo)}
	result := f.login(t)
	if result.PreToken == "" || len(result.Accounts) != 2 {
		t.Fatalf("expected an account-selection challenge, got %+v", result)
	}
	return f, result
}

// twoAccountMembers answers both the list and the per-account lookup, the
// latter always as an active membership.
type twoAccountMembers struct {
	*stubMemberRepo
}

func (m twoAccountMembers) GetByAccountAndUser(_ context.Context, accountID, userID uuid.UUID) (*account_member.AccountMember, error) {
	for _, mem := range m.memberships {
		if mem.AccountID == accountID && mem.UserID == userID {
			active := mem
			return &active, nil
		}
	}
	return nil, app_errors.ErrNotAMember
}

func TestSelectAccountSpendsThePreAuthToken(t *testing.T) {
	f, challenge := newSelectFixture(t)
	accountID := f.accounts[0].ID
	if _, _, err := f.svc.SelectAccount(context.Background(), challenge.PreToken, accountID); err != nil {
		t.Fatalf("first selection: %v", err)
	}
	if _, _, err := f.svc.SelectAccount(context.Background(), challenge.PreToken, f.accounts[1].ID); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a replayed pre-auth token = %v, want ErrTokenInvalid", err)
	}
}

func TestSelectAccountRefusesATokenPredatingThePasswordChange(t *testing.T) {
	f, challenge := newSelectFixture(t)
	changed := time.Now().Add(time.Second)
	f.usr.PasswordChangedAt = &changed
	before := len(f.svc.refreshTokens.(*fakeRefreshStore).byHash)
	if _, _, err := f.svc.SelectAccount(context.Background(), challenge.PreToken, f.accounts[0].ID); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a pre-auth token issued before the password change = %v, want ErrTokenInvalid", err)
	}
	if got := len(f.svc.refreshTokens.(*fakeRefreshStore).byHash); got != before {
		t.Fatalf("refusal still recorded a refresh token: %d records, want %d", got, before)
	}
}

func TestSelectAccountAllowsATokenIssuedAfterThePasswordChange(t *testing.T) {
	f, _ := newSelectFixture(t)
	changed := time.Now().Add(-time.Minute)
	f.usr.PasswordChangedAt = &changed
	challenge := f.login(t)
	if _, _, err := f.svc.SelectAccount(context.Background(), challenge.PreToken, f.accounts[0].ID); err != nil {
		t.Fatalf("a pre-auth token issued after the password change was refused: %v", err)
	}
}

func TestSelectAccountConcurrentDoubleUseAdmitsExactlyOne(t *testing.T) {
	f, challenge := newSelectFixture(t)
	const callers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, _, err := f.svc.SelectAccount(context.Background(), challenge.PreToken, f.accounts[i%2].ID); err == nil {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if admitted != 1 {
		t.Fatalf("%d concurrent selections were admitted on one pre-auth token, want exactly 1", admitted)
	}
}

func TestSelectAccountRefusesAnUnissuedTokenAfterARestart(t *testing.T) {
	// A restart loses the ticket map. The signed token still verifies; it must
	// still be refused, or a restart would restore the replayability.
	f, challenge := newSelectFixture(t)
	f.svc.preAuthTickets = newTicketStore(preAuthTokenTTL)
	if _, _, err := f.svc.SelectAccount(context.Background(), challenge.PreToken, f.accounts[0].ID); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("an unissued pre-auth token = %v, want ErrTokenInvalid", err)
	}
}

func TestCompleteTwoFactorIssuesASpendablePreAuthToken(t *testing.T) {
	// The multi-account TOTP path mints its pre-auth token through the same
	// function, so it must be issued into the store too — otherwise every
	// two-factor user with two accounts is refused at account selection.
	f := newMandatoryFixture(t, passkeyCount{n: 0}, totpEnrolled{}, false, false)
	f.svc.memberRepo = twoAccountMembers{stubMemberRepo: f.svc.memberRepo.(*stubMemberRepo)}
	challenge := f.login(t)
	result, err := f.svc.CompleteTwoFactor(context.Background(), challenge.TwoFactorToken, "000000")
	if err != nil {
		t.Fatalf("CompleteTwoFactor: %v", err)
	}
	if result.PreToken == "" {
		t.Fatalf("expected an account-selection challenge, got %+v", result)
	}
	if _, _, err := f.svc.SelectAccount(context.Background(), result.PreToken, f.accounts[1].ID); err != nil {
		t.Fatalf("SelectAccount after a two-factor login: %v", err)
	}
}
