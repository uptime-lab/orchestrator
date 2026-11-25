package node_ssh_adopt

// Outbound SSH work is bounded server-wide AND per account,
// before any DNS lookup, key decryption or dial, so one tenant cannot hold
// every slot and a refusal costs the server nothing.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_adoption_job"
)

// countingResolver answers like fakeResolver and counts lookups: a capacity
// refusal must land before DNS is asked.
type countingResolver struct {
	inner fakeResolver
	calls atomic.Int32
}

func (r *countingResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	r.calls.Add(1)
	return r.inner.LookupIPAddr(ctx, host)
}

// hold takes n slots on the gate for the account and returns their releases.
func hold(t *testing.T, g *capacityGate, accountID uuid.UUID, n int) []func() {
	t.Helper()
	releases := make([]func(), 0, n)
	for i := 0; i < n; i++ {
		release, err := g.acquire(accountID)
		if err != nil {
			t.Fatalf("acquire %d: %v", i+1, err)
		}
		releases = append(releases, release)
	}
	return releases
}

func releaseAll(rs []func()) {
	for _, r := range rs {
		r()
	}
}

// ---- adoption -------------------------------------------------------------------

func TestAdoptionCapacityIsRefusedBeforeAJobExists(t *testing.T) {
	h := newHarness(t, nil)
	// Fill the server-wide bound from other accounts so the refusal is the
	// global one, not this account's share.
	var held []func()
	for len(held) < maxConcurrentAdoptions {
		held = append(held, hold(t, h.svc.adoptGate, uuid.New(), 1)...)
	}
	_, err := h.svc.StartAdoption(context.Background(), h.adoptCmd())
	var capacity *AdoptionCapacityError
	if !errors.As(err, &capacity) {
		t.Fatalf("err = %v, want AdoptionCapacityError", err)
	}
	if capacity.Limit != maxConcurrentAdoptions || capacity.PerAccount {
		t.Errorf("refusal = %+v, want the server-wide limit", capacity)
	}
	if h.jobs.count() != 0 {
		t.Error("a job row was created for a refused adoption")
	}
	if len(h.ssh.Dials()) != 0 {
		t.Error("a dial happened for a refused adoption")
	}

	// Release one slot: the next request gets it, and the run gives it back.
	held[0]()
	job, err := h.svc.StartAdoption(context.Background(), h.adoptCmd())
	if err != nil {
		t.Fatalf("StartAdoption after a release: %v", err)
	}
	h.waitJob(t, job.ID)
	if h.svc.adoptGate.inFlight(h.actor.AccountID) != 0 {
		t.Error("the finished run did not release its per-account slot")
	}
	if h.svc.adoptGate.globalInFlight() != maxConcurrentAdoptions-1 {
		t.Errorf("global in flight = %d, want the %d still held by others", h.svc.adoptGate.globalInFlight(), maxConcurrentAdoptions-1)
	}
	releaseAll(held[1:])
}

func TestAdoptionPerAccountShareKeepsSlotsForOtherTenants(t *testing.T) {
	h := newHarness(t, nil)
	held := hold(t, h.svc.adoptGate, h.actor.AccountID, maxAdoptionsPerAccount)
	defer releaseAll(held)

	_, err := h.svc.StartAdoption(context.Background(), h.adoptCmd())
	var capacity *AdoptionCapacityError
	if !errors.As(err, &capacity) {
		t.Fatalf("err = %v, want AdoptionCapacityError", err)
	}
	if !capacity.PerAccount || capacity.Limit != maxAdoptionsPerAccount {
		t.Errorf("refusal = %+v, want the per-account share", capacity)
	}
	if !strings.Contains(strings.ToLower(capacity.Error()), "your account") {
		t.Errorf("message %q should say it is this account's bound", capacity.Error())
	}

	// Another tenant still adopts.
	other := h.adoptCmd()
	other.AuthContext = commands.NewAuthContext(uuid.New(), uuid.New())
	job, err := h.svc.StartAdoption(context.Background(), other)
	if err != nil {
		t.Fatalf("another account's StartAdoption: %v", err)
	}
	h.waitJob(t, job.ID)
}

func TestAdoptionSlotIsReleasedWhenTheJobRowCannotBeWritten(t *testing.T) {
	h := newHarness(t, nil)
	h.svc.jobRepo = &failingJobRepo{memJobRepo: newMemJobRepo()}
	if _, err := h.svc.StartAdoption(context.Background(), h.adoptCmd()); err == nil {
		t.Fatal("StartAdoption succeeded with a repo that cannot write")
	}
	if h.svc.adoptGate.globalInFlight() != 0 || h.svc.adoptGate.inFlight(h.actor.AccountID) != 0 {
		t.Error("a slot leaked past a failed job write")
	}
}

type failingJobRepo struct{ *memJobRepo }

func (*failingJobRepo) Create(context.Context, *node_adoption_job.Job) error {
	return errors.New("database is away")
}

// ---- preflight ------------------------------------------------------------------

