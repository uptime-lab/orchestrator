package organizational_unit

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/organizational_unit_summary"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// Stubs embed their interface so an unexpected call panics rather than
// silently returning zero values (the package's stub convention).

type summaryOURepo struct {
	repository.OrganizationalUnitRepository
	ous []organizational_unit.OrganizationalUnit
}

func (r *summaryOURepo) GetByID(_ context.Context, _ uuid.UUID, id uuid.UUID) (*organizational_unit.OrganizationalUnit, error) {
	for i := range r.ous {
		if r.ous[i].ID == id {
			copied := r.ous[i]
			return &copied, nil
		}
	}
	return nil, app_errors.ErrOrganizationalUnitNotFound
}

func (r *summaryOURepo) GetAllByAccountID(context.Context, uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	return append([]organizational_unit.OrganizationalUnit(nil), r.ous...), nil
}

type summaryNodeRepo struct {
	repository.NodeRepository
	nodes []node.Node
}

func (r *summaryNodeRepo) GetAllByAccountID(context.Context, uuid.UUID) ([]node.Node, error) {
	return append([]node.Node(nil), r.nodes...), nil
}

type summaryPolicyRepo struct {
	repository.AccountPolicyRepository
}

func (r *summaryPolicyRepo) Get(context.Context, uuid.UUID) (*organizational_unit.AccountPolicy, error) {
	return nil, nil
}

type summaryAccessRepo struct {
	repository.AccessEntryRepository
	scoped      []access_entry.AccessEntry
	accountWide []access_entry.AccessEntry
}

func (r *summaryAccessRepo) ListByOUs(context.Context, uuid.UUID, []uuid.UUID) ([]access_entry.AccessEntry, error) {
	return append([]access_entry.AccessEntry(nil), r.scoped...), nil
}

func (r *summaryAccessRepo) ListAccountLevel(context.Context, uuid.UUID) ([]access_entry.AccessEntry, error) {
	return append([]access_entry.AccessEntry(nil), r.accountWide...), nil
}

// The nodes section must report, per node, whether THIS unit's chain actually
// decides its policies — the whole licence of the Organization page's node
// list — and the access section must apply the resolution's nearest-level
// shadowing to what it samples. Sections not requested land in omitted.
func TestSummaryPolicyModesShadowingAndOmitted(t *testing.T) {
	accountID := uuid.New()
	rootID, childID := uuid.New(), uuid.New()
	flag := true

	ouRepo := &summaryOURepo{ous: []organizational_unit.OrganizationalUnit{
		{ID: rootID, AccountID: accountID, Name: "root"},
		// The child answers boot_to_os itself, so root-unit members routed
		// through it read "via_child".
		{ID: childID, AccountID: accountID, Name: "child", ParentID: &rootID, BootToOS: &flag},
	}}
	inherits, overridden, viaChild := uuid.New(), uuid.New(), uuid.New()
	nodeRepo := &summaryNodeRepo{nodes: []node.Node{
		{ID: inherits, Name: "plain", OUID: &rootID},
		{ID: overridden, Name: "custom", OUID: &rootID, SSHPasswordAuth: &flag},
		{ID: viaChild, Name: "routed", OUID: &childID},
	}}

	// The same OS username at the unit and at the account level: the nearest
	// level (the unit) wins entirely; the account-wide copy is shadowed.
	scopedEntry := access_entry.AccessEntry{ID: uuid.New(), Type: access_entry.TypeUser, Username: "ops", OUIDs: []uuid.UUID{rootID}}
	accountEntry := access_entry.AccessEntry{ID: uuid.New(), Type: access_entry.TypeUser, Username: "ops", AccountWide: true, Sudo: true}

	recorder, _ := testAuditRecorder()
	svc := &Service{
		ouRepo:            ouRepo,
		nodeRepo:          nodeRepo,
		accountPolicyRepo: &summaryPolicyRepo{},
		accessEntryRepo:   &summaryAccessRepo{scoped: []access_entry.AccessEntry{scopedEntry}, accountWide: []access_entry.AccessEntry{accountEntry}},
		audit:             recorder,
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	out, err := svc.Summary(context.Background(),
		organizational_unit_summary.OrganizationalUnitSummaryQuery{
			ID:          rootID,
			AuthContext: commands.NewAuthContext(accountID, uuid.New()),
		},
		SummarySections{Nodes: true, Access: true})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}

	if out.Nodes == nil || out.Nodes.Total != 3 {
		t.Fatalf("nodes section: %+v", out.Nodes)
	}
	modes := map[uuid.UUID]string{}
	for _, item := range out.Nodes.Items {
		modes[item.ID] = item.PolicyMode
	}
	if modes[inherits] != "inherits" || modes[overridden] != "overridden" || modes[viaChild] != "via_child" {
		t.Errorf("policy modes = %v", modes)
	}
	if out.Counts.Nodes != 2 || out.Counts.ChildNodes != 1 {
		t.Errorf("counts: direct=%d child=%d", out.Counts.Nodes, out.Counts.ChildNodes)
	}

	if out.AccessEntries == nil || out.AccessEntries.Total != 2 {
		t.Fatalf("access section: %+v", out.AccessEntries)
	}
	for _, item := range out.AccessEntries.Items {
		switch item.ID {
		case scopedEntry.ID:
			if item.Shadowed || item.EffectiveFrom != "unit" {
				t.Errorf("unit entry: %+v", item)
			}
		case accountEntry.ID:
			if !item.Shadowed || item.EffectiveFrom != "account" {
				t.Errorf("account entry must be shadowed by the nearer username: %+v", item)
			}
		}
	}

	// Sections the caller may not read are NAMED, not silently empty.
	for _, want := range []string{"secrets", "workflows", "master_tokens"} {
		found := false
		for _, got := range out.Omitted {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("omitted must name %q, got %v", want, out.Omitted)
		}
	}
}
