package member

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_invite"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// Member is one account member with their identity and bindings resolved.
type Member struct {
	User     *user.User
	Status   account_member.MemberStatus
	Bindings []role_binding.RoleBinding
}

// BindingSpec is one requested binding in a replace operation.
type BindingSpec struct {
	Role authz.Role
	OUID *uuid.UUID
}

// authzInvalidator drops cached authorization for a member so a binding/status
// change takes effect immediately (satisfied by the authz enforcer).
type authzInvalidator interface {
	Invalidate(accountID, userID uuid.UUID)
}

// sessionTerminator closes a member's live connections so a revocation takes
// effect on streams that already authorized (satisfied by the WebSocket
// manager).
type sessionTerminator interface {
	DisconnectUser(userID uuid.UUID)
}

// inviteMailer delivers an invite link (satisfied by services/invite_delivery).
// Optional: with no mail transport the token is still returned to the caller
// and the operator shares the link by hand, as before.
type inviteMailer interface {
	Enabled() bool
	Send(ctx context.Context, invite *account_invite.AccountInvite, token string) error
}

// Service manages account members, their role bindings and invites. Every
// mutating operation is gated on the caller holding an account-wide
// super_admin binding (member management stays super_admin-only).
type Service struct {
	userRepo     repository.UserRepository
	accountRepo  repository.AccountRepository
	memberRepo   repository.AccountMemberRepository
	bindingRepo  repository.RoleBindingRepository
	mutationRepo repository.MemberMutationRepository
	inviteRepo   repository.AccountInviteRepository
	ouRepo       repository.OrganizationalUnitRepository
	auditRepo    repository.AuditEventRepository
	authService  services.AuthService
	hasher       services.PasswordHasher
	invalidator  authzInvalidator
	terminator   sessionTerminator
	inviteMail   inviteMailer
	// cfg bounds what this tenant can make the platform send on its behalf.
	cfg config.MemberConfig
	// maxMembers is the per-account seat cap (SetMaxMembersPerAccount,
	// 0 = unlimited). It lives outside MemberConfig deliberately: that block
	// bounds what a tenant does to people OUTSIDE the platform (mail through the
	// operator's relay), while this bounds what one tenant takes from the shared
	// instance — the QuotaConfig family.
	maxMembers int
	logger     *slog.Logger
}

// SetInviteMailer wires invite email delivery (optional).
func (s *Service) SetInviteMailer(m inviteMailer) {
	s.inviteMail = m
}

// deliverInvite mails the link if delivery is configured, and reports whether
// it went out. Failures are logged, never returned: the invite itself was
// created, and the caller still has the one-shot token to share by hand.
func (s *Service) deliverInvite(ctx context.Context, invite *account_invite.AccountInvite, token string) bool {
	if s.inviteMail == nil || !s.inviteMail.Enabled() {
		return false
	}
	if err := s.inviteMail.Send(ctx, invite, token); err != nil {
		s.logger.WarnContext(ctx, "invite created but delivery failed", "error", err, "invite_id", invite.ID)
		return false
	}
	return true
}

// SetAuthzInvalidator wires the authz cache invalidator (optional; when unset
// changes take effect within the cache TTL instead of immediately).
func (s *Service) SetAuthzInvalidator(inv authzInvalidator) {
	s.invalidator = inv
}

// SetSessionTerminator wires live-connection teardown (optional; when unset a
// revoked member's open sockets survive until their access token expires).
func (s *Service) SetSessionTerminator(t sessionTerminator) {
	s.terminator = t
}

// invalidate drops the target's cached authorization if an invalidator is set,
// and tears down their live connections.
//
// Both happen here because both answer the same question — "this member's
// permissions just changed, stop honouring the old ones". Dropping the cache
// alone fixed the next REQUEST; a WebSocket authorizes once and then streams,
// so without the disconnect a removed member kept receiving updates for as long
// as their token lasted.
func (s *Service) invalidate(accountID, userID uuid.UUID) {
	if s.invalidator != nil {
		s.invalidator.Invalidate(accountID, userID)
	}
	if s.terminator != nil {
		s.terminator.DisconnectUser(userID)
	}
}

