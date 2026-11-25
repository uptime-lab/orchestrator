package node

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

// countingNodeRepo is the narrow part of the repository this guard uses. It is
// declared here rather than reusing a wider fake because the guard's whole
// contract is "what does COUNT say, and what happens when it fails".
type countingNodeRepo struct {
	count int64
	err   error
	calls int
}

func (r *countingNodeRepo) CountByAccountID(context.Context, uuid.UUID) (int64, error) {
	r.calls++
	return r.count, r.err
}

// quotaService builds just enough Service to exercise assertNodeQuota. The
// helper takes the narrow interface, so the test does not have to satisfy the
// full NodeRepository — which would be forty methods of noise around one.
func quotaService(max int, repo nodeCounter) *Service {
	return &Service{maxNodesPerAccount: max, nodeQuotaCounter: repo, logger: slog.New(slog.DiscardHandler)}
}

func TestNodeQuotaRefusesAtTheCap(t *testing.T) {
	repo := &countingNodeRepo{count: 5}
	err := quotaService(5, repo).assertNodeQuota(context.Background(), uuid.New())
	if !errors.Is(err, app_errors.ErrNodeLimitReached) {
		t.Fatalf("err = %v, want ErrNodeLimitReached", err)
	}
	// The message has to carry the limit, or an operator cannot tell whether
	// they hit a cap or a bug.
	if err != nil && !contains(err.Error(), "max 5") {
		t.Errorf("error message %q does not state the limit", err)
	}
}

func TestNodeQuotaAllowsBelowTheCap(t *testing.T) {
	if err := quotaService(5, &countingNodeRepo{count: 4}).assertNodeQuota(context.Background(), uuid.New()); err != nil {
		t.Fatalf("err = %v, want nil one below the cap", err)
	}
}

// 0 = unlimited, the repo's quota convention — and it must not even ask the
// database, or an unlimited install pays for a COUNT on every node create.
func TestNodeQuotaZeroIsUnlimitedAndDoesNotQuery(t *testing.T) {
	repo := &countingNodeRepo{count: 1_000_000}
	if err := quotaService(0, repo).assertNodeQuota(context.Background(), uuid.New()); err != nil {
		t.Fatalf("err = %v, want nil when the cap is off", err)
	}
	if repo.calls != 0 {
		t.Errorf("counted %d times with the cap off; it should not query at all", repo.calls)
	}
}

// Fails OPEN. A database hiccup must not become "this account can no longer
// adopt hardware" — the cap exists to stop a runaway, and a failed COUNT is not
// what a runaway looks like.
func TestNodeQuotaFailsOpenWhenTheCountFails(t *testing.T) {
	repo := &countingNodeRepo{err: errors.New("database is unhappy")}
	if err := quotaService(1, repo).assertNodeQuota(context.Background(), uuid.New()); err != nil {
		t.Fatalf("err = %v, want nil: the guard must fail open", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
