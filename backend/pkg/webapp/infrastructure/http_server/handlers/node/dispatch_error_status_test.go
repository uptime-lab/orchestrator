package node

// Every typed dispatch refusal must reach the operator as itself. They are all
// raised at ONE place — command dispatch — and surface through a dozen
// handlers, so the failure mode is a handler with no arm for one of them
// answering 500 "failed to send command" / "failed to provision EEPROM", which
// says the platform broke when in fact it decided — AgentTooOldError on this
// route and WatcherNodeError on provision-eeprom both carry a remedy that such
// an answer would discard.
//
// The structural fix is results.OperatorFacingError — the type declares its own
// status and code, so EVERY site that returns it answers correctly, including
// sites written later. This test drives the real route with each of them and
// asserts two things: the status, and that the operator-facing text survives.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	node_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node"
)

func TestTypedDispatchRefusalsKeepTheirOwnStatus(t *testing.T) {
	nodeID := uuid.New()

	cases := []struct {
		name       string
		err        error
		wantStatus int
		// A phrase from the error that the operator needs: what to do next.
		wantPhrase string
	}{
		{
			name:       "watcher node",
			err:        &node_service.WatcherNodeError{NodeID: nodeID, Slug: "update_boot_order"},
			wantStatus: http.StatusConflict,
			wantPhrase: "power-cycle it into recovery",
		},
		{
			name:       "watcher install the node could not come back from",
			err:        &node_service.WatcherInstallUnsafeError{NodeID: nodeID, Reason: "the boot order reaches NVMe disk before HTTP boot"},
			wantStatus: http.StatusConflict,
			wantPhrase: "provision its EEPROM with a network-first boot order",
		},
		{
			name:       "agent too old",
			err:        &node_service.AgentTooOldError{NodeID: nodeID, Slug: "install_agent (variant=watcher)", Have: "0.6.2", Require: "0.6.3"},
			wantStatus: http.StatusBadRequest,
			wantPhrase: "0.6.3",
		},
		{
			name:       "another command owns the disk",
			err:        &node_service.DeviceBusyError{NodeID: nodeID, Slug: "install_os", ActiveID: uuid.New(), ActiveCmd: "erase_drive"},
			wantStatus: http.StatusConflict,
			wantPhrase: "erase_drive",
		},
		{
			name:       "oversized payload field",
			err:        &node_service.CommandPayloadTooLargeError{Field: "content", Bytes: 9 << 20, Limit: 8 << 20},
			wantStatus: http.StatusBadRequest,
			wantPhrase: "the limit is",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, accountID, userID := refusingHandler(t, tc.err)
			rec := sendHostnameCommand(t, h, accountID, userID)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d — the refusal flattened (body: %s)",
					rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantPhrase) {
				t.Errorf("the response does not carry the operator's next step %q: %s",
					tc.wantPhrase, rec.Body.String())
			}
		})
	}
}

// The route-level arms on this handler and the status the type declares are two
// statements of the same rule, so they are asserted to agree. Without this the
// duplication is free to drift, and a drift shows up as one route answering 409
// while every other answers 400 for the same refusal.
func TestRouteArmsAgreeWithTheDeclaredStatus(t *testing.T) {
	nodeID := uuid.New()
	for _, err := range []results.OperatorFacingError{
		&node_service.WatcherNodeError{NodeID: nodeID, Slug: "reboot"},
		&node_service.WatcherInstallUnsafeError{NodeID: nodeID, Reason: "no http entry"},
		&node_service.AgentTooOldError{NodeID: nodeID, Slug: "read_file", Have: "0.3.0", Require: "0.4.0"},
		&node_service.DeviceBusyError{NodeID: nodeID, Slug: "install_os", ActiveID: uuid.New(), ActiveCmd: "install_os"},
		&node_service.CommandPayloadTooLargeError{Field: "script", Bytes: 1 << 20, Limit: 128 << 10},
	} {
		h, accountID, userID := refusingHandler(t, err)
		rec := sendHostnameCommand(t, h, accountID, userID)
		if rec.Code != err.HTTPStatus() {
			t.Errorf("%T: route answered %d, the type declares %d", err, rec.Code, err.HTTPStatus())
		}
	}
}

// The code a client branches on is asserted where the CENTRAL mapping applies —
// every handler with no arm of its own — in
// results.TestAnOperatorFacingErrorIsAnsweredAsItself. This route predates the
// `code` field and answers its own legacy `{"error": …}` shape, which is left
// alone deliberately: its clients already parse it, and the statuses (asserted
// above) are what changed behaviour.

// The test above drives a route with EXPLICIT arms, which return before the
// central mapping is ever consulted — so it asserts those arms and would pass
// with the central mapping reverted. This one drives a handler that has none:
// mapFileError's default branch hands the error to SendInternalServerError, and
// the status therefore comes from the type. It is the actual regression guard.
//
// The pairing matters beyond the test: a Watcher node whose operator tracks a
// file is the same failure mode as provision-eeprom, on a different route.
func TestAHandlerWithNoArmOfItsOwnStillAnswersTheDeclaredStatus(t *testing.T) {
	h := &NodeHandler{}

	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantPhrase string
	}{
		{
			name:       "watcher node tracking a file",
			err:        &node_service.WatcherNodeError{NodeID: uuid.New(), Slug: "read_file"},
			wantStatus: http.StatusConflict,
			wantPhrase: "install the full agent",
		},
		{
			name:       "another command owns the disk",
			err:        &node_service.DeviceBusyError{NodeID: uuid.New(), Slug: "read_file", ActiveID: uuid.New(), ActiveCmd: "install_os"},
			wantStatus: http.StatusConflict,
			wantPhrase: "install_os",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/x", nil), rec)

			if err := h.mapFileError(c, tc.err); err != nil {
				t.Fatalf("mapper returned an error instead of a response: %v", err)
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d — the refusal flattened into the generic 500 (body: %s)",
					rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantPhrase) {
				t.Errorf("the response lost the operator's next step %q: %s", tc.wantPhrase, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"code"`) {
				t.Errorf("the response carries no machine-readable code: %s", rec.Body.String())
			}
		})
	}
}

// An ordinary failure on the same route keeps answering 500 with the handler's
// own words and echoes nothing of the underlying error.
func TestAHandlerWithNoArmStillHidesAnOrdinaryError(t *testing.T) {
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/x", nil), rec)
	h := &NodeHandler{}

	if err := h.mapFileError(c, errors.New(`pq: relation "node_files" does not exist`)); err != nil {
		t.Fatalf("mapper returned an error instead of a response: %v", err)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "node_files") {
		t.Errorf("the underlying error leaked: %s", rec.Body.String())
	}
}
