package node_activity_list

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
)

func base() NodeActivityListQuery {
	return NodeActivityListQuery{AuthContext: commands.NewAuthContext(uuid.New(), uuid.New())}
}

// An ignored filter returns everything, and a client that asked for failures
// reads the whole log as failed. Refusing is the only honest answer.
func TestUnknownStatusIsRefused(t *testing.T) {
	q := base()
	q.Statuses = []string{"failed", "borked"}
	if err := q.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want a refusal naming the unknown status")
	}

	q.Statuses = []string{"failed", "skipped", "expired", "superseded", "registered", "sent", "in_progress", "success"}
	if err := q.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil for the real lifecycle", err)
	}
}

func TestPaginationIsNormalizedToTheEffectiveValues(t *testing.T) {
	cases := []struct{ page, perPage, wantPage, wantPer int }{
		{0, 0, 1, DefaultPerPage},
		{-3, -1, 1, DefaultPerPage},
		{2, 10, 2, 10},
		{1, MaxPerPage + 1000, 1, MaxPerPage},
	}
	for _, tc := range cases {
		q := base()
		q.Page, q.PerPage = tc.page, tc.perPage
		if err := q.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		if q.Page != tc.wantPage || q.PerPage != tc.wantPer {
			t.Errorf("page/per_page = %d/%d, want %d/%d", q.Page, q.PerPage, tc.wantPage, tc.wantPer)
		}
	}
}

func TestAnInvertedTimeRangeIsRefused(t *testing.T) {
	q := base()
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	q.Since, q.Until = &late, &early
	if err := q.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want a refusal: an inverted range silently matches nothing")
	}
}

func TestAnAccountlessQueryIsRefused(t *testing.T) {
	q := NodeActivityListQuery{}
	if err := q.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want a refusal without an account")
	}
}
