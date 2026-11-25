package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	var out bytes.Buffer
	base := slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(NewContextHandler(base)), &out
}

func decode(t *testing.T, out *bytes.Buffer) map[string]any {
	t.Helper()
	line := strings.TrimSpace(out.String())
	if line == "" {
		t.Fatal("nothing was logged")
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("log line is not JSON: %v (%s)", err, line)
	}
	return record
}

// The whole point: a service line has to be attributable to the request and the
// tenant that caused it, without the ~98 call sites that already pass a context
// changing at all.
func TestAContextAwareCallCarriesTheScope(t *testing.T) {
	logger, out := newTestLogger()
	ctx := WithScope(context.Background(), Scope{
		RequestID: "req-1",
		AccountID: "acct-1",
		UserID:    "user-1",
	})

	logger.WarnContext(ctx, "something happened", "node_id", "n-1")

	record := decode(t, out)
	for key, want := range map[string]string{
		"request_id": "req-1",
		"account_id": "acct-1",
		"user_id":    "user-1",
		"node_id":    "n-1",
	} {
		if got, _ := record[key].(string); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// The request id is stamped before authentication and the tenant only becomes
// known afterwards, so the second WithScope must MERGE. Replacing would erase the
// request id on every authenticated request — the exact opposite of the point.
func TestTheSecondScopeMergesRatherThanReplaces(t *testing.T) {
	ctx := WithScope(context.Background(), Scope{RequestID: "req-1"})
	ctx = WithScope(ctx, Scope{AccountID: "acct-1", UserID: "user-1"})

	scope, ok := ScopeFrom(ctx)
	if !ok {
		t.Fatal("no scope in the context")
	}
	if scope.RequestID != "req-1" || scope.AccountID != "acct-1" || scope.UserID != "user-1" {
		t.Fatalf("merged scope is %+v", scope)
	}
}

// A logger built with .With(...) must keep the wrapper, or the scope silently
// stops appearing for every component that scopes its logger — which is all of
// them (`logger.With("service", "...")` is the house style).
func TestTheScopeSurvivesLoggerWith(t *testing.T) {
	logger, out := newTestLogger()
	scoped := logger.With("service", "node")

	scoped.ErrorContext(
		WithScope(context.Background(), Scope{RequestID: "req-2"}),
		"failed",
	)

	record := decode(t, out)
	if got, _ := record["request_id"].(string); got != "req-2" {
		t.Fatalf("request_id = %q after .With(); the wrapper was lost", got)
	}
	if got, _ := record["service"].(string); got != "node" {
		t.Fatalf("service = %q; the base attributes were lost", got)
	}
}

// WithGroup keeps the wrapper, but the correlation fields land INSIDE the group —
// a handler cannot lift attributes out of an open group through the slog API.
// Recorded as the documented behaviour rather than left to be discovered while
// reading logs: nothing in this codebase calls WithGroup today, and anything that
// starts to must expect `<group>.request_id` instead of `request_id`.
func TestTheScopeNestsUnderAGroupRatherThanDisappearing(t *testing.T) {
	logger, out := newTestLogger()
	grouped := logger.WithGroup("grp")

	grouped.InfoContext(WithScope(context.Background(), Scope{RequestID: "req-3"}), "hi")

	record := decode(t, out)
	group, ok := record["grp"].(map[string]any)
	if !ok {
		t.Fatalf("no group in the record: %v", record)
	}
	if got, _ := group["request_id"].(string); got != "req-3" {
		t.Fatalf("grp.request_id = %q; the wrapper was lost by WithGroup", got)
	}
}

// No scope, or an empty one, must add nothing — a line with empty ids is worse
// than a line without them, because a log query cannot tell "unauthenticated"
// from "not stamped".
func TestNoScopeAddsNothing(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"absent": context.Background(),
		"empty":  WithScope(context.Background(), Scope{}),
	} {
		logger, out := newTestLogger()
		logger.InfoContext(ctx, "plain")

		record := decode(t, out)
		for _, key := range []string{"request_id", "account_id", "user_id"} {
			if _, present := record[key]; present {
				t.Errorf("%s scope: %s was added anyway", name, key)
			}
		}
	}
}

// A partial scope is the unauthenticated case: an id, no tenant.
func TestAPartialScopeAddsOnlyWhatItHas(t *testing.T) {
	logger, out := newTestLogger()

	logger.InfoContext(WithScope(context.Background(), Scope{RequestID: "req-4"}), "public route")

	record := decode(t, out)
	if got, _ := record["request_id"].(string); got != "req-4" {
		t.Fatalf("request_id = %q", got)
	}
	if _, present := record["account_id"]; present {
		t.Error("account_id was added for an unauthenticated request")
	}
}

// A call WITHOUT a context cannot carry the scope — slog gives the handler no way
// to see one. Asserted so the limitation is explicit rather than discovered while
// reading logs during an incident: the ~658 plain call sites stay uncorrelated
// until they are converted.
func TestAPlainCallCannotCarryTheScope(t *testing.T) {
	logger, out := newTestLogger()
	logger.Info("plain call")

	if _, present := decode(t, out)["request_id"]; present {
		t.Error("a context-free call somehow carried a scope")
	}
}

func TestDoubleWrappingIsIdempotent(t *testing.T) {
	base := slog.NewJSONHandler(&bytes.Buffer{}, nil)
	once := NewContextHandler(base)
	twice := NewContextHandler(once)

	if once != twice {
		t.Fatal("wrapping an already-wrapped handler produced a second layer, which would duplicate every attribute")
	}
}

func TestNilBaseHandlerStaysNil(t *testing.T) {
	if NewContextHandler(nil) != nil {
		t.Fatal("wrapping nil produced a handler")
	}
}

func TestScopeFromToleratesANilContext(t *testing.T) {
	//nolint:staticcheck // deliberately passing nil: a logging helper must not be
	// the thing that panics.
	if _, ok := ScopeFrom(nil); ok {
		t.Fatal("reported a scope for a nil context")
	}
}
