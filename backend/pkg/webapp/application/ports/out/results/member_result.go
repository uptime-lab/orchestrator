package results

import (
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_invite"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
)

// RoleBindingResult is one role binding in API responses.
type RoleBindingResult struct {
	ID   string  `json:"id"`
	Role string  `json:"role"`
	OUID *string `json:"ou_id,omitempty"`
}

// MemberResult is one account member with bindings.
type MemberResult struct {
	UserID    string              `json:"user_id"`
	Name      string              `json:"name"`
	Email     string              `json:"email"`
	Status    string              `json:"status"`
	Bindings  []RoleBindingResult `json:"bindings"`
	CreatedAt time.Time           `json:"created_at"`
}

// NewRoleBindingResults maps bindings into their wire shape.
func NewRoleBindingResults(bindings []role_binding.RoleBinding) []RoleBindingResult {
	out := make([]RoleBindingResult, 0, len(bindings))
	for _, b := range bindings {
		res := RoleBindingResult{ID: b.ID.String(), Role: string(b.Role)}
		if b.OUID != nil {
			id := b.OUID.String()
			res.OUID = &id
		}
		out = append(out, res)
	}
	return out
}

// NewMemberResults maps members into their wire shape.
func NewMemberResults(members []member.Member) []MemberResult {
	out := make([]MemberResult, 0, len(members))
	for _, m := range members {
		out = append(out, MemberResult{
			UserID:    m.User.ID.String(),
			Name:      m.User.Name,
			Email:     m.User.Email,
			Status:    string(m.Status),
			Bindings:  NewRoleBindingResults(m.Bindings),
			CreatedAt: m.User.CreatedAt,
		})
	}
	return out
}

// InviteResult is one pending invite.
type InviteResult struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	OUID      *string   `json:"ou_id,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// InviteCreatedResult carries the plaintext token — returned exactly once at
// creation; only its hash is stored.
//
// Delivered reports whether the link was also emailed. The token is returned
// either way: the UI keeps the copyable link as a fallback for mail that never
// arrives, and installs without SMTP depend on it entirely.
type InviteCreatedResult struct {
	Invite    InviteResult `json:"invite"`
	Token     string       `json:"token"`
	Delivered bool         `json:"delivered"`
}

// NewInviteResult maps an invite into its wire shape (no token).
func NewInviteResult(invite *account_invite.AccountInvite) InviteResult {
	res := InviteResult{
		ID:        invite.ID.String(),
		Email:     invite.Email,
		Role:      string(invite.Role),
		ExpiresAt: invite.ExpiresAt,
		CreatedAt: invite.CreatedAt,
	}
	if invite.OUID != nil {
		id := invite.OUID.String()
		res.OUID = &id
	}
	return res
}

// NewInviteResults maps invites into their wire shape.
func NewInviteResults(invites []account_invite.AccountInvite) []InviteResult {
	out := make([]InviteResult, 0, len(invites))
	for i := range invites {
		out = append(out, NewInviteResult(&invites[i]))
	}
	return out
}