// NewMemberService wires the member service. All dependencies are required.
func NewMemberService(
	userRepo repository.UserRepository,
	accountRepo repository.AccountRepository,
	memberRepo repository.AccountMemberRepository,
	bindingRepo repository.RoleBindingRepository,
	mutationRepo repository.MemberMutationRepository,
	inviteRepo repository.AccountInviteRepository,
	ouRepo repository.OrganizationalUnitRepository,
	auditRepo repository.AuditEventRepository,
	authService services.AuthService,
	cfg config.MemberConfig,
	logger *slog.Logger,
) *Service {
	if mutationRepo == nil {
		panic("member.Service requires a member mutation repository")
	}
	return &Service{
		userRepo:     userRepo,
		accountRepo:  accountRepo,
		memberRepo:   memberRepo,
		bindingRepo:  bindingRepo,
		mutationRepo: mutationRepo,
		inviteRepo:   inviteRepo,
		ouRepo:       ouRepo,
		auditRepo:    auditRepo,
		authService:  authService,
		cfg:          cfg,
		hasher:       services.NewBCryptPasswordHasher(12),
		logger:       logger.With("service", "member"),
	}
}

// requireSuperAdmin fails unless the caller is an ACTIVE member holding an
// account-wide super_admin binding.
func (s *Service) requireSuperAdmin(ctx context.Context, auth commands.AuthContext) error {
	member, err := s.memberRepo.GetByAccountAndUser(ctx, auth.AccountID, auth.UserID)
	if err != nil || member.Status != account_member.StatusActive {
		return app_errors.ErrUnauthorized
	}
	bindings, err := s.bindingRepo.ListByAccountAndUser(ctx, auth.AccountID, auth.UserID)
	if err != nil {
		return err
	}
	for _, b := range bindings {
		if b.Role == authz.RoleSuperAdmin && b.IsAccountWide() {
			return nil
		}
	}
	return app_errors.ErrUnauthorized
}

// RequireSuperAdmin is requireSuperAdmin for the HTTP layer, which asks it
// before a route's step-up so a caller without the role is refused first.
// Every service method that needs the role still checks it itself.
func (s *Service) RequireSuperAdmin(ctx context.Context, auth commands.AuthContext) error {
	return s.requireSuperAdmin(ctx, auth)
}

// requireActiveMember fails unless the caller is an ACTIVE member of the
// account. The JWT middleware never re-resolves membership, so without this a
// removed or disabled member keeps reading the roster until their access token
// expires.
func (s *Service) requireActiveMember(ctx context.Context, auth commands.AuthContext) error {
	member, err := s.memberRepo.GetByAccountAndUser(ctx, auth.AccountID, auth.UserID)
	if err != nil || member.Status != account_member.StatusActive {
		return app_errors.ErrUnauthorized
	}
	return nil
}

// List returns the account's members with their bindings. Any active member
// may read the roster (the team page shows it); a revoked one may not.
func (s *Service) List(ctx context.Context, auth commands.AuthContext) ([]Member, error) {
	if err := s.requireActiveMember(ctx, auth); err != nil {
		return nil, err
	}
	memberships, err := s.memberRepo.ListByAccountID(ctx, auth.AccountID)
	if err != nil {
		return nil, err
	}
	allBindings, err := s.bindingRepo.ListByAccountID(ctx, auth.AccountID)
	if err != nil {
		return nil, err
	}
	byUser := make(map[uuid.UUID][]role_binding.RoleBinding)
	for _, b := range allBindings {
		byUser[b.UserID] = append(byUser[b.UserID], b)
	}

	members := make([]Member, 0, len(memberships))
	for _, m := range memberships {
		usr, err := s.userRepo.GetByID(ctx, m.UserID)
		if err != nil {
			s.logger.Warn("membership references missing user", "user_id", m.UserID)
			continue
		}
		members = append(members, Member{
			User:     usr,
			Status:   m.Status,
			Bindings: byUser[m.UserID],
		})
	}
	return members, nil
}

