package node

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/instance_anomaly"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

type refusalFakeAnomalyRepo struct {
	rows  []repository.SerialBindingRefusal
	err   error
	asked *uuid.UUID
}

func (f *refusalFakeAnomalyRepo) Record(context.Context, *instance_anomaly.Anomaly) error { return nil }
func (f *refusalFakeAnomalyRepo) ResolveCondition(context.Context, string) error          { return nil }
func (f *refusalFakeAnomalyRepo) ListSerialBindingRefusals(_ context.Context, accountID uuid.UUID, _ int) ([]repository.SerialBindingRefusal, error) {
	f.asked = &accountID
	return f.rows, f.err
}

func refusalService(anomalies *refusalFakeAnomalyRepo, scopes map[string]repository.SerialBindingScope, scopeErr error) *Service {
	s := &Service{
		nodeRepo: &serialScopeFakeRepo{scopes: scopes, err: scopeErr},
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if anomalies != nil {
		s.anomalyRepo = anomalies
	}
	return s
}

// A device can be refused for minutes with nothing on the new account's Add
// Node. The record the guard already writes must come back as the account's
// own refusal, live, and still bound.
func TestListAdoptionRefusalsSurfacesALiveRefusalStillBoundElsewhere(t *testing.T) {
	account := uuid.New()
	now := time.Now()
	anomalies := &refusalFakeAnomalyRepo{rows: []repository.SerialBindingRefusal{{
		HardwareSerial: "10000000ABCD0002", FirstSeenAt: now.Add(-23 * time.Minute), LastSeenAt: now.Add(-2 * time.Second), OccurrenceCount: 690,
	}}}
	svc := refusalService(anomalies, map[string]repository.SerialBindingScope{
		"10000000abcd0002": {InAccount: false, InAnotherAccount: true},
	}, nil)

	got, err := svc.ListAdoptionRefusals(context.Background(), account)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if anomalies.asked == nil || *anomalies.asked != account {
		t.Fatalf("the read must be scoped to the asking account, asked %v", anomalies.asked)
	}
	if len(got) != 1 {
		t.Fatalf("got %d refusals, want 1: %+v", len(got), got)
	}
	r := got[0]
	if r.HardwareSerial != "10000000abcd0002" {
		t.Errorf("serial must be normalized, got %q", r.HardwareSerial)
	}
	if !r.Live || !r.StillBoundElsewhere || r.Occurrences != 690 {
		t.Errorf("got live=%v still_bound=%v occurrences=%d, want true/true/690", r.Live, r.StillBoundElsewhere, r.Occurrences)
	}
}

// A serial this account now holds itself is the device having got in — the
// old account released it and the retry succeeded. Reporting that as a refusal
// is the panel lying in the other direction, so the row is dropped, not marked.
func TestListAdoptionRefusalsDropsASerialThisAccountNowHolds(t *testing.T) {
	now := time.Now()
	anomalies := &refusalFakeAnomalyRepo{rows: []repository.SerialBindingRefusal{
		{HardwareSerial: "1000000000000001", LastSeenAt: now.Add(-time.Hour)},
		{HardwareSerial: "1000000000000002", LastSeenAt: now.Add(-time.Hour)},
	}}
	svc := refusalService(anomalies, map[string]repository.SerialBindingScope{
		"1000000000000001": {InAccount: true, InAnotherAccount: true}, // admitted here since
		"1000000000000002": {InAccount: false, InAnotherAccount: true},
	}, nil)

	got, err := svc.ListAdoptionRefusals(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].HardwareSerial != "1000000000000002" {
		t.Fatalf("want only the still-refused serial, got %+v", got)
	}
}

// Live and StillBoundElsewhere are two different facts and must be reported
// independently: a stale record whose serial has been released is history; a
// live one whose serial has been released means "wait for the next retry".
func TestListAdoptionRefusalsDerivesLiveAndBoundSeparately(t *testing.T) {
	now := time.Now()
	anomalies := &refusalFakeAnomalyRepo{rows: []repository.SerialBindingRefusal{
		{HardwareSerial: "1000000000000001", LastSeenAt: now.Add(-node.AdoptionRefusalLiveWindow - time.Minute)},
		{HardwareSerial: "1000000000000002", LastSeenAt: now.Add(-10 * time.Second)},
	}}
	// Neither serial is held by anyone any more.
	svc := refusalService(anomalies, map[string]repository.SerialBindingScope{}, nil)

	got, err := svc.ListAdoptionRefusals(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if got[0].Live || got[0].StillBoundElsewhere {
		t.Errorf("stale + released: got live=%v bound=%v, want false/false", got[0].Live, got[0].StillBoundElsewhere)
	}
	if !got[1].Live || got[1].StillBoundElsewhere {
		t.Errorf("live + released: got live=%v bound=%v, want true/false", got[1].Live, got[1].StillBoundElsewhere)
	}
}

// The anomaly repository is optional wiring on this service. Without it the
// answer is "no refusals known", never an error — the Add Node sidebar must
// not go red on an install that simply does not record anomalies.
func TestListAdoptionRefusalsWithoutAnAnomalyRepoIsEmpty(t *testing.T) {
	svc := refusalService(nil, nil, nil)
	got, err := svc.ListAdoptionRefusals(context.Background(), uuid.New())
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want empty slice and nil error", got, err)
	}
}

// A failed binding lookup must surface as an error, not as "nothing is bound":
// an empty scope map reads as "released, wait for the retry", which is the
// wrong advice when the truth is unknown.
func TestListAdoptionRefusalsReportsABindingLookupFailure(t *testing.T) {
	anomalies := &refusalFakeAnomalyRepo{rows: []repository.SerialBindingRefusal{{HardwareSerial: "1000000000000001", LastSeenAt: time.Now()}}}
	svc := refusalService(anomalies, nil, errors.New("connection reset"))
	if _, err := svc.ListAdoptionRefusals(context.Background(), uuid.New()); err == nil {
		t.Fatal("expected the lookup failure to be reported")
	}
}
