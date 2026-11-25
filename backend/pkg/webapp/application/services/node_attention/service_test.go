package node_attention

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	attention "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_attention"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

// The fakes embed their port so only the methods under test need a body; an
// unimplemented one panics rather than quietly returning a zero value.
type fakeNodes struct {
	repository.NodeRepository
	nodes           []node.Node
	err             error
	admitted        int64
	accountAdmitted int64
	countErr        error
	accountCountErr error
}

func (f *fakeNodes) CountAdmittedNodesForAccount(context.Context, uuid.UUID) (int64, error) {
	return f.accountAdmitted, f.accountCountErr
}

func (f *fakeNodes) GetAllByAccountID(context.Context, uuid.UUID) ([]node.Node, error) {
	return f.nodes, f.err
}

func (f *fakeNodes) CountAdmittedNodes(context.Context) (int64, error) {
	return f.admitted, f.countErr
}

type fixedCapSource int

func (c fixedCapSource) EffectiveDeviceCap(context.Context) (int, string) { return int(c), "" }

type fakeOUs struct {
	repository.OrganizationalUnitRepository
	ous []organizational_unit.OrganizationalUnit
	err error
}

func (f *fakeOUs) GetAllByAccountID(context.Context, uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	return f.ous, f.err
}

type fakeCommands struct {
	repository.NodeCommandRepository
	busy     map[uuid.UUID]repository.ActiveCommand
	busyErr  error
	attempts map[uuid.UUID]map[string]repository.CommandAttempt
	attErr   error
}

func (f *fakeCommands) NodesWithActiveCommands(context.Context, uuid.UUID) (map[uuid.UUID]repository.ActiveCommand, error) {
	return f.busy, f.busyErr
}

func (f *fakeCommands) LatestAttempts(context.Context, uuid.UUID, []string) (map[uuid.UUID]map[string]repository.CommandAttempt, error) {
	return f.attempts, f.attErr
}

type fakeStates struct {
	repository.NodeUtilityStateRepository
	states []utility.NodeState
	err    error
}

func (f *fakeStates) ListByNodes(context.Context, uuid.UUID, []uuid.UUID) ([]utility.NodeState, error) {
	return f.states, f.err
}

type fakeReleases struct {
	latest string
	err    error
}

func (f fakeReleases) Latest(context.Context) (string, error) { return f.latest, f.err }

type fakeFirmware struct {
	base map[string]string
	err  error
}