// ReplaceBindings transactionally replaces a member's bindings, honoring the
// last-super_admin invariant.
//
// expectedBindingIDs is the optional precondition: the ids of the bindings the
// caller read. When given, a member whose bindings changed since is refused
// with ErrMemberBindingsStale, so a form prepared from an older read cannot put
// back a role another administrator just removed. Absent, the write is
// unconditional, as it was before the precondition existed.
//
// Only an active account-wide super_admin may write, and the repository asks
// that again under the account lock, so the writer's right is the one held at
// the moment of the write. Since super_admin is the highest role, a writer who
// holds it may grant any role and one who does not may grant none.
func (s *Service) ReplaceBindings(ctx context.Context, auth commands.AuthContext, targetUserID uuid.UUID, specs []BindingSpec, expectedBindingIDs *[]uuid.UUID) ([]role_binding.RoleBinding, error) {
	if err := s.requireSuperAdmin(ctx, auth); err != nil {
		return nil, err
	}
	if _, err := s.memberRepo.GetByAccountAndUser(ctx, auth.AccountID, targetUserID); err != nil {
		return nil, app_errors.ErrAccountMemberNotFound
	}

	bindings := make([]role_binding.RoleBinding, 0, len(specs))
	keepsSuperAdmin := false
	for _, spec := range specs {
		if !spec.Role.IsValid() {
			return nil, app_errors.ErrInvalidRole
		}
		if spec.OUID != nil {
			if !spec.Role.CanBindToOU() {
				return nil, fmt.Errorf("%w: super_admin binds account-wide only", app_errors.ErrInvalidRole)
			}
			if _, err := s.ouRepo.GetByID(ctx, auth.AccountID, *spec.OUID); err != nil {
				return nil, fmt.Errorf("organizational unit not found: %w", err)
			}
		}
		if spec.Role == authz.RoleSuperAdmin && spec.OUID == nil {
			keepsSuperAdmin = true
		}
		createdBy := auth.UserID
		bindings = append(bindings, *role_binding.NewRoleBinding(auth.AccountID, targetUserID, spec.Role, spec.OUID, &createdBy))
	}

	// The last-super_admin invariant (dropping the target's account-wide
	// super_admin binding must not leave the account without one) is enforced
	// inside the replace transaction, under the account's advisory lock, so
	// concurrent demotions cannot both observe the same pre-mutation count.
	if err := s.mutationRepo.ReplaceBindingsGuarded(ctx, auth.AccountID, repository.BindingsReplace{
		ActorID:            auth.UserID,
		UserID:             targetUserID,
		Bindings:           bindings,
		KeepsSuperAdmin:    keepsSuperAdmin,
		ExpectedBindingIDs: expectedBindingIDs,
	}); err != nil {
		return nil, err
	}
	s.invalidate(auth.AccountID, targetUserID)
	s.audit(ctx, auth, audit_event.ActionMemberBindingsReplace, audit_event.TargetUser, targetUserID.String())
	return s.bindingRepo.ListByAccountAndUser(ctx, auth.AccountID, targetUserID)
}

// UpdateStatus enables/disables a member, honoring the last-super_admin
// invariant.
func (s *Service) UpdateStatus(ctx context.Context, auth commands.AuthContext, targetUserID uuid.UUID, status account_member.MemberStatus) error {
	if err := s.requireSuperAdmin(ctx, auth); err != nil {
		return err
	}
	if !status.IsValid() {
		return fmt.Errorf("%w (got %q)", app_errors.ErrInvalidMemberStatus, status)
	}
	// Disabling the last super_admin is rejected inside the update transaction
	// under the account's advisory lock (see MemberMutationRepository).
	if err := s.mutationRepo.SetStatusGuarded(ctx, auth.AccountID, targetUserID, status); err != nil {
		return err
	}
	s.invalidate(auth.AccountID, targetUserID)
	s.audit(ctx, auth, audit_event.MemberStatusAction(string(status)), audit_event.TargetUser, targetUserID.String())
	return nil
}

