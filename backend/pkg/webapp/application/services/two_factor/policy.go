package two_factor

import (
	"context"
	"log/slog"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"github.com/google/uuid"
)

// Policy answers the account-level "is a second factor mandatory here?"
// question for the enforcement middleware.
//
// It is a thin, separately-cached read because it runs on EVERY authenticated
// request. Without the cache, mandating 2FA would add an accounts lookup to the
// hot path of the whole API.
// passkeyCounter reports how many WebAuthn credentials a user holds.
type passkeyCounter interface {
	CountByUserID(ctx context.Context, userID uuid.UUID) (int64, error)
}

// passwordLookup reads a user so the policy can tell a password user from a
// federated one. Satisfied by the user repository.
type passwordLookup interface {
	GetByID(ctx context.Context, id uuid.UUID) (*user.User, error)
}

type Policy struct {
	accountRepo repository.AccountRepository
	factors     *Service
	passkeys    passkeyCounter
	users       passwordLookup
	cache       *ttlCache[uuid.UUID, bool]
	logger      *slog.Logger
}

func NewPolicy(accountRepo repository.AccountRepository, factors *Service, logger *slog.Logger) *Policy {
	if accountRepo == nil {
		panic("two_factor.Policy requires an account repository")
	}
	if factors == nil {
		panic("two_factor.Policy requires the two-factor service")
	}
	return &Policy{
		accountRepo: accountRepo,
		factors:     factors,
		cache:       newTTLCache[uuid.UUID, bool](policyCacheTTL),
		logger:      logger.With("service", "two_factor_policy"),
	}
}

// AccountRequiresTwoFactor reports whether the account mandates a second
// factor.
//
// Fails OPEN on a lookup error: an unreachable database must not lock every
// member of every account out of the product. The factor itself is still
// enforced at login for users who enrolled one — this flag only governs whether
// enrollment is compulsory.
func (p *Policy) AccountRequiresTwoFactor(ctx context.Context, accountID uuid.UUID) bool {
	if required, ok := p.cache.get(accountID); ok {
		return required
	}
	acc, err := p.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		p.logger.WarnContext(ctx, "failed to resolve two-factor policy", "account_id", accountID, "error", err)
		return false
	}
	p.cache.set(accountID, acc.RequireTwoFactor)
	return acc.RequireTwoFactor
}

// UserHasTwoFactor reports whether the user satisfies the requirement.
//
// A registered passkey counts. A passkey login is already two factors —
// possession of the authenticator plus the user verification it performs — so
// demanding a TOTP secret on top would be ceremony, and would push a user who
// has adopted the stronger credential back onto the weaker one.
//
// This answers "is the user ENROLLED", not "did this session use the factor".
// It is safe to gate on only because the login path closes the gap: for a
// member of a requiring account a password alone never yields a session once
// a passkey exists (auth.Login challenges, and only the passkey ceremony
// completes it), and the ceremony itself demands user verification
// (passkey.Service). Loosen either and this check counts a session the
// factor never touched.
//
// The mandate is for PASSWORD users. A member with no password — invited by
// email and signed in with Google, never having set one — is never asked for
// a factor: the federated login path does not challenge (the IdP owns its
// MFA), so demanding enrolment produced an authenticator nobody was prompted
// for and a 403 on every request until it existed.
// The read is by primary key and runs only for accounts that mandate a factor;
// a FAILED read keeps the previous answer rather than exempting, because this
// is a convenience for federated members, not a boundary. A password set
// later puts the member under the mandate on their next request.
func (p *Policy) UserHasTwoFactor(ctx context.Context, userID uuid.UUID) bool {
	if p.users != nil {
		usr, err := p.users.GetByID(ctx, userID)
		switch {
		case err != nil:
			p.logger.WarnContext(ctx, "failed to read the user for the two-factor policy; assuming a password user", "user_id", userID, "error", err)
		case strings.TrimSpace(usr.HashedPassword) == "":
			return true
		}
	}
	if p.factors.IsEnabled(ctx, userID) {
		return true
	}
	if p.passkeys != nil {
		if count, err := p.passkeys.CountByUserID(ctx, userID); err == nil && count > 0 {
			return true
		}
	}
	return false
}

// SetPasskeyCounter lets a registered passkey satisfy the requirement.
func (p *Policy) SetPasskeyCounter(c passkeyCounter) {
	p.passkeys = c
}

// SetPasswordLookup lets the policy exempt members who have no password.
func (p *Policy) SetPasswordLookup(l passwordLookup) {
	p.users = l
}

// Invalidate drops the cached answer for an account, so toggling the policy
// takes effect at once instead of within the TTL.
func (p *Policy) Invalidate(accountID uuid.UUID) {
	p.cache.delete(accountID)
}
