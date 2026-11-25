package results

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	entitlement_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
)

func entitlementSnapshot() entitlement_service.Snapshot {
	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	return entitlement_service.Snapshot{
		State:  entitlement_service.StateActive,
		Source: entitlement_service.SourceFile,
		Document: &entitlement.Document{Version: entitlement.Version, CustomerID: "the-operators-contract",
			DeviceCap: 0, IssuedAt: issued, ExpiresAt: expires, Kind: entitlement.KindHosted, GraceDays: 30,
			Note: "operator's note"},
		EffectiveCap:         0,
		AdmittedCount:        77,
		AccountAdmittedCount: 5,
		FreeCap:              entitlement.FreeDeviceCap,
		GraceUntil:           expires.AddDate(0, 0, 30),
		SubscriptionURL:      entitlement.DefaultSubscriptionURL,
		Rejections:           []entitlement_service.Rejection{{Source: entitlement_service.SourceDatabase, Reason: "file applied: bad signature"}},
	}
}

// A tenant of a hosted install sees what the cap means for it — state, the
// caps, the count, expiry and grace — and nothing of the operator's
// contract or of files other accounts offered.
func TestEntitlementSummaryCarriesNoTerms(t *testing.T) {
	raw, err := json.Marshal(NewEntitlementResult(entitlementSnapshot(), false))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, leaked := range []string{"the-operators-contract", "operator's note", "hosted", "bad signature",
		`"source"`, `"customer_id"`, `"kind"`, `"note"`, `"rejections"`, `"issued_at"`, `"device_cap"`} {
		if strings.Contains(body, leaked) {
			t.Errorf("summary carries %s: %s", leaked, body)
		}
	}
	if strings.Contains(body, `"admitted_devices"`) {
		t.Errorf("summary carries the install-wide count: %s", body)
	}
	for _, kept := range []string{`"instance_operator":false`, `"state":"active"`, `"account_devices":5`,
		`"effective_device_cap":0`, `"grace_until"`, `"expires_at"`} {
		if !strings.Contains(body, kept) {
			t.Errorf("summary lacks %s: %s", kept, body)
		}
	}
}

func TestEntitlementFullCardForTheOperator(t *testing.T) {
	out := NewEntitlementResult(entitlementSnapshot(), true)
	if !out.InstanceOperator || out.CustomerID != "the-operators-contract" || out.Source != "file" ||
		out.Note != "operator's note" || len(out.Rejections) != 1 {
		t.Errorf("operator's card = %+v", out)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{`"admitted_devices":77`, `"account_devices":5`} {
		if !strings.Contains(string(raw), kept) {
			t.Errorf("operator's card lacks %s: %s", kept, raw)
		}
	}
}

// An unreadable count is -1 on both shapes, never 0, and the summary still
// carries no install-wide field.
func TestEntitlementUnreadableCounts(t *testing.T) {
	snap := entitlementSnapshot()
	snap.AdmittedCount, snap.AccountAdmittedCount = -1, -1
	full, _ := json.Marshal(NewEntitlementResult(snap, true))
	if !strings.Contains(string(full), `"admitted_devices":-1`) || !strings.Contains(string(full), `"account_devices":-1`) {
		t.Errorf("operator's card = %s", full)
	}
	summary, _ := json.Marshal(NewEntitlementResult(snap, false))
	if strings.Contains(string(summary), `"admitted_devices"`) || !strings.Contains(string(summary), `"account_devices":-1`) {
		t.Errorf("summary = %s", summary)
	}
}

// A file signing a positive cap below the free cap is enforced as the free
// cap. The operator's card shows both numbers and says why they differ; a
// tenant's summary shows only the cap in force, and neither the signed value
// nor the flag that would describe it.
func TestEntitlementBelowFloorCard(t *testing.T) {
	snap := entitlementSnapshot()
	snap.Document.DeviceCap = 10
	snap.Document.Kind = entitlement.KindSubscription
	snap.EffectiveCap = entitlement.FreeDeviceCap
	snap.SignedCapBelowFloor = true

	full := NewEntitlementResult(snap, true)
	if full.DeviceCap == nil || *full.DeviceCap != 10 || full.EffectiveDeviceCap == nil || *full.EffectiveDeviceCap != entitlement.FreeDeviceCap ||
		!full.DeviceCapBelowFreeFloor {
		t.Errorf("operator's card = %+v, want signed 10, effective %d and the below-floor flag", full, entitlement.FreeDeviceCap)
	}

	raw, err := json.Marshal(NewEntitlementResult(snap, false))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, fmt.Sprintf(`"effective_device_cap":%d`, entitlement.FreeDeviceCap)) {
		t.Errorf("summary lacks the effective cap: %s", body)
	}
	for _, leaked := range []string{`"device_cap"`, `"device_cap_below_free_floor"`} {
		if strings.Contains(body, leaked) {
			t.Errorf("summary carries %s: %s", leaked, body)
		}
	}
}

