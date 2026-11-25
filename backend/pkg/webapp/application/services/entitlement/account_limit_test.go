package entitlement

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
)

// overrideReader answers one account's override, or an error.
type overrideReader struct {
	o     entitlement.AccountLimitOverride
	err   error
	calls int
}

func (r *overrideReader) DeviceLimitOverride(context.Context, uuid.UUID) (entitlement.AccountLimitOverride, error) {
	r.calls++
	return r.o, r.err
}

func intp(n int) *int { return &n }

// The override's whole lifecycle, judged on the service's own clock at each
// read, with no job in between: in force before its end, gone after it (no
// grace), forever without one, and an override below the default binds too.
func TestAccountLimitFollowsTheOverrideAndItsEnd(t *testing.T) {
	h := newHarness(t, "")
	end := h.clock.Add(182 * 24 * time.Hour)
	tenant := uuid.New()

	cases := []struct {
		name      string
		override  entitlement.AccountLimitOverride
		at        time.Time
		wantLimit int
		wantPlan  entitlement.AccountLimitPlan
		wantUntil bool
	}{
		{"no override is the default", entitlement.AccountLimitOverride{}, h.clock, 5, entitlement.AccountLimitPlanFree, false},
		{"dated override in force", entitlement.AccountLimitOverride{Limit: intp(50), Until: &end}, h.clock, 50, entitlement.AccountLimitPlanSubscription, true},
		{"dated override one second before its end", entitlement.AccountLimitOverride{Limit: intp(50), Until: &end}, end.Add(-time.Second), 50, entitlement.AccountLimitPlanSubscription, true},
		{"dated override at its end falls back", entitlement.AccountLimitOverride{Limit: intp(50), Until: &end}, end, 5, entitlement.AccountLimitPlanFree, false},
		{"dated override after its end falls back", entitlement.AccountLimitOverride{Limit: intp(50), Until: &end}, end.Add(48 * time.Hour), 5, entitlement.AccountLimitPlanFree, false},
		{"no end is forever", entitlement.AccountLimitOverride{Limit: intp(50)}, h.clock.Add(100 * 365 * 24 * time.Hour), 50, entitlement.AccountLimitPlanSubscription, false},
		{"lower than the default", entitlement.AccountLimitOverride{Limit: intp(3)}, h.clock, 3, entitlement.AccountLimitPlanSubscription, false},
		{"zero is no limit for the account", entitlement.AccountLimitOverride{Limit: intp(0)}, h.clock, 0, entitlement.AccountLimitPlanSubscription, false},
		{"an end without a limit is the default", entitlement.AccountLimitOverride{Until: &end}, h.clock, 5, entitlement.AccountLimitPlanFree, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reader := &overrideReader{o: c.override}
			h.svc.SetAccountDeviceLimit(5, "sales@example.com", reader)
			h.clock = c.at
			got, err := h.svc.AccountDeviceLimit(context.Background(), tenant)
			if err != nil {
				t.Fatal(err)
			}
			if got.Limit != c.wantLimit || got.Plan != c.wantPlan || (got.Until != nil) != c.wantUntil {
				t.Errorf("limit %+v, want %d %s until=%t", got, c.wantLimit, c.wantPlan, c.wantUntil)
			}
			if got.Contact != "sales@example.com" {
				t.Errorf("contact = %q", got.Contact)
			}
		})
	}
}

// Off by default: ACCOUNT_DEVICE_LIMIT=0 binds nobody and reads nothing, so
// an install that does not use it behaves as before it existed — even when
// an override is written.
func TestAccountLimitIsOffAtZero(t *testing.T) {
	h := newHarness(t, "")
	reader := &overrideReader{o: entitlement.AccountLimitOverride{Limit: intp(1)}}
	h.svc.SetAccountDeviceLimit(0, "sales@example.com", reader)
	got, err := h.svc.AccountDeviceLimit(context.Background(), uuid.New())
	if err != nil || got.Applies() {
		t.Errorf("limit %+v, %v; want none", got, err)
	}
	if reader.calls != 0 {
		t.Errorf("the override was read %d time(s) with the limit off", reader.calls)
	}
	if snap := h.svc.CurrentFor(context.Background(), uuid.New()); snap.AccountLimit != nil {
		t.Errorf("the card carries an account limit with the limit off: %+v", snap.AccountLimit)
	}
}

// The account that operates the install is not a tenant: no per-account
// limit binds it, and its card stays the operator's.
func TestTheInstanceOperatorIsNotBoundByTheAccountLimit(t *testing.T) {
	h := newHarness(t, "")
	h.svc.SetAccountDeviceLimit(5, "", &overrideReader{})
	got, err := h.svc.AccountDeviceLimit(context.Background(), h.operator.id)
	if err != nil || got.Applies() {
		t.Errorf("operator's limit %+v, %v; want none", got, err)
	}
	if snap := h.svc.CurrentFor(context.Background(), h.operator.id); snap.AccountLimit != nil {
		t.Error("the operator's card carries a per-account limit")
	}

	// A lookup that fails is a tenant: the limit binds rather than exempts.
	h.operator.err = errors.New("db down")
	if got, _ := h.svc.AccountDeviceLimit(context.Background(), h.operator.id); got.Limit != 5 {
		t.Errorf("an unconfirmed operator got limit %d, want the default 5", got.Limit)
	}
}

// An override that cannot be read is an error for admission (which refuses)
// and an unknown limit on the card, never the default shown as the answer.
func TestAnUnreadableOverride(t *testing.T) {
	h := newHarness(t, "")
	h.svc.SetAccountDeviceLimit(5, "", &overrideReader{err: errors.New("timeout")})
	tenant := uuid.New()
	if _, err := h.svc.AccountDeviceLimit(context.Background(), tenant); err == nil {
		t.Error("an unreadable override answered a limit")
	}
	snap := h.svc.CurrentFor(context.Background(), tenant)
	if snap.AccountLimit == nil || snap.AccountLimit.Known {
		t.Errorf("card view = %+v, want an unknown account limit", snap.AccountLimit)
	}
}