// Remove deletes a membership AND all the member's bindings in the account,
// honoring the last-super_admin invariant. The user identity is never
// touched.
func (s *Service) Remove(ctx context.Context, auth commands.AuthContext, targetUserID uuid.UUID) error {
	// Self-leave is allowed without super_admin rights; removing someone
	// else requires them.
	if targetUserID != auth.UserID {
		if err := s.requireSuperAdmin(ctx, auth); err != nil {
			return err
		}
	}
	// Bindings and membership go in one transaction under the account's
	// advisory lock, which also enforces the last-super_admin invariant.
	if err := s.mutationRepo.RemoveGuarded(ctx, auth.AccountID, targetUserID); err != nil {
		return err
	}
	s.invalidate(auth.AccountID, targetUserID)
	s.audit(ctx, auth, audit_event.ActionMemberRemove, audit_event.TargetUser, targetUserID.String())
	return nil
}

// InviteOutcome carries a freshly minted invite, its PLAINTEXT token (shown
// exactly once) and whether the link was successfully emailed.
//
// The token is returned even when delivery succeeded: the UI keeps offering a
// copyable link as a fallback for the mail that lands in a spam folder.
type InviteOutcome struct {
	Invite    *account_invite.AccountInvite
	Token     string
	Delivered bool
}

// inviteResendCooldown is how long a freshly mailed invite must sit before it
// can be re-sent to the same address.
//
// Not configurable: it is not a policy choice but the difference between a
// resend and a loop. Five minutes is longer than a person waits before deciding
// mail did not arrive, and short enough that "resend" still feels like a button
// rather than a punishment.
const inviteResendCooldown = 5 * time.Minute

// inviteRateWindow is the rolling window MaxInvitesPerDay is counted over.
const inviteRateWindow = 24 * time.Hour

// assertInviteAllowance refuses when this account has already sent its share of
// invites in the last window.
//
// Counted off the AUDIT TRAIL, not off account_invites, and that is the whole
// design: an invite row is deleted both by revoking it and by re-sending it
// (both call DeletePendingByEmail), so a cap counted on the working table is
// defeated by revoke-and-retry — a loop that sends unlimited mail while the
// table never holds more than one row. Audit events are written by the same
// action and removable by no request path.
//
// The known softness, stated rather than hidden: audit writes are best-effort
// everywhere else in this codebase, so an event that failed to write is an
// invite this counter does not see. That undercounts by the number of failed
// writes, which is the right trade — making the audit write fatal here would
// turn "bookkeeping is briefly unavailable" into "nobody can invite anyone",
// and the guard is a bound on volume, not a security boundary.
// SetMaxMembersPerAccount wires the per-account seat cap (0 = unlimited).
func (s *Service) SetMaxMembersPerAccount(max int) { s.maxMembers = max }

// assertSeatAllowance refuses when the account already holds its share of seats.
//
// Checked at BOTH the invite and the accept, and that is not belt-and-braces: an
// invite issued while there was room can be accepted long after there is none,
// so checking only the send lets an account exceed the cap by pre-issuing
// invitations. Checking only the accept would be defensible but rude — it tells
// the invitee, who can do nothing about it, instead of the admin who can.
//
// Fails OPEN on a count error, like every other quota guard here: a seat cap
// exists to bound growth, and a database hiccup must not lock a colleague out
// of an account they were legitimately invited to.
func (s *Service) assertSeatAllowance(ctx context.Context, accountID uuid.UUID) error {
	limit := s.maxMembers
	if limit <= 0 {
		return nil
	}
	count, err := s.memberRepo.CountByAccountID(ctx, accountID)
	if err != nil {
		s.logger.WarnContext(ctx, "could not count members; allowing the seat",
			"error", err, "account_id", accountID)
		return nil
	}
	if count >= int64(limit) {
		return fmt.Errorf("%w: this account holds %d of %d seats — remove a member before adding another",
			app_errors.ErrMemberLimitReached, count, limit)
	}
	return nil
}