// The PUT answer names the pasted file in "applied" with the exact contract
// the SPA reads; a GET (no Applied on the snapshot) carries no such key, and
// neither does the tenant summary.
func TestEntitlementAppliedIsOnThePutAnswerOnly(t *testing.T) {
	snap := entitlementSnapshot()
	for _, operator := range []bool{true, false} {
		raw, err := json.Marshal(NewEntitlementResult(snap, operator))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"applied"`) {
			t.Errorf("a read (operator=%v) carries applied: %s", operator, raw)
		}
	}

	snap.Applied = &entitlement_service.AppliedFile{
		Document: &entitlement.Document{CustomerID: "older", DeviceCap: 25,
			IssuedAt:  time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC),
			ExpiresAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)},
		State:   entitlement_service.StateLapsed,
		InForce: false,
	}
	raw, err := json.Marshal(NewEntitlementResult(snap, true))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Applied map[string]any `json:"applied"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"in_force":   false,
		"issued_at":  "2025-06-01T00:00:00Z",
		"expires_at": "2026-06-01T00:00:00Z",
		"device_cap": float64(25),
		"state":      "lapsed",
	}
	if fmt.Sprint(body.Applied) != fmt.Sprint(want) {
		t.Errorf("applied = %v, want %v", body.Applied, want)
	}
	if summary, _ := json.Marshal(NewEntitlementResult(snap, false)); strings.Contains(string(summary), `"applied"`) {
		t.Errorf("the tenant summary carries applied: %s", summary)
	}
}

// An account the per-account limit binds sees its OWN limit, plan, end date,
// count and the contact — and nothing of the install's entitlement: not its
// state, caps, expiry, grace, subscription link or terms, which before this
// mode reached a tenant as the summary and read as the tenant's own
// subscription.
func TestEntitlementAccountLimitShapeCarriesNoInstanceTerms(t *testing.T) {
	until := time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC)
	for name, view := range map[string]entitlement_service.AccountLimitView{
		"free": {Known: true, Limit: entitlement.AccountLimit{Limit: 5, Plan: entitlement.AccountLimitPlanFree,
			Contact: "sales@example.com"}},
		"subscription": {Known: true, Limit: entitlement.AccountLimit{Limit: 50,
			Plan: entitlement.AccountLimitPlanSubscription, Until: &until, Contact: "sales@example.com"}},
	} {
		t.Run(name, func(t *testing.T) {
			snap := entitlementSnapshot()
			v := view
			snap.AccountLimit = &v
			raw, err := json.Marshal(NewEntitlementResult(snap, false))
			if err != nil {
				t.Fatal(err)
			}
			body := string(raw)
			for _, leaked := range []string{"the-operators-contract", "2027-01-01", "2027-01-31", `"state"`,
				`"expires_at"`, `"grace_until"`, `"effective_device_cap"`, `"free_device_cap"`,
				`"subscription_url"`, `"admitted_devices"`, `"customer_id"`, `"kind"`, `"rejections"`} {
				if strings.Contains(body, leaked) {
					t.Errorf("the account-limit card carries %s: %s", leaked, body)
				}
			}
			kept := []string{`"mode":"account_limit"`, `"instance_operator":false`, `"account_devices":5`,
				fmt.Sprintf(`"account_device_limit":%d`, v.Limit.Limit), `"account_plan":"` + name + `"`,
				`"account_limit_contact":"sales@example.com"`}
			if name == "subscription" {
				kept = append(kept, `"account_plan_until":"2027-06-30T00:00:00Z"`)
			} else if strings.Contains(body, "account_plan_until") {
				t.Errorf("the free plan carries a date: %s", body)
			}
			for _, k := range kept {
				if !strings.Contains(body, k) {
					t.Errorf("the account-limit card lacks %s: %s", k, body)
				}
			}
		})
	}
}

// The operator's card is never the account-limit shape, and an unknown limit
// is -1 with no plan.
func TestEntitlementAccountLimitShapeEdges(t *testing.T) {
	snap := entitlementSnapshot()
	snap.AccountLimit = &entitlement_service.AccountLimitView{Known: true, Limit: entitlement.AccountLimit{Limit: 5}}
	if out := NewEntitlementResult(snap, true); out.Mode != EntitlementModeInstance || out.CustomerID == "" {
		t.Errorf("the operator got %+v, want the full instance card", out)
	}
	snap.AccountLimit = &entitlement_service.AccountLimitView{Known: false}
	out := NewEntitlementResult(snap, false)
	if out.AccountDeviceLimit == nil || *out.AccountDeviceLimit != -1 || out.AccountPlan != "" {
		t.Errorf("an unknown limit answered %+v", out)
	}
	if out := NewEntitlementResult(entitlementSnapshot(), false); out.Mode != EntitlementModeInstance {
		t.Errorf("no account limit answered mode %q", out.Mode)
	}
}