func TestPreflightCapacityIsRefusedBeforeDNSKeyLoadingOrDial(t *testing.T) {
	h := newHarness(t, func(c *config.SSHAdoptConfig) { c.AllowedHostCIDRs = []string{"10.0.0.0/8"} })
	resolver := &countingResolver{inner: fakeResolver{hosts: map[string][]string{"pi.lan": {"10.0.0.7"}}}}
	h.svc.resolver = resolver
	var held []func()
	for len(held) < maxConcurrentPreflights {
		held = append(held, hold(t, h.svc.preflightGate, uuid.New(), 1)...)
	}
	defer releaseAll(held)

	cmd := h.preflightCmd()
	cmd.Host = "pi.lan"
	cmd.Password = ""
	keyID := uuid.New()
	cmd.SSHKeyID = &keyID
	_, err := h.svc.Preflight(context.Background(), cmd)
	var capacity *PreflightCapacityError
	if !errors.As(err, &capacity) {
		t.Fatalf("err = %v, want PreflightCapacityError", err)
	}
	if capacity.PerAccount || capacity.Limit != maxConcurrentPreflights {
		t.Errorf("refusal = %+v, want the server-wide limit", capacity)
	}
	if capacity.HTTPStatus() != http.StatusTooManyRequests || capacity.ErrorCode() != "preflight_capacity" {
		t.Errorf("status/code = %d/%s", capacity.HTTPStatus(), capacity.ErrorCode())
	}
	if n := resolver.calls.Load(); n != 0 {
		t.Errorf("DNS was asked %d times for a refused preflight", n)
	}
	if n := h.keys.calls.Load(); n != 0 {
		t.Errorf("the connection key was decrypted %d times for a refused preflight", n)
	}
	if len(h.ssh.Dials()) != 0 {
		t.Error("a dial happened for a refused preflight")
	}
}

func TestPreflightPerAccountShareKeepsSlotsForOtherTenants(t *testing.T) {
	h := newHarness(t, nil)
	held := hold(t, h.svc.preflightGate, h.actor.AccountID, maxPreflightsPerAccount)
	defer releaseAll(held)

	_, err := h.svc.Preflight(context.Background(), h.preflightCmd())
	var capacity *PreflightCapacityError
	if !errors.As(err, &capacity) {
		t.Fatalf("err = %v, want PreflightCapacityError", err)
	}
	if !capacity.PerAccount || capacity.Limit != maxPreflightsPerAccount {
		t.Errorf("refusal = %+v, want the per-account share", capacity)
	}

	other := h.preflightCmd()
	other.AuthContext = commands.NewAuthContext(uuid.New(), uuid.New())
	res, err := h.svc.Preflight(context.Background(), other)
	if err != nil || !res.Reachable {
		t.Fatalf("another account's Preflight = %+v, %v", res, err)
	}
}

func TestPreflightReleasesItsSlotOnEveryExitPath(t *testing.T) {
	h := newHarness(t, func(c *config.SSHAdoptConfig) { c.AllowedHostCIDRs = []string{"10.0.0.0/8"} })
	h.svc.resolver = fakeResolver{hosts: map[string][]string{"pi.lan": {"10.0.0.7"}, "evil.lan": {"8.8.8.8"}}}

	check := func(name string) {
		t.Helper()
		if g := h.svc.preflightGate.globalInFlight(); g != 0 {
			t.Errorf("after %s: %d server-wide preflight slots still held", name, g)
		}
		if a := h.svc.preflightGate.inFlight(h.actor.AccountID); a != 0 {
			t.Errorf("after %s: %d per-account preflight slots still held", name, a)
		}
	}

	h.preflightOK(t)
	check("a reachable probe")

	h.ssh.setDialErr(errors.New("connection refused"))
	if _, err := h.svc.Preflight(context.Background(), h.preflightCmd()); err != nil {
		t.Fatal(err)
	}
	check("an unreachable probe")
	h.ssh.setDialErr(nil)

	changed := h.preflightCmd()
	changed.ExpectedHostKeyFingerprint = otherKey
	if _, err := h.svc.Preflight(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	check("a host-key-changed probe")

	outside := h.preflightCmd()
	outside.Host = "evil.lan"
	if _, err := h.svc.Preflight(context.Background(), outside); err == nil {
		t.Fatal("an out-of-allowlist host was probed")
	}
	check("an allowlist refusal")

	agent := h.preflightCmd()
	agent.Password = ""
	agent.UseAgent = true
	if _, err := h.svc.Preflight(context.Background(), agent); err == nil {
		t.Fatal("agent auth ran while disabled")
	}
	check("an agent-auth refusal")
}

func TestCapacityErrorsAnswer429ThroughTheGenericMapper(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{&AdoptionCapacityError{Limit: 4}, "adoption_capacity"},
		{&PreflightCapacityError{Limit: 8, PerAccount: true}, "preflight_capacity"},
	} {
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil), rec)
		if err := results.SendInternalServerError(c, "failed", tc.err); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusTooManyRequests {
			t.Errorf("%T: status = %d, want 429", tc.err, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), tc.code) {
			t.Errorf("%T: body = %s, want %s", tc.err, rec.Body.String(), tc.code)
		}
	}
}
