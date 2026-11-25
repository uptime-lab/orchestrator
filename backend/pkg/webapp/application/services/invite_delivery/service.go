// Package invite_delivery mails account invite links.
//
// It is a separate service rather than a method on the member service so that
// member management keeps knowing nothing about mail, and so the invite email
// can name the account and the inviter without the member service growing an
// account repository it otherwise does not need.
//
// Delivery never gates invite creation: the one-shot token is still returned to
// the caller, so an install with no SMTP keeps working exactly as before — the
// operator copies the link out of the UI by hand.
package invite_delivery

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_invite"
)

// invitePath is the SPA route that accepts an invite token.
const invitePath = "/invite"

// Sender delivers a message (satisfied by infrastructure/mailer).
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Service renders and sends invite emails.
type Service struct {
	accountRepo repository.AccountRepository
	userRepo    repository.UserRepository
	sender      Sender
	appBaseURL  string
	logger      *slog.Logger
}

func NewService(
	accountRepo repository.AccountRepository,
	userRepo repository.UserRepository,
	sender Sender,
	appBaseURL string,
	logger *slog.Logger,
) *Service {
	if accountRepo == nil {
		panic("invite_delivery.Service requires an account repository")
	}
	if userRepo == nil {
		panic("invite_delivery.Service requires a user repository")
	}
	return &Service{
		accountRepo: accountRepo,
		userRepo:    userRepo,
		sender:      sender,
		appBaseURL:  strings.TrimRight(strings.TrimSpace(appBaseURL), "/"),
		logger:      logger.With("service", "invite_delivery"),
	}
}

// Enabled reports whether invite mail can actually be delivered.
func (s *Service) Enabled() bool {
	return s.sender != nil && s.appBaseURL != ""
}

// Send mails the invite link to the invited address.
//
// The account and inviter names are decoration: if either lookup fails the mail
// still goes out with a neutral wording, because failing to send an invite over
// a missing display name would be a worse outcome than a slightly vaguer email.
func (s *Service) Send(ctx context.Context, invite *account_invite.AccountInvite, token string) error {
	if !s.Enabled() {
		return nil
	}

	accountName := ""
	if acc, err := s.accountRepo.GetByID(ctx, invite.AccountID); err == nil {
		accountName = strings.TrimSpace(acc.Name)
	}
	inviterName := ""
	if usr, err := s.userRepo.GetByID(ctx, invite.InvitedBy); err == nil {
		inviterName = strings.TrimSpace(usr.Name)
		if inviterName == "" {
			inviterName = usr.Email
		}
	}

	link := fmt.Sprintf("%s%s?token=%s", s.appBaseURL, invitePath, url.QueryEscape(token))
	subject := "You have been invited to Orchestrator"
	if accountName != "" {
		subject = fmt.Sprintf("You have been invited to %s on Orchestrator", accountName)
	}
	if err := s.sender.Send(ctx, invite.Email, subject, inviteBody(accountName, inviterName, string(invite.Role), link)); err != nil {
		s.logger.ErrorContext(ctx, "failed to send invite email", "error", err, "invite_id", invite.ID)
		return err
	}
	s.logger.InfoContext(ctx, "invite email sent", "invite_id", invite.ID)
	return nil
}

func inviteBody(accountName, inviterName, role, link string) string {
	target := "an account on Orchestrator"
	if accountName != "" {
		target = accountName
	}
	who := "Someone"
	if inviterName != "" {
		who = inviterName
	}
	return fmt.Sprintf(`Hello,

%s invited you to join %s as %s.

Accept the invitation here:

%s

The link is valid for 7 days and can be used once. If you already have an
Orchestrator account with this address, sign in first and the invitation will
be added to your existing account rather than creating a new one.

If you were not expecting this, you can ignore this message — nothing happens
until the link is opened.
`, who, target, role, link)
}
