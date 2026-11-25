package member

import (
	"context"
	"errors"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
)

// The invite's address is where the link is mailed and what the signup must
// match, so a string that is not an address is an invite nobody can accept.
// It was created with 201.
func TestInviteRefusesAStringThatIsNotAnAddress(t *testing.T) {
	f := newMemberFixture()
	f.svc.cfg.MaxInvitesPerDay = 0
	for _, email := range []string{"not-an-email", "a@", "Name <a@b.example>"} {
		_, err := f.svc.Invite(context.Background(), f.auth(), email, authz.RoleViewer, nil)
		if !errors.Is(err, app_errors.ErrUserEmailInvalid) {
			t.Errorf("Invite(%q): err = %v, want ErrUserEmailInvalid", email, err)
		}
	}
}