func (s *Service) assertInviteAllowance(ctx context.Context, accountID uuid.UUID) error {
	limit := s.cfg.MaxInvitesPerDay
	if limit <= 0 {
		return nil
	}
	sent, err := s.auditRepo.CountByAccountAndActionsSince(ctx, accountID,
		[]string{audit_event.ActionInviteCreate, audit_event.ActionInviteResend},
		time.Now().Add(-inviteRateWindow))
	if err != nil {
		// Fail OPEN. This bounds how much mail a tenant can send, and the mail
		// still goes through the operator's relay either way — refusing a
		// legitimate invite because a COUNT failed is a worse outcome than one
		// extra invite in the window where the database is unhappy.
		s.logger.WarnContext(ctx, "could not count recent invites; allowing this one",
			"error", err, "account_id", accountID)
		return nil
	}
	if sent >= int64(limit) {
		return fmt.Errorf("%w: this account has sent %d invitations in the last 24 hours (limit %d) — "+
			"wait, or share the invitation link you already have",
			app_errors.ErrInviteRateLimited, sent, limit)
	}
	return nil
}

// Invite creates an invite, mails the link if delivery is configured, and
// returns the invite with its one-shot token. A prior pending invite for the
// same email is revoked.
//
// An invitation is the issuer's grant and lives only as long as the issuer may
// issue it, i.e. while they are an ACTIVE member holding an account-wide
// super_admin binding — the only role that may invite, and one that may grant
// every role, so "the invitation's role does not exceed what the issuer may
// grant now" and "the issuer may still invite" are the same test. Removing,
// disabling or demoting the issuer below that role revokes their pending
// invitations in the same transaction (MemberMutationRepository), and
// AcceptInviteRecord asks the question again at acceptance. A super_admin who
// stays one — the last one included — keeps their invitations.
func (s *Service) Invite(ctx context.Context, auth commands.AuthContext, email string, role authz.Role, ouID *uuid.UUID) (*InviteOutcome, error) {
	if err := s.requireSuperAdmin(ctx, auth); err != nil {
		return nil, err
	}
	if err := s.assertInviteAllowance(ctx, auth.AccountID); err != nil {
		return nil, err
	}
	if err := s.assertSeatAllowance(ctx, auth.AccountID); err != nil {
		return nil, err
	}
	if !role.IsValid() {
		return nil, app_errors.ErrInvalidRole
	}
	if ouID != nil {
		if !role.CanBindToOU() {
			return nil, fmt.Errorf("%w: super_admin binds account-wide only", app_errors.ErrInvalidRole)
		}
		if _, err := s.ouRepo.GetByID(ctx, auth.AccountID, *ouID); err != nil {
			return nil, fmt.Errorf("organizational unit not found: %w", err)
		}
	}

	email = account_invite.NormalizeEmail(email)
	if email == "" {
		return nil, app_errors.ErrUserEmailRequired
	}
	// The address is where the link is mailed and what the signup must match,
	// so one that is not an address is an invite nobody can ever accept.
	if !user.IsValidEmail(email) {
		return nil, app_errors.ErrUserEmailInvalid
	}

	// Reject inviting an existing member of this account.
	if existing, err := s.userRepo.GetByEmail(ctx, email); err == nil {
		if _, err := s.memberRepo.GetByAccountAndUser(ctx, auth.AccountID, existing.ID); err == nil {
			return nil, app_errors.ErrAlreadyMember
		}
	}

	// Re-inviting revokes the prior pending invite.
	if err := s.inviteRepo.DeletePendingByEmail(ctx, auth.AccountID, email); err != nil {
		return nil, err
	}

	invite, token, err := account_invite.NewAccountInvite(auth.AccountID, email, role, ouID, auth.UserID)
	if err != nil {
		return nil, err
	}
	if err := s.inviteRepo.Create(ctx, invite); err != nil {
		return nil, err
	}
	s.audit(ctx, auth, audit_event.ActionInviteCreate, audit_event.TargetInvite, invite.ID.String())
	return &InviteOutcome{Invite: invite, Token: token, Delivered: s.deliverInvite(ctx, invite, token)}, nil
}

