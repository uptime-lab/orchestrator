package entitlement

import (
	"strings"
	"testing"
	"time"
)

func TestValidAccountLimitContact(t *testing.T) {
	for _, ok := range []string{"", "sales@example.com", "Write to sales@example.com"} {
		if !ValidAccountLimitContact(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"a\nb", "tab\there", " padded", strings.Repeat("a", MaxAccountLimitContactLength+1)} {
		if ValidAccountLimitContact(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The refusal says which limit applies and carries only the account's own
// numbers, the plan and, for a dated subscription, its end.
func TestAccountDeviceLimitErrorDetails(t *testing.T) {
	until := time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC)
	e := &AccountDeviceLimitError{Limit: 50, Count: 50, Plan: AccountLimitPlanSubscription, Until: &until,
		Contact: "sales@example.com"}
	d := e.ErrorDetails().(map[string]any)
	if d["limit"] != 50 || d["account_devices"] != int64(50) || d["plan"] != "subscription" ||
		d["until"] != "2027-06-30T00:00:00Z" || d["contact"] != "sales@example.com" {
		t.Errorf("details = %v", d)
	}
	if _, ok := d["count"]; ok {
		t.Error("the refusal carries an install-wide count field")
	}
	if e.ErrorCode() != AccountDeviceLimitErrorCode || e.HTTPStatus() != 409 {
		t.Errorf("code %s status %d", e.ErrorCode(), e.HTTPStatus())
	}
	if !strings.Contains(e.Error(), "sales@example.com") || !strings.Contains(e.Error(), "keep working") {
		t.Errorf("message = %q", e.Error())
	}
	free := (&AccountDeviceLimitError{Limit: 5, Count: 12, Plan: AccountLimitPlanFree}).ErrorDetails().(map[string]any)
	if _, ok := free["until"]; ok {
		t.Error("the free plan carries a date")
	}
}
