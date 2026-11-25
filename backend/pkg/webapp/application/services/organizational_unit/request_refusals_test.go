package organizational_unit

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_policy_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/organizational_unit_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/organizational_unit_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// requireRefusal asserts err is a typed refusal every handler answers as
// itself: the given status, the given code, and a message naming want.
func requireRefusal(t *testing.T, err error, status int, code, want string) {
	t.Helper()
	var facing results.OperatorFacingError
	if !errors.As(err, &facing) {
		t.Fatalf("err = %v (%T), want a typed refusal; an untyped error answers 500", err, err)
	}
	if facing.HTTPStatus() != status || facing.ErrorCode() != code {
		t.Errorf("refusal = %d %q, want %d %q", facing.HTTPStatus(), facing.ErrorCode(), status, code)
	}
	if !strings.Contains(facing.Error(), want) {
		t.Errorf("message %q does not name %q", facing.Error(), want)
	}
}

// A tri-state policy value outside inherit|on|off used to read as inherit:
// the request answered 200 and cleared the level's setting. Every route that
// takes one refuses it, naming the field and the values it accepts.
func TestUnknownPolicyFlagIsRefusedOnEveryLevel(t *testing.T) {
	bogus := organizational_unit.PolicyFlag("bogus")
	ctx := context.Background()

	t.Run("organizational unit update", func(t *testing.T) {
		f := newFleetFixture(nil, nil)
		_, err := f.svc.Update(ctx, organizational_unit_update.OrganizationalUnitUpdateCommand{
			ID: f.rootID, BootToOS: &bogus, AuthContext: commands.NewAuthContext(f.accountID, uuid.New()),
		})
		requireRefusal(t, err, http.StatusBadRequest, "invalid_policy_flag", "boot_to_os must be one of inherit, on, off")
	})
	t.Run("organizational unit create", func(t *testing.T) {
		f := newFleetFixture(nil, nil)
		_, err := f.svc.Create(ctx, organizational_unit_create.OrganizationalUnitCreateCommand{
			Name: "unit", SSHPasswordAuth: bogus, AuthContext: commands.NewAuthContext(f.accountID, uuid.New()),
		})
		requireRefusal(t, err, http.StatusBadRequest, "invalid_policy_flag", "ssh_password_auth")
	})
	t.Run("account policy", func(t *testing.T) {
		f := newFleetFixture(nil, nil)
		_, err := f.svc.SetAccountPolicy(ctx, account_policy_update.AccountPolicyUpdateCommand{
			NetworkFirst: &bogus, AuthContext: commands.NewAuthContext(f.accountID, uuid.New()),
		})
		requireRefusal(t, err, http.StatusBadRequest, "invalid_policy_flag", "network_first")
	})
	t.Run("node policy", func(t *testing.T) {
		repo := &policyFakeNodeRepo{n: &node.Node{ID: uuid.New()}}
		s := &Service{nodeRepo: repo, audit: testRecorder()}
		err := s.SetNodePolicy(ctx, audit.SystemActor(uuid.New()), uuid.New(), repo.n.ID, nil, &bogus, nil)
		requireRefusal(t, err, http.StatusBadRequest, "invalid_policy_flag", "boot_to_os")
		if repo.written != nil {
			t.Errorf("a refused policy wrote %v", repo.written)
		}
	})
}

// The empty value and the three named ones stay accepted: empty is how the
// create command and older clients say inherit.
func TestKnownPolicyFlagsAreAccepted(t *testing.T) {
	for _, v := range []organizational_unit.PolicyFlag{"", organizational_unit.PolicyInherit, organizational_unit.PolicyOn, organizational_unit.PolicyOff} {
		if err := v.Validate("boot_to_os"); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", v, err)
		}
	}
}

// A watchdog configuration the domain refuses is the request's fault. It
// reached the operator as a 500 "failed to update", which hid the reason the
// validator had already worded.
func TestInvalidWatchdogConfigIsARequestRefusal(t *testing.T) {
	ctx := context.Background()
	empty := &node.WatchdogConfig{Enabled: true} // enabled, supervising nothing

	t.Run("organizational unit", func(t *testing.T) {
		f := newFleetFixture(nil, nil)
		_, err := f.svc.Update(ctx, organizational_unit_update.OrganizationalUnitUpdateCommand{
			ID: f.rootID, WatchdogConfig: empty, AuthContext: commands.NewAuthContext(f.accountID, uuid.New()),
		})
		requireRefusal(t, err, http.StatusBadRequest, "invalid_watchdog_config", "enable at least one kind of supervision")
	})
	t.Run("account policy", func(t *testing.T) {
		f := newFleetFixture(nil, nil)
		_, err := f.svc.SetAccountPolicy(ctx, account_policy_update.AccountPolicyUpdateCommand{
			WatchdogConfig: empty, AuthContext: commands.NewAuthContext(f.accountID, uuid.New()),
		})
		requireRefusal(t, err, http.StatusBadRequest, "invalid_watchdog_config", "enable at least one kind of supervision")
	})
}

// A reference that is not a UUID is named by field rather than reported as a
// server fault.
func TestMalformedProvisioningReferenceIsARequestRefusal(t *testing.T) {
	f := newFleetFixture(nil, nil)
	notAUUID := "nope"
	_, err := f.svc.Update(context.Background(), organizational_unit_update.OrganizationalUnitUpdateCommand{
		ID: f.rootID, OSImageID: &notAUUID, AuthContext: commands.NewAuthContext(f.accountID, uuid.New()),
	})
	requireRefusal(t, err, http.StatusBadRequest, "invalid_os_image_id", "os_image_id is not a valid UUID")
}