func (f fakeFirmware) BaseVersions(context.Context) (map[string]string, error) {
	return f.base, f.err
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func onlineNode() node.Node {
	seen := time.Now().Add(-time.Minute)
	return node.Node{
		ID:           uuid.New(),
		Name:         "node",
		Status:       node.StatusOnline,
		CreatedAt:    time.Now().Add(-30 * 24 * time.Hour),
		LastSeenAt:   &seen,
		AgentVersion: "0.6.9",
	}
}

// The reason the whole package carries an Omitted list. A signer that will not
// answer, or a release listing that will not load, must leave the fleet
// reporting "we could not look" — not "nothing to do". The two are the same
// JSON otherwise, and only one of them means the fleet is fine.
func TestAnUnreadableSourceIsOmittedRatherThanAnsweredClean(t *testing.T) {
	svc := NewService(
		&fakeNodes{nodes: []node.Node{onlineNode()}},
		&fakeOUs{},
		&fakeCommands{},
		&fakeStates{},
		fakeReleases{err: errors.New("cdn listing unavailable")},
		fakeFirmware{err: errors.New("signer unreachable")},
		discard(),
	)
	fleet, err := svc.Resolve(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil: one unreadable source must not fail the whole view", err)
	}
	for _, want := range []attention.Kind{attention.KindAgentOutdated, attention.KindBootloaderOutdated} {
		if !slices.Contains(fleet.Omitted, want) {
			t.Errorf("Omitted = %v, want it to name %s", fleet.Omitted, want)
		}
	}
	if len(fleet.Nodes) != 0 {
		t.Errorf("Nodes = %d, want 0: a skipped check must produce no items", len(fleet.Nodes))
	}
}

// With the sources answering, the same node is reported — so the test above is
// measuring the omission and not a node that was never going to appear.
func TestAReadableSourceProducesTheItem(t *testing.T) {
	svc := NewService(
		&fakeNodes{nodes: []node.Node{onlineNode()}},
		&fakeOUs{},
		&fakeCommands{},
		&fakeStates{},
		fakeReleases{latest: "0.6.13"},
		fakeFirmware{},
		discard(),
	)
	fleet, err := svc.Resolve(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(fleet.Nodes) != 1 || len(fleet.Nodes[0].Items) != 1 ||
		fleet.Nodes[0].Items[0].Kind != attention.KindAgentOutdated {
		t.Fatalf("got %+v, want one node with agent_outdated", fleet.Nodes)
	}
	if slices.Contains(fleet.Omitted, attention.KindAgentOutdated) {
		t.Error("a check that ran must not be reported as omitted")
	}
	// No signer configured is a real install, and the bootloader question is
	// then unanswerable rather than answered "current".
	if !slices.Contains(fleet.Omitted, attention.KindBootloaderOutdated) {
		t.Errorf("Omitted = %v, want bootloader_outdated with no signer", fleet.Omitted)
	}
}

// The node read is the one input the view cannot degrade around.
func TestAFailedNodeReadIsFatal(t *testing.T) {
	svc := NewService(
		&fakeNodes{err: errors.New("database down")},
		&fakeOUs{}, &fakeCommands{}, &fakeStates{}, fakeReleases{}, fakeFirmware{}, discard(),
	)
	if _, err := svc.Resolve(context.Background(), uuid.New()); err == nil {
		t.Fatal("Resolve() error = nil, want the node read's error")
	}
}

// A row with nothing asked of it yet is not drift. Reporting it would put every
// node that has ever had a utility on the attention list forever.
func TestUtilityDriftCountsOnlyNonConvergedRows(t *testing.T) {
	n := onlineNode()
	svc := NewService(
		&fakeNodes{nodes: []node.Node{n}},
		&fakeOUs{},
		&fakeCommands{},
		&fakeStates{states: []utility.NodeState{
			{NodeID: n.ID, UtilityID: "converged", DesiredHash: "a", AppliedHash: "a"},
			{NodeID: n.ID, UtilityID: "nothing-asked", DesiredHash: "", AppliedHash: ""},
			{NodeID: n.ID, UtilityID: "behind", DesiredHash: "b", AppliedHash: "a"},
			{NodeID: n.ID, UtilityID: "broken", DesiredHash: "c", AppliedHash: "c", LastError: "failed: exit 1"},
		}},
		fakeReleases{}, fakeFirmware{}, discard(),
	)
	fleet, err := svc.Resolve(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	var got []string
	for _, it := range fleet.Nodes[0].Items {
		if it.Kind == attention.KindUtilityDrift {
			got = append(got, it.Subject)
		}
	}
	slices.Sort(got)
	want := []string{"behind", "broken"}
	if !slices.Equal(got, want) {
		t.Errorf("drifted utilities = %v, want %v", got, want)
	}
}

// A node the platform is already working on must be flagged, and an unreadable
// answer must not refuse every action across the fleet. The case it guards:
// two profile applies pressed while an agent update has the agent mid-restart,
// both delivered and neither acknowledged.
func TestBusyIsReportedAndFailsOpen(t *testing.T) {
	n := onlineNode()
	svc := NewService(
		&fakeNodes{nodes: []node.Node{n}},
		&fakeOUs{},
		&fakeCommands{busy: map[uuid.UUID]repository.ActiveCommand{
			n.ID: {Slug: node_command.SlugApplyUtilityConfig, Progress: 10},
		}},
		&fakeStates{},
		fakeReleases{latest: "0.7.1"},
		fakeFirmware{}, discard(),
	)
	fleet, err := svc.Resolve(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(fleet.Nodes) != 1 || fleet.Nodes[0].Busy == nil {
		t.Fatalf("got %+v, want the node flagged busy", fleet.Nodes)
	}
	// Naming the command is the point: "busy" alone is indistinguishable from
	// stuck, and a package install can run for minutes.
	if fleet.Nodes[0].Busy.Slug != node_command.SlugApplyUtilityConfig ||
		fleet.Nodes[0].Busy.Progress != 10 {
		t.Errorf("busy = %+v, want the running command and its progress", fleet.Nodes[0].Busy)
	}

	svc = NewService(
		&fakeNodes{nodes: []node.Node{n}},
		&fakeOUs{},
		&fakeCommands{busyErr: errors.New("database down")},
		&fakeStates{},
		fakeReleases{latest: "0.7.1"},
		fakeFirmware{}, discard(),
	)
	fleet, err = svc.Resolve(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Resolve() error = %v, want the view to survive one failed query", err)
	}
	if len(fleet.Nodes) != 1 || fleet.Nodes[0].Busy != nil {
		t.Errorf("got %+v, want the node reported and NOT withheld when busy is unknown", fleet.Nodes)
	}
}

// Only the LAST attempt counts, and only when it failed. A node that failed
// yesterday and succeeded today is not carrying a problem, and saying it is
// would send an operator after a card they already replaced.
func TestOnlyTheLatestAttemptIsReportedAsAFailure(t *testing.T) {
	n := onlineNode()
	failedAt := time.Now().Add(-time.Minute)
	cmdID := uuid.New()

	svc := NewService(
		&fakeNodes{nodes: []node.Node{n}},
		&fakeOUs{},
		&fakeCommands{attempts: map[uuid.UUID]map[string]repository.CommandAttempt{
			n.ID: {
				node_command.SlugApplyDeviceProfile: {CommandID: cmdID, Status: "failed", At: failedAt},
				node_command.SlugUpdateAgent:        {CommandID: uuid.New(), Status: "success", At: time.Now()},
			},
		}},
		&fakeStates{},
		fakeReleases{latest: "0.7.1"},
		fakeFirmware{}, discard(),
	)
	fleet, err := svc.Resolve(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	got := fleet.Nodes[0].Failures
	if len(got) != 1 {
		t.Fatalf("failures = %+v, want only the failed slug", got)
	}
	if got[0].Slug != node_command.SlugApplyDeviceProfile || got[0].CommandID != cmdID {
		t.Errorf("failure = %+v, want the profile apply and its command id", got[0])
	}
}

// Only the LATEST access apply counts. A failure followed by a success is not
// a problem, and reporting it would send an operator after a key that is
// already on the device.
// The device-cap check reads the instance-wide cap and count once per page
// load. Without a source, or with a count that cannot be read, it is named
// in Omitted rather than answered; at the cap it reaches the node waiting to
// be admitted and leaves the admitted one alone.
func TestDeviceCapCheckIsAnsweredOrOmitted(t *testing.T) {
	waiting := onlineNode()
	waiting.Status = node.StatusReadyForAdoption
	waiting.LastSeenAt = nil
	waiting.ApprovalStatus = node.ApprovalStatusApproved
	admitted := onlineNode()
	admitted.ApprovalStatus = node.ApprovalStatusApproved

	build := func(nodes *fakeNodes, src DeviceCapSource) *Service {
		svc := NewService(nodes, &fakeOUs{}, &fakeCommands{}, &fakeStates{}, fakeReleases{latest: "0.6.9"}, fakeFirmware{}, discard())
		if src != nil {
			svc.SetDeviceCapSource(src)
		}
		return svc
	}

	for name, svc := range map[string]*Service{
		"no source":        build(&fakeNodes{nodes: []node.Node{waiting}, admitted: 25}, nil),
		"count unreadable": build(&fakeNodes{nodes: []node.Node{waiting}, countErr: errors.New("db")}, fixedCapSource(25)),
		"account count unreadable": build(&fakeNodes{nodes: []node.Node{waiting}, admitted: 25,
			accountCountErr: errors.New("db")}, fixedCapSource(25)),
	} {
		fleet, err := svc.Resolve(context.Background(), uuid.New())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Contains(fleet.Omitted, attention.KindDeviceCapReached) {
			t.Errorf("%s: Omitted = %v, want it to name %s", name, fleet.Omitted, attention.KindDeviceCapReached)
		}
	}

	svc := build(&fakeNodes{nodes: []node.Node{waiting, admitted}, admitted: 25}, fixedCapSource(25))
	fleet, err := svc.Resolve(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(fleet.Omitted, attention.KindDeviceCapReached) {
		t.Errorf("the check was omitted with a source and a count: %v", fleet.Omitted)
	}
	reported := map[uuid.UUID]bool{}
	for _, n := range fleet.Nodes {
		for _, it := range n.Items {
			if it.Kind == attention.KindDeviceCapReached {
				reported[n.Node.ID] = true
			}
		}
	}
	if !reported[waiting.ID] || reported[admitted.ID] {
		t.Errorf("reported = %v; want the waiting node only", reported)
	}
}

func TestOnlyAFailedLatestAccessApplyRaisesTheCondition(t *testing.T) {
	n := onlineNode()
	for _, tc := range []struct {
		status string
		want   bool
	}{
		{status: "failed", want: true},
		{status: "success", want: false},
		{status: "in_progress", want: false},
	} {
		svc := NewService(
			&fakeNodes{nodes: []node.Node{n}},
			&fakeOUs{},
			&fakeCommands{attempts: map[uuid.UUID]map[string]repository.CommandAttempt{
				n.ID: {node_command.SlugApplyAccessConfig: {CommandID: uuid.New(), Status: tc.status}},
			}},
			&fakeStates{},
			fakeReleases{}, fakeFirmware{}, discard(),
		)
		fleet, err := svc.Resolve(context.Background(), uuid.New())
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		got := false
		if len(fleet.Nodes) > 0 {
			for _, item := range fleet.Nodes[0].Items {
				if item.Kind == attention.KindAccessApplyFailed {
					got = true
				}
			}
		}
		if got != tc.want {
			t.Errorf("latest apply %q: reported = %v, want %v", tc.status, got, tc.want)
		}
	}
}

// operatorCapSource also answers the instance-operator question, as the
// entitlement service does.
type operatorCapSource struct {
	fixedCapSource
	operator uuid.UUID
}

func (o operatorCapSource) IsInstanceOperator(_ context.Context, accountID uuid.UUID) bool {
	return accountID == o.operator
}

// The item carries the caller's own count
// and whether it operates the install; only the operator gets the install's.
func TestDeviceCapItemIsScopedToTheCaller(t *testing.T) {
	waiting := onlineNode()
	waiting.Status = node.StatusReadyForAdoption
	waiting.LastSeenAt = nil
	waiting.ApprovalStatus = node.ApprovalStatusApproved
	operator, tenant := uuid.New(), uuid.New()

	for _, tc := range []struct {
		name    string
		account uuid.UUID
		src     DeviceCapSource
		wantOp  bool
	}{
		{"tenant", tenant, operatorCapSource{25, operator}, false},
		{"operator", operator, operatorCapSource{25, operator}, true},
		{"source without the operator question", operator, fixedCapSource(25), false},
	} {
		svc := NewService(&fakeNodes{nodes: []node.Node{waiting}, admitted: 30, accountAdmitted: 2},
			&fakeOUs{}, &fakeCommands{}, &fakeStates{}, fakeReleases{latest: "0.6.9"}, fakeFirmware{}, discard())
		svc.SetDeviceCapSource(tc.src)
		fleet, err := svc.Resolve(context.Background(), tc.account)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var item *attention.Item
		for _, n := range fleet.Nodes {
			for i := range n.Items {
				if n.Items[i].Kind == attention.KindDeviceCapReached {
					item = &n.Items[i]
				}
			}
		}
		if item == nil {
			t.Fatalf("%s: no device_cap_reached item", tc.name)
		}
		wantCurrent := ""
		if tc.wantOp {
			wantCurrent = "30"
		}
		if item.Current != wantCurrent || item.Expected != "25" ||
			item.AccountDevices == nil || *item.AccountDevices != 2 ||
			item.InstanceOperator == nil || *item.InstanceOperator != tc.wantOp {
			t.Errorf("%s: item = %+v", tc.name, *item)
		}
	}
}
