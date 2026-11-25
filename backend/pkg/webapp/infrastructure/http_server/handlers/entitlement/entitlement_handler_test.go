package entitlement

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz/authztest"
	entitlement_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
)

type emptyRepo struct {
	repository.InstanceEntitlementRepository
}

func (emptyRepo) ListNewest(context.Context, int) ([]entitlement.Record, error) { return nil, nil }

// fixedCount: the install admits 7 devices; every account holds 2 of them.
type fixedCount struct{}

func (fixedCount) CountAdmittedNodes(context.Context) (int64, error) { return 7, nil }
func (fixedCount) CountAdmittedNodesForAccount(context.Context, uuid.UUID) (int64, error) {
	return 2, nil
}

type fixedOperator uuid.UUID

func (o fixedOperator) InstanceOperatorAccountID(context.Context) (uuid.UUID, error) {
	return uuid.UUID(o), nil
}

// testService answers operatorID as the account that operates the install.
func testService(operatorID uuid.UUID) *entitlement_service.Service {
	recorder, _ := audittest.New()
	return entitlement_service.NewService(emptyRepo{}, fixedCount{}, fixedOperator(operatorID), "", "", recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// drive registers the routes behind a pass-through auth, a recording step-up
// and the given enforcer, and sends one request as the instance operator.
func drive(t *testing.T, method string, role authz.Role) (rec *httptest.ResponseRecorder, stepUpRan bool) {
	t.Helper()
	accountID, userID := uuid.New(), uuid.New()
	e := echo.New()
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("accountID", accountID.String())
			c.Set("userID", userID.String())
			return next(c)
		}
	}
	recentAuth := func(echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			stepUpRan = true
			return c.NoContent(http.StatusTeapot)
		}
	}
	h := NewHandler(testService(accountID), quiet())
	h.RegisterRoutes(e, auth, recentAuth, authztest.ActiveMemberWithRole(accountID, userID, role))

	req := httptest.NewRequest(method, "/api/v1/server/entitlement", strings.NewReader(`{"document":"x"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatal("route is not registered")
	}
	return rec, stepUpRan
}

// Applying a file changes what the whole install admits, so the PUT sits
// behind the step-up; the GET is a read and does not. Asserted by driving
// the routes, since Echo does not expose a route's middleware chain.
func TestApplyRequiresStepUpAndGetDoesNot(t *testing.T) {
	rec, stepUpRan := drive(t, http.MethodPut, authz.RoleSuperAdmin)
	if !stepUpRan || rec.Code != http.StatusTeapot {
		t.Errorf("PUT: step-up ran %v, status %d; want the step-up to run", stepUpRan, rec.Code)
	}
	rec, stepUpRan = drive(t, http.MethodGet, authz.RoleSuperAdmin)
	if stepUpRan || rec.Code != http.StatusOK {
		t.Errorf("GET: step-up ran %v, status %d; want 200 with no step-up", stepUpRan, rec.Code)
	}
}

// entitlement.manage is held by no role below super_admin, and the action
// gate runs BEFORE the step-up: an admin is refused without ever being asked
// to confirm.
func TestBothRoutesRefuseAnAdmin(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		rec, stepUpRan := drive(t, method, authz.RoleAdmin)
		if rec.Code != http.StatusForbidden || stepUpRan {
			t.Errorf("%s as admin: status %d, step-up ran %v; want 403 and no step-up", method, rec.Code, stepUpRan)
		}
	}
}

// getAs calls the GET handler as accountID, on an install operated by
// operatorID, and returns the data object.
func getAs(t *testing.T, accountID, operatorID uuid.UUID) map[string]any {
	t.Helper()
	h := NewHandler(testService(operatorID), quiet())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/entitlement", nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("accountID", accountID.String())
	if err := h.Get(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Data
}

// The two shapes of the GET: the operating account sees the full card and
// is told it may apply; any other account's super_admin sees the summary.
func TestGetShapeFollowsTheInstanceOperator(t *testing.T) {
	operator := uuid.New()
	full := getAs(t, operator, operator)
	if full["instance_operator"] != true || full["source"] != "none" {
		t.Errorf("operator's GET = %v, want instance_operator true and the source", full)
	}
	summary := getAs(t, uuid.New(), operator)
	if summary["instance_operator"] != false {
		t.Errorf("tenant's GET = %v, want instance_operator false", summary)
	}
	for _, key := range []string{"source", "customer_id", "kind", "note", "rejections", "device_cap", "issued_at", "applied_at"} {
		if _, ok := summary[key]; ok {
			t.Errorf("tenant's GET carries %q: %v", key, summary)
		}
	}
	if summary["state"] != "free" || summary["account_devices"] != float64(2) ||
		summary["effective_device_cap"] != float64(entitlement.FreeDeviceCap) {
		t.Errorf("tenant's summary lacks the state, its own count or the cap: %v", summary)
	}
}

// A tenant learns how many devices ITS account holds, never how many the
// whole install manages: on a hosted install that number is every other
// customer's fleet added up.
func TestTenantSummaryCarriesOnlyItsOwnCount(t *testing.T) {
	summary := getAs(t, uuid.New(), uuid.New())
	if v, ok := summary["admitted_devices"]; ok {
		t.Errorf("tenant's GET carries the install-wide count admitted_devices=%v: %v", v, summary)
	}
	if summary["account_devices"] != float64(2) {
		t.Errorf("tenant's GET account_devices = %v, want the account's own 2: %v", summary["account_devices"], summary)
	}
}

// A tenant's super_admin is refused the paste with the typed 403, after the
// step-up and before the file is read.
func TestApplyFromAnotherAccountIs403(t *testing.T) {
	h := NewHandler(testService(uuid.New()), quiet())
	req := httptest.NewRequest(http.MethodPut, "/api/v1/server/entitlement", strings.NewReader(`{"document":"entl1.bm90.c2ln"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("accountID", uuid.New().String())
	c.Set("userID", uuid.New().String())
	if err := h.Apply(c); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusForbidden || body.Code != entitlement.NotInstanceOperatorErrorCode {
		t.Fatalf("status %d code %q, want 403 %q: %s", rec.Code, body.Code, entitlement.NotInstanceOperatorErrorCode, rec.Body.String())
	}
}

func TestGetAnswersTheFreeState(t *testing.T) {
	operator := uuid.New()
	h := NewHandler(testService(operator), quiet())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/entitlement", nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("accountID", operator.String())
	if err := h.Get(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data["state"] != "free" || body.Data["free_device_cap"] != float64(entitlement.FreeDeviceCap) ||
		body.Data["effective_device_cap"] != float64(entitlement.FreeDeviceCap) || body.Data["admitted_devices"] != float64(7) ||
		body.Data["account_devices"] != float64(2) {
		t.Errorf("data = %v", body.Data)
	}
	if body.Data["subscription_url"] != entitlement.DefaultSubscriptionURL {
		t.Errorf("subscription_url = %v", body.Data["subscription_url"])
	}
}

// A paste that does not verify is the typed 400 with its code, through the
// shared error path, and never a 500.
func TestApplyAnswersARejectedFileAs400(t *testing.T) {
	operator := uuid.New()
	h := NewHandler(testService(operator), quiet())
	req := httptest.NewRequest(http.MethodPut, "/api/v1/server/entitlement", strings.NewReader(`{"document":"entl1.bm90.c2ln"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("accountID", operator.String())
	c.Set("userID", uuid.New().String())
	if err := h.Apply(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != entitlement.RejectedErrorCode {
		t.Errorf("code = %q", body.Code)
	}
	if !strings.Contains(body.Message, "not accepted") {
		t.Errorf("message = %q", body.Message)
	}
}
