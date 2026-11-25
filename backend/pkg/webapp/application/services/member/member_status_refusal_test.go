package member

import (
	"context"
	"errors"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
)

// A status other than active/disabled is the caller's mistake: it surfaces
// as the typed refusal the handler answers 400, and nothing is written.
func TestAnUnknownMemberStatusIsATypedRefusal(t *testing.T) {
	f := newMemberFixture()

	err := f.svc.UpdateStatus(context.Background(), f.auth(), f.target, account_member.MemberStatus("suspended"))
	if !errors.Is(err, app_errors.ErrInvalidMemberStatus) {
		t.Fatalf("got %v, want ErrInvalidMemberStatus", err)
	}
	if f.mutations.statusCalls != 0 {
		t.Fatal("a refused status change reached the repository")
	}
}
