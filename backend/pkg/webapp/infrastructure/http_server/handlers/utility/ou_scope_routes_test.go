package utility

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
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz/authztest"
	utility_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/utility"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

// Every utility route once carried the account-level nil-target gate, so
// an OU-scoped member lost the ENTIRE Utilities surface while
// docs/UTILITIES-PLAN.md §3.8 promises OU-scoped holders operate within their
// scope. These are ROUTE tests over a real enforcer: the door (any-scope
// utility.read), the walls (AllowedOUs narrowing), and the strict mutation
// rules all have to hold on the wire, not in a unit test's imagination.

type routeAssignments struct {
	repository.UtilityAssignmentRepository
	rows []utility.Assignment
}

func (r *routeAssignments) ListByAccount(context.Context, uuid.UUID) ([]utility.Assignment, error) {
	return r.rows, nil
}

func (r *routeAssignments) CountByAccount(context.Context, uuid.UUID) (int64, error) {
	return int64(len(r.rows)), nil
}

func (r *routeAssignments) Create(_ context.Context, a *utility.Assignment) error {
	r.rows = append(r.rows, *a)
	return nil
}

func (r *routeAssignments) GetByID(_ context.Context, _ uuid.UUID, id uuid.UUID) (*utility.Assignment, error) {
	for i := range r.rows {
		if r.rows[i].ID == id {
			copied := r.rows[i]
			return &copied, nil
		}
	}
	return nil, nil
}

type routeNodes struct {
	repository.NodeRepository
	rows []node.Node
}

func (r *routeNodes) GetAllByAccountID(context.Context, uuid.UUID) ([]node.Node, error) {
	return r.rows, nil
}

func (r *routeNodes) GetByID(_ context.Context, _ uuid.UUID, id uuid.UUID) (*node.Node, error) {
	for i := range r.rows {
		if r.rows[i].ID == id {
			copied := r.rows[i]
			return &copied, nil
		}
	}
	return nil, nil
}

type routeOUs struct {
	repository.OrganizationalUnitRepository
}

func (routeOUs) GetAllByAccountID(context.Context, uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	return nil, nil
}

func (routeOUs) GetByID(_ context.Context, accountID, id uuid.UUID) (*organizational_unit.OrganizationalUnit, error) {
	return &organizational_unit.OrganizationalUnit{ID: id, AccountID: accountID}, nil
}

type routeStates struct {
	repository.NodeUtilityStateRepository
}

func (routeStates) ListByNodes(context.Context, uuid.UUID, []uuid.UUID) ([]utility.NodeState, error) {
	return nil, nil
}

func (routeStates) ListByNode(context.Context, uuid.UUID, uuid.UUID) ([]utility.NodeState, error) {
	return nil, nil
}

// utilityRouteHarness mounts the REAL handler and routes over the given
// enforcer, with two units' worth of data behind them.
func utilityRouteHarness(t *testing.T, enforcer *authz_service.Enforcer,
	accountID, userID uuid.UUID, rows []utility.Assignment, nodes []node.Node) *echo.Echo {
	t.Helper()
	recorder, _ := audittest.New()
	svc := utility_service.NewService(
		&utility.Catalog{Utilities: []utility.Definition{{ID: "docker", Name: "Docker", Class: utility.ClassReconciled}}},
		&routeAssignments{rows: rows},
		routeStates{},
		routeOUs{},
		&routeNodes{rows: nodes},
		recorder,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		0, 0,
	)
	h := NewHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e := echo.New()
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("userID", userID.String())
			c.Set("accountID", accountID.String())
			return next(c)
		}
	}
	h.RegisterRoutes(e, auth, enforcer)
	return e
}

