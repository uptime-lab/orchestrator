package entitlement

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz/authztest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// ticketVerifier is the reauth service's contract at its narrowest: one
// ticket value is a recent confirmation, anything else is not.
type ticketVerifier struct {
	valid string
	asked int
}

func (v *ticketVerifier) EnsureRecentAuth(_ context.Context, _ uuid.UUID, ticket string, _ services.SessionProof) error {
	v.asked++
	if ticket != "" && ticket == v.valid {
		return nil
	}
	return app_errors.ErrReauthRequired
}

// Applying a file changes what the whole install admits, so the PUT sits
// behind the step-up — proven here with the REAL middleware the router
// builds (middleware.RequireRecentAuth), not a stand-in: with no ticket the
// PUT answers 403 reauth_required and never reaches the paste, with a ticket
// it reaches the handler, and the GET never asks.
func TestEntitlementPutRequiresTheRealStepUp(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	verifier := &ticketVerifier{valid: "fresh-ticket"}

	e := echo.New()
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("accountID", accountID.String())
			c.Set("userID", userID.String())
			return next(c)
		}
	}
	h := NewHandler(testService(accountID), quiet())
	h.RegisterRoutes(e, auth, middleware.RequireRecentAuth(verifier, quiet()),
		authztest.ActiveMemberWithRole(accountID, userID, authz.RoleSuperAdmin))

	var lastError string
	send := func(method, ticket string) (*httptest.ResponseRecorder, string) {
		req := httptest.NewRequest(method, "/api/v1/server/entitlement", strings.NewReader(`{"document":"entl1.bm90.c2ln"}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		if ticket != "" {
			req.Header.Set(middleware.ReauthTicketHeader, ticket)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		var body struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		lastError = body.Message
		return rec, body.Code
	}

	rec, code := send(http.MethodPut, "")
	if rec.Code != http.StatusForbidden || code != middleware.ReauthCodeRequired {
		t.Fatalf("PUT without a ticket: %d %q, want 403 %q", rec.Code, code, middleware.ReauthCodeRequired)
	}
	// The step-up guards more than sign-in changes, so the sentence shown
	// with the prompt must fit this route too.
	if lastError != "confirm your identity to continue" {
		t.Errorf("PUT without a ticket says %q, want the neutral step-up sentence", lastError)
	}
	rec, code = send(http.MethodPut, "stale-ticket")
	if rec.Code != http.StatusForbidden || code != middleware.ReauthCodeRequired {
		t.Fatalf("PUT with a ticket that does not verify: %d %q, want 403 %q", rec.Code, code, middleware.ReauthCodeRequired)
	}
	// Past the step-up the handler runs: this paste does not verify, which
	// is its own typed 400.
	rec, code = send(http.MethodPut, "fresh-ticket")
	if rec.Code != http.StatusBadRequest || code != entitlement.RejectedErrorCode {
		t.Fatalf("PUT with a fresh ticket: %d %q, want the handler's 400 %q", rec.Code, code, entitlement.RejectedErrorCode)
	}

	asked := verifier.asked
	rec, _ = send(http.MethodGet, "")
	if rec.Code != http.StatusOK || verifier.asked != asked {
		t.Errorf("GET: status %d, step-up asked %d more time(s); want 200 and no step-up", rec.Code, verifier.asked-asked)
	}
}
