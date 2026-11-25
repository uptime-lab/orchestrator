package organizational_unit

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/organizational_unit_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/organizational_unit_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// Every policy an update stores, a create stores too. A create that did not
// carry a field answered 201 and silently stored nothing for it.

type createStoreOURepo struct {
	repository.OrganizationalUnitRepository
	stored map[uuid.UUID]organizational_unit.OrganizationalUnit
}

func (r *createStoreOURepo) Create(_ context.Context, ou *organizational_unit.OrganizationalUnit) error {
	r.stored[ou.ID] = *ou
	return nil
}

func (r *createStoreOURepo) GetByID(_ context.Context, _, id uuid.UUID) (*organizational_unit.OrganizationalUnit, error) {
	ou, ok := r.stored[id]
	if !ok {
		return nil, app_errors.ErrOrganizationalUnitNotFound
	}
	return &ou, nil
}

func (r *createStoreOURepo) GetAllByAccountID(_ context.Context, accountID uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	var out []organizational_unit.OrganizationalUnit
	for _, ou := range r.stored {
		if ou.AccountID == accountID {
			out = append(out, ou)
		}
	}
	return out, nil
}

func newCreateService() (*Service, *createStoreOURepo) {
	repo := &createStoreOURepo{stored: map[uuid.UUID]organizational_unit.OrganizationalUnit{}}
	recorder, _ := testAuditRecorder()
	svc := &Service{
		ouRepo: repo,
		audit:  recorder,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		deviceProfiles: &device_profile.Catalog{Boards: []device_profile.BoardProfile{
			{ID: "bare-pi", Name: "Bare Pi"},
		}},
	}
	return svc, repo
}

func TestCreateStoresEveryPolicyTheUpdateStores(t *testing.T) {
	svc, _ := newCreateService()
	ctx := context.Background()
	auth := commands.NewAuthContext(uuid.New(), uuid.New())
	position := 7
	watchdog := &node.WatchdogConfig{Enabled: true, AgentHealth: true, AgentChangeSec: 300, IntervalSec: 10, RetryTimeoutSec: 60}
	led := &node.LEDConfig{ActLEDOff: true}
	profile := &device_profile.Equipage{ProfileID: "bare-pi"}

	created, err := svc.Create(ctx, organizational_unit_create.OrganizationalUnitCreateCommand{
		Name: "unit", Position: &position, WatchdogConfig: watchdog, LEDConfig: led, DeviceProfile: profile,
		NetworkFirst: organizational_unit.PolicyOn, BootToOS: organizational_unit.PolicyOff,
		SSHPasswordAuth: organizational_unit.PolicyOff, DefaultUsername: "pi", AuthContext: auth,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := svc.GetByID(ctx, organizational_unit_get.OrganizationalUnitGetQuery{ID: created.ID, AuthContext: auth})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Position != position {
		t.Errorf("position = %d, want %d", got.Position, position)
	}
	if got.WatchdogConfig == nil || !got.WatchdogConfig.AgentHealth || got.WatchdogConfig.AgentChangeSec != 300 {
		t.Errorf("watchdog_config = %+v, want the one created with", got.WatchdogConfig)
	}
	if got.LEDConfig == nil || !got.LEDConfig.ActLEDOff {
		t.Errorf("led_config = %+v, want the one created with", got.LEDConfig)
	}
	if got.DeviceProfile == nil || got.DeviceProfile.ProfileID != "bare-pi" {
		t.Errorf("device_profile = %+v, want the one created with", got.DeviceProfile)
	}
	if got.NetworkFirst == nil || !*got.NetworkFirst || got.BootToOS == nil || *got.BootToOS ||
		got.SSHPasswordAuth == nil || *got.SSHPasswordAuth {
		t.Errorf("flags = %v/%v/%v, want on/off/off", got.NetworkFirst, got.BootToOS, got.SSHPasswordAuth)
	}
	if got.DefaultUsername == nil || *got.DefaultUsername != "pi" {
		t.Errorf("default_username = %v, want pi", got.DefaultUsername)
	}
}

// A value the update refuses is refused at create with the same typed 400,
// and no unit is created.
func TestCreateRefusesAnInvalidPolicyAndCreatesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  organizational_unit_create.OrganizationalUnitCreateCommand
		code string
	}{
		{"watchdog supervising nothing", organizational_unit_create.OrganizationalUnitCreateCommand{
			WatchdogConfig: &node.WatchdogConfig{Enabled: true}}, "invalid_watchdog_config"},
		{"unknown device profile", organizational_unit_create.OrganizationalUnitCreateCommand{
			DeviceProfile: &device_profile.Equipage{ProfileID: "no-such-board"}}, "invalid_device_profile"},
		{"LED source out of range", organizational_unit_create.OrganizationalUnitCreateCommand{
			LEDConfig: &node.LEDConfig{DigitalSources: []string{"no-such-source"}}}, "invalid_led_config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newCreateService()
			tc.cmd.Name = "unit"
			tc.cmd.AuthContext = commands.NewAuthContext(uuid.New(), uuid.New())
			_, err := svc.Create(context.Background(), tc.cmd)
			requireRefusal(t, err, http.StatusBadRequest, tc.code, "")
			if len(repo.stored) != 0 {
				t.Errorf("%d units were created for a refused request", len(repo.stored))
			}
		})
	}
}

// A unit created without a position goes after its siblings, so units created
// in a row keep that order instead of all storing 0. Top-level units are
// siblings of each other; a nested unit counts only its parent's children.
func TestCreateWithoutPositionGoesAfterItsSiblings(t *testing.T) {
	svc, _ := newCreateService()
	ctx := context.Background()
	auth := commands.NewAuthContext(uuid.New(), uuid.New())
	create := func(name string, parent *uuid.UUID) *organizational_unit.OrganizationalUnit {
		t.Helper()
		ou, err := svc.Create(ctx, organizational_unit_create.OrganizationalUnitCreateCommand{
			Name: name, ParentID: parent, AuthContext: auth,
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return ou
	}
	a, b, c := create("a", nil), create("b", nil), create("c", nil)
	if a.Position != 0 || b.Position != 1 || c.Position != 2 {
		t.Fatalf("top-level positions = %d, %d, %d, want 0, 1, 2", a.Position, b.Position, c.Position)
	}
	child1, child2 := create("a1", &a.ID), create("a2", &a.ID)
	if child1.Position != 0 || child2.Position != 1 {
		t.Fatalf("nested positions = %d, %d, want 0, 1", child1.Position, child2.Position)
	}
	if d := create("d", nil); d.Position != 3 {
		t.Fatalf("a fourth top-level unit took position %d, want 3", d.Position)
	}

	// Another account's units are not siblings.
	other, err := svc.Create(ctx, organizational_unit_create.OrganizationalUnitCreateCommand{
		Name: "x", AuthContext: commands.NewAuthContext(uuid.New(), uuid.New()),
	})
	if err != nil {
		t.Fatalf("create in another account: %v", err)
	}
	if other.Position != 0 {
		t.Fatalf("first unit of another account took position %d, want 0", other.Position)
	}
}