// ResendInvite re-sends a pending invitation.
//
// The stored token is only a digest, so there is nothing to re-send: this mints
// a NEW token for the same address, role and OU scope and revokes the old one.
// That is also the safer behaviour — a resend after a leaked mailbox should not
// keep the previously mailed link alive.
func (s *Service) ResendInvite(ctx context.Context, auth commands.AuthContext, inviteID uuid.UUID) (*InviteOutcome, error) {
	if err := s.requireSuperAdmin(ctx, auth); err != nil {
		return nil, err
	}
	if err := s.assertInviteAllowance(ctx, auth.AccountID); err != nil {
		return nil, err
	}
	if err := s.assertSeatAllowance(ctx, auth.AccountID); err != nil {
		return nil, err
	}
	existing, err := s.inviteRepo.GetByID(ctx, auth.AccountID, inviteID)
	if err != nil {
		return nil, app_errors.ErrAccountInviteNotFound
	}
	if existing.AcceptedAt != nil {
		return nil, app_errors.ErrInviteNotValid
	}
	// A resend re-mails the same address, so it is the one action that can be
	// pointed at a single victim rather than spread across a list. The daily cap
	// bounds the volume; this bounds the RATE at which one mailbox can be hit,
	// which is what a recipient actually experiences as abuse.
	//
	// Measured against CreatedAt because a resend REPLACES the row (it mints a
	// new token and drops the old one), so the row's age is the age of the last
	// send — there is no separate last_sent_at to keep in step.
	if age := time.Since(existing.CreatedAt); age < inviteResendCooldown {
		return nil, fmt.Errorf("%w: this invitation was sent %s ago — wait %s before re-sending",
			app_errors.ErrInviteRateLimited,
			age.Round(time.Second), (inviteResendCooldown - age).Round(time.Second))
	}

	if err := s.inviteRepo.DeletePendingByEmail(ctx, auth.AccountID, existing.Email); err != nil {
		return nil, err
	}
	invite, token, err := account_invite.NewAccountInvite(
		auth.AccountID, existing.Email, existing.Role, existing.OUID, auth.UserID)
	if err != nil {
		return nil, err
	}
	if err := s.inviteRepo.Create(ctx, invite); err != nil {
		return nil, err
	}
	s.audit(ctx, auth, audit_event.ActionInviteResend, audit_event.TargetInvite, invite.ID.String())
	return &InviteOutcome{Invite: invite, Token: token, Delivered: s.deliverInvite(ctx, invite, token)}, nil
}

// ListInvites returns the account's pending invites.
func (s *Service) ListInvites(ctx context.Context, auth commands.AuthContext) ([]account_invite.AccountInvite, error) {
	if err := s.requireSuperAdmin(ctx, auth); err != nil {
		return nil, err
	}
	return s.inviteRepo.ListPendingByAccountID(ctx, auth.AccountID)
}

// RevokeInvite deletes a pending invite.
func (s *Service) RevokeInvite(ctx context.Context, auth commands.AuthContext, inviteID uuid.UUID) error {
	if err := s.requireSuperAdmin(ctx, auth); err != nil {
		return err
	}
	if err := s.inviteRepo.Delete(ctx, auth.AccountID, inviteID); err != nil {
		return err
	}
	s.audit(ctx, auth, audit_event.ActionInviteRevoke, audit_event.TargetInvite, inviteID.String())
	return nil
}

// AcceptInvite adds the authenticated user to the invite's account. The
// user's email must match the invited email (case-folded); the token is
// atomically single-use.
func (s *Service) AcceptInvite(ctx context.Context, userID uuid.UUID, token string) (*account_invite.AccountInvite, error) {
	usr, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, app_errors.ErrUserNotFound
	}
	return s.acceptInviteForUser(ctx, usr, token)
}