func do(e *echo.Echo, method, path string, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestOUScopedMemberKeepsTheUtilitiesSurface(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	myOU, otherOU := uuid.New(), uuid.New()
	myNode := node.Node{ID: uuid.New(), AccountID: accountID, OUID: &myOU}

	mine := utility.Assignment{ID: uuid.New(), AccountID: accountID, UtilityID: "docker", OUIDs: []uuid.UUID{myOU}, Enabled: true}
	foreign := utility.Assignment{ID: uuid.New(), AccountID: accountID, UtilityID: "docker", OUIDs: []uuid.UUID{otherOU}, Enabled: true}
	accountWide := utility.Assignment{ID: uuid.New(), AccountID: accountID, UtilityID: "docker", AccountWide: true, Enabled: true}
	rows := []utility.Assignment{mine, foreign, accountWide}

	enforcer := authztest.ActiveMemberWithOURole(accountID, userID, authz.RoleAdmin, myOU)
	e := utilityRouteHarness(t, enforcer, accountID, userID, rows, []node.Node{myNode})

	t.Run("the list opens and is narrowed to the caller's unit", func(t *testing.T) {
		rec := do(e, http.MethodGet, "/api/v1/utilities", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("list = %d, want 200 — the whole surface was closed to OU-scoped members", rec.Code)
		}
		var envelope struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(envelope.Data) != 1 || envelope.Data[0].ID != mine.ID.String() {
			t.Fatalf("listed %v, want exactly the caller's unit's assignment — an account-wide or foreign row here is enumeration", envelope.Data)
		}
	})

	t.Run("the ladder answers for the caller's unit and refuses another", func(t *testing.T) {
		if rec := do(e, http.MethodGet, "/api/v1/organizational-units/"+myOU.String()+"/utility-ladder", ""); rec.Code != http.StatusOK {
			t.Fatalf("own unit's ladder = %d, want 200", rec.Code)
		}
		if rec := do(e, http.MethodGet, "/api/v1/organizational-units/"+otherOU.String()+"/utility-ladder", ""); rec.Code != http.StatusForbidden {
			t.Fatalf("foreign unit's ladder = %d, want 403", rec.Code)
		}
	})

	t.Run("a foreign assignment cannot be read by id", func(t *testing.T) {
		if rec := do(e, http.MethodGet, "/api/v1/utilities/assignments/"+foreign.ID.String(), ""); rec.Code != http.StatusForbidden {
			t.Fatalf("foreign assignment read = %d, want 403", rec.Code)
		}
	})

	t.Run("an account-wide assignment is account-wide holders' business", func(t *testing.T) {
		if rec := do(e, http.MethodGet, "/api/v1/utilities/assignments/"+accountWide.ID.String(), ""); rec.Code != http.StatusForbidden {
			t.Fatalf("account-wide assignment read = %d, want 403", rec.Code)
		}
		if rec := do(e, http.MethodPost, "/api/v1/utilities/assignments/"+accountWide.ID.String()+"/apply", ""); rec.Code != http.StatusForbidden {
			t.Fatalf("account-wide assignment apply = %d, want 403", rec.Code)
		}
	})

	t.Run("creating inside the caller's unit passes authorization", func(t *testing.T) {
		body := `{"utility_id":"docker","ou_ids":["` + myOU.String() + `"]}`
		rec := do(e, http.MethodPost, "/api/v1/utilities/assignments", body)
		// The stub repository cannot persist, so anything but an authz refusal
		// proves the gate opened for the caller's own unit.
		if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
			t.Fatalf("create in own unit = %d; §3.8 promises OU-scoped holders assign within their scope", rec.Code)
		}
	})

	t.Run("creating an account-wide assignment stays account-wide-only", func(t *testing.T) {
		rec := do(e, http.MethodPost, "/api/v1/utilities/assignments", `{"utility_id":"docker","account_wide":true}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("account-wide create = %d, want 403", rec.Code)
		}
	})

	t.Run("a scope spanning a foreign unit needs EVERY unit", func(t *testing.T) {
		body := `{"utility_id":"docker","ou_ids":["` + myOU.String() + `","` + otherOU.String() + `"]}`
		rec := do(e, http.MethodPost, "/api/v1/utilities/assignments", body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("cross-unit create = %d, want 403 — an assignment spanning two units is a change to both", rec.Code)
		}
	})

	t.Run("the caller's node answers effective-utilities", func(t *testing.T) {
		rec := do(e, http.MethodGet, "/api/v1/nodes/"+myNode.ID.String()+"/effective-utilities", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("own node's effective view = %d, want 200", rec.Code)
		}
	})
}

// The strict cases must not have loosened: no membership and no binding still
// end at the door.
func TestUtilitiesSurfaceStaysGated(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	for _, tc := range []struct {
		name     string
		enforcer *authz_service.Enforcer
		want     int
	}{
		{"no membership row", authztest.NonMember(), http.StatusUnauthorized},
		{"membership disabled", authztest.DisabledMember(), http.StatusUnauthorized},
		{"active member with no binding", authztest.ActiveMemberNoBindings(), http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := utilityRouteHarness(t, tc.enforcer, accountID, userID, nil, nil)
			if rec := do(e, http.MethodGet, "/api/v1/utilities", ""); rec.Code != tc.want {
				t.Fatalf("list = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
