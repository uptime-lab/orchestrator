package account

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_delete"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
)

// deleteRecordingService fails the property under test if the handler ever
// reaches the service's delete.
type deleteRecordingService struct {
	services.AccountService
	deletes int
}

func (s *deleteRecordingService) DeleteAccount(context.Context, account_delete.AccountDeleteCommand) error {
	s.deletes++
	return nil
}

// DELETE /api/v1/accounts/{id} deletes nothing for any caller — including the
// instance operator's super_admin, whose account is the one a delete must
// never remove (it would promote the next-oldest account, a tenant, to
// operator). The route answers 403 and never reaches the service; the
// service's own operator refusal (InstanceOperatorAccountError) is the
// guard for any caller that wires it back.
func TestDeleteAccountRouteDeletesNothing(t *testing.T) {
	svc := &deleteRecordingService{}
	h := NewAccountHandler(svc, 365, slog.New(slog.NewTextHandler(io.Discard, nil)))
	accountID := uuid.New()

	e := echo.New()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/accounts/"+accountID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(accountID.String())
	c.Set("accountID", accountID.String())
	c.Set("userID", uuid.New().String())

	if err := h.DeleteAccount(c); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if svc.deletes != 0 {
		t.Errorf("the route reached the service's delete %d time(s)", svc.deletes)
	}
}

// getAccountService answers GetAccount with a fixed record.
type getAccountService struct {
	services.AccountService
	acc *account.Account
}

func (s *getAccountService) GetAccount(context.Context, uuid.UUID) (*account.Account, error) {
	return s.acc, nil
}

// The account record says how long the account's audit trail is kept: its
// own value when instance administration set one, else the install default.
// It is read-only — the update command carries no such field.
func TestGetAccountSaysHowLongTheAuditTrailIsKept(t *testing.T) {
	ninety := 90
	for name, tc := range map[string]struct {
		own  *int
		want float64
	}{
		"install default": {nil, 365},
		"account's own":   {&ninety, 90},
	} {
		t.Run(name, func(t *testing.T) {
			accountID := uuid.New()
			svc := &getAccountService{acc: &account.Account{ID: accountID, Name: "a", AuditRetentionDays: tc.own}}
			h := NewAccountHandler(svc, 365, slog.New(slog.NewTextHandler(io.Discard, nil)))

			e := echo.New()
			rec := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/accounts/"+accountID.String(), nil), rec)
			c.SetParamNames("id")
			c.SetParamValues(accountID.String())
			c.Set("accountID", accountID.String())

			if err := h.GetAccount(c); err != nil {
				t.Fatalf("handler: %v", err)
			}
			var body struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding %s: %v", rec.Body.String(), err)
			}
			if got := body.Data["audit_retention_days"]; got != tc.want {
				t.Errorf("audit_retention_days = %v, want %v", got, tc.want)
			}
		})
	}
	if _, writable := reflect.TypeOf(account_update.AccountUpdateCommand{}).FieldByName("AuditRetentionDays"); writable {
		t.Error("the account update command carries the audit retention: the tenant API must not write it")
	}
}