// InvitePreview is the little an invite link can safely say about itself
// BEFORE anyone accepts it.
type InvitePreview struct {
	Email       string
	AccountName string
	Role        string
	// UserExists tells the page whether to offer "sign in to accept" or
	// "create your account". Without it the page can only guess from the
	// session, and guessing wrong sends an existing user into a signup form
	// the server is guaranteed to reject.
	UserExists bool
}

// PreviewInvite resolves a token to the facts the invite page needs.
//
// Public, because it runs before there is a session — the token IS the
// credential, and whoever holds the link is the intended recipient, so telling
// them which address was invited reveals nothing they were not just emailed.
// Everything else about the account stays out of it.
func (s *Service) PreviewInvite(ctx context.Context, token string) (*InvitePreview, error) {
	invite, err := s.lookupPendingInvite(ctx, token)
	if err != nil {
		return nil, err
	}
	preview := &InvitePreview{
		Email: invite.Email,
		Role:  string(invite.Role),
	}
	if acc, err := s.accountRepo.GetByID(ctx, invite.AccountID); err == nil {
		preview.AccountName = acc.Name
	}
	if _, err := s.userRepo.GetByEmail(ctx, invite.Email); err == nil {
		preview.UserExists = true
	}
	return preview, nil
}

// AcceptInviteForNewUser creates a fresh identity from an invite: no account
// of their own — only the membership the invite grants. Returns the token
// pair for the invite's account.
func (s *Service) AcceptInviteForNewUser(ctx context.Context, token, name, password string) (*services.TokenPair, *user.User, error) {
	// The password rule is checked BEFORE the invite is looked up, for the same
	// reason password_reset.Confirm checks before claiming its token: an invite
	// is single-use, and someone who fumbles the form must not burn their link.
	//
	// This path once enforced NO password rule at all — not a length, not
	// even non-empty, so bcrypt would happily hash "" and mint an account.
	// The only floor was minLength={8} in InviteAcceptForm, i.e. the browser.
	// Invites are how people join an invite-gated install, so this is the
	// primary onboarding path, not a corner of it.
	if password == "" {
		return nil, nil, app_errors.ErrUserPasswordRequired
	}
	if len(password) < user.MinPasswordLength {
		return nil, nil, app_errors.ErrUserPasswordTooShort
	}
	if len(password) > user.MaxPasswordLength {
		return nil, nil, app_errors.ErrUserPasswordTooLong
	}

	invite, err := s.lookupPendingInvite(ctx, token)
	if err != nil {
		return nil, nil, err
	}

	// The invited email must be free.
	if _, err := s.userRepo.GetByEmail(ctx, invite.Email); err == nil {
		return nil, nil, app_errors.ErrUserAlreadyExists
	}

	hashed, err := s.hasher.HashPassword(password)
	if err != nil {
		return nil, nil, app_errors.ErrUserPasswordRequired
	}
	if name == "" {
		name = "User"
	}
	// The account email is forced to the invited email: the emailed token
	// doubles as proof of mailbox access.
	newUser := user.NewUser(invite.Email, name, hashed)
	if err := s.userRepo.Create(ctx, newUser); err != nil {
		return nil, nil, err
	}

	if _, err := s.acceptInviteForUser(ctx, newUser, token); err != nil {
		s.userRepo.Delete(ctx, newUser.ID)
		return nil, nil, err
	}

	tokens, err := s.authService.GenerateTokens(newUser, invite.AccountID)
	if err != nil {
		return nil, nil, err
	}
	return tokens, newUser, nil
}

// acceptInviteForUser validates and consumes the invite for an existing
// identity.
func (s *Service) acceptInviteForUser(ctx context.Context, usr *user.User, token string) (*account_invite.AccountInvite, error) {
	invite, err := s.lookupPendingInvite(ctx, token)
	if err != nil {
		return nil, err
	}
	if err := s.AcceptInviteRecord(ctx, usr, invite); err != nil {
		return nil, err
	}
	return invite, nil
}

// AcceptInviteRecord is the one acceptance every path shares, for an invite
// already resolved — by its token here, by the IdP-verified address in the
// federated sign-in. Whatever the route, an acceptance gets the same checks
// (address, existing membership, the seat cap, a since-deleted OU), the same
// atomic single-use flip and the same audit event: a path that skipped the
// cap would let invites issued while there was room seat members past it.
func (s *Service) AcceptInviteRecord(ctx context.Context, usr *user.User, invite *account_invite.AccountInvite) error {
	if invite == nil || invite.AcceptedAt != nil {
		return app_errors.ErrInviteNotValid
	}
	if account_invite.NormalizeEmail(usr.Email) != invite.Email {
		return app_errors.ErrInviteEmailMismatch
	}
	if _, err := s.memberRepo.GetByAccountAndUser(ctx, invite.AccountID, usr.ID); err == nil {
		return app_errors.ErrAlreadyMember
	}
	// The seat cap is re-checked here, against the account the invite names:
	// this invite may have been issued when there was room.
	if err := s.assertSeatAllowance(ctx, invite.AccountID); err != nil {
		return err
	}
	// The issuer must still be allowed to issue it. Revocation at removal,
	// suspension and demotion already deletes such invitations; this covers an
	// invitation created while that change was in flight, and any path that
	// changes a member's role without going through this service. The answer
	// is the same refusal as a revoked invitation: the invitee learns nothing
	// about the issuer.
	if err := s.requireSuperAdmin(ctx, commands.NewAuthContext(invite.AccountID, invite.InvitedBy)); err != nil {
		if errors.Is(err, app_errors.ErrUnauthorized) {
			return app_errors.ErrInviteNotValid
		}
		return err
	}
	// An invite scoped to a since-deleted OU must not mint a dead binding.
	if invite.OUID != nil {
		if _, err := s.ouRepo.GetByID(ctx, invite.AccountID, *invite.OUID); err != nil {
			return app_errors.ErrInviteNotValid
		}
	}

	// Atomic single-use: only one accept can flip the row.
	accepted, err := s.inviteRepo.Accept(ctx, invite.TokenHash)
	if err != nil {
		return err
	}
	if !accepted {
		return app_errors.ErrInviteNotValid
	}

	member := account_member.NewAccountMember(invite.AccountID, usr.ID)
	if err := s.memberRepo.Create(ctx, member); err != nil {
		return err
	}
	invitedBy := invite.InvitedBy
	binding := role_binding.NewRoleBinding(invite.AccountID, usr.ID, invite.Role, invite.OUID, &invitedBy)
	if err := s.bindingRepo.Create(ctx, binding); err != nil {
		return err
	}

	event := audit_event.NewUserEvent(invite.AccountID, usr.ID, audit_event.ActionInviteAccept, audit_event.TargetInvite, invite.ID.String())
	if err := s.auditRepo.Create(ctx, event); err != nil {
		s.logger.Warn("failed to write audit event", "error", err)
	}
	return nil
}

// lookupPendingInvite resolves a plaintext token to a pending, unexpired
// invite without consuming it.
func (s *Service) lookupPendingInvite(ctx context.Context, token string) (*account_invite.AccountInvite, error) {
	invite, err := s.inviteRepo.GetByTokenHash(ctx, account_invite.HashToken(token))
	if err != nil {
		return nil, app_errors.ErrInviteNotValid
	}
	if invite.AcceptedAt != nil {
		return nil, app_errors.ErrInviteNotValid
	}
	return invite, nil
}

// audit records a privileged member-management action; failures are logged,
// never propagated.
func (s *Service) audit(ctx context.Context, auth commands.AuthContext, action, targetType, targetID string) {
	event := audit_event.NewUserEvent(auth.AccountID, auth.UserID, action, targetType, targetID)
	if err := s.auditRepo.Create(ctx, event); err != nil {
		s.logger.Warn("failed to write audit event", "action", action, "error", err)
	}
}
