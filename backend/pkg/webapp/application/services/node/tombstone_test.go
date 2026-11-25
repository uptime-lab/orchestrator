package node

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/node_commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_tombstone"
)

// The adoption method decides what a restore is WORTH, so it must come from what
// the device reported rather than from what the platform assumes. An
// EEPROM-provisioned device rejoins on its own once the row is back; a device
// whose identity is a file on its own disk rejoins only while that file survives;
// a node that never reported a serial cannot be recognised at boot at all.
func TestAdoptionMethodComesFromWhatTheDeviceReported(t *testing.T) {
	bakedHost := &node.Node{
		ID:        uuid.New(),
		AccountID: uuid.New(),
		Fingerprint: &node.HardwareFingerprint{
			HardwareSerial: "10000000abcd0001",
		},
		SystemInfo: &node.SystemInfo{
			Hardware: &node.HardwareInfo{
				Boot: &node.BootInfo{BootHTTPHost: "boot.example.test"},
			},
		},
	}
	if got := tombstoneFor(bakedHost, uuid.New()).AdoptionMethod; got != node_tombstone.AdoptionEEPROM {
		t.Errorf("a device with a baked EEPROM boot host = %q, want %q", got, node_tombstone.AdoptionEEPROM)
	}

	fileAdopted := &node.Node{
		ID:          uuid.New(),
		AccountID:   uuid.New(),
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: "10000000abcd0003"},
	}
	if got := tombstoneFor(fileAdopted, uuid.New()).AdoptionMethod; got != node_tombstone.AdoptionFile {
		t.Errorf("a device with a serial but no baked host = %q, want %q", got, node_tombstone.AdoptionFile)
	}

	// A WebUSB placeholder, or a row created and deleted before first contact.
	// Dereferencing the nil fingerprint here would panic on the most common
	// delete there is, which is the same trap the audit line beside it hit.
	never := &node.Node{ID: uuid.New(), AccountID: uuid.New()}
	ts := tombstoneFor(never, uuid.New())
	if ts.AdoptionMethod != node_tombstone.AdoptionUnknown {
		t.Errorf("a node that never reported hardware = %q, want %q", ts.AdoptionMethod, node_tombstone.AdoptionUnknown)
	}
	if ts.RecognisableAtBoot() {
		t.Error("a tombstone with no serial claims it can be recognised at boot")
	}
}

// The identity is what makes a restore work, so it must be carried verbatim: the
// device authenticates as that node id with that token, and a fresh id would
// produce a row it can never match.
func TestTheTombstoneCarriesTheIdentityARestoreNeeds(t *testing.T) {
	ouID := uuid.New()
	actor := uuid.New()
	n := &node.Node{
		ID:          uuid.New(),
		AccountID:   uuid.New(),
		Name:        "Node-abcd0001",
		Hostname:    "rpi-abcd0001",
		UniqueToken: "the-devices-credential",
		OUID:        &ouID,
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: "10000000abcd0001"},
	}

	ts := tombstoneFor(n, actor)

	if ts.ID != n.ID {
		t.Errorf("tombstone id = %s, want the DELETED node's id %s", ts.ID, n.ID)
	}
	if ts.UniqueToken != n.UniqueToken {
		t.Error("the token the device still holds was not carried")
	}
	if ts.HardwareSerial != "10000000abcd0001" {
		t.Errorf("serial = %q", ts.HardwareSerial)
	}
	if ts.OUID == nil || *ts.OUID != ouID {
		t.Errorf("ou = %v, want %v", ts.OUID, ouID)
	}
	if ts.DeletedBy != actor {
		t.Error("the actor was not recorded")
	}
	if !ts.Restorable() {
		t.Error("a fresh tombstone must be restorable")
	}
}

// The boot override is RECORDED, not chosen. Both defaults are wrong: leaving a
// network-first device on "boot the local OS" means HTTP-boot keeps withholding
// and the restore appears to do nothing, while forcing netboot would drag a
// healthy node into recovery on its next reboot.
func TestTheBootOverrideIsCarriedRatherThanChosen(t *testing.T) {
	netboot := false
	localOS := true
	for name, want := range map[string]*bool{
		"network-first node": &netboot,
		"boot-to-OS node":    &localOS,
		"inheriting node":    nil,
	} {
		n := &node.Node{ID: uuid.New(), AccountID: uuid.New(), BootToOS: want}
		got := tombstoneFor(n, uuid.New()).BootToOS
		switch {
		case want == nil && got != nil:
			t.Errorf("%s: boot override = %v, want nil (inherit)", name, *got)
		case want != nil && (got == nil || *got != *want):
			t.Errorf("%s: boot override = %v, want %v", name, got, *want)
		}
	}
}

// A used tombstone is history, not an offer — otherwise a second restore would
// race the live row the device is already talking to.
func TestARestoredTombstoneIsNoLongerAnOffer(t *testing.T) {
	restoredAt := time.Now()
	ts := node_tombstone.Tombstone{ID: uuid.New(), RestoredAt: &restoredAt}
	if ts.Restorable() {
		t.Error("an already-restored tombstone is still being offered")
	}
}

// --- the force gate -------------------------------------------------------

// The gate exists because deleting an ONLINE node is the moment that strands the
// device: it is the one time the platform can still reach it, and deleting there
// leaves the agent holding an identity nothing will ever accept again.
func TestDeletingAnOnlineNodeIsRefusedWithoutForce(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	repo := &deleteFakeRepo{nodes: map[uuid.UUID]*node.Node{
		nodeID: {ID: nodeID, AccountID: accountID, Status: node.StatusOnline},
	}}
	recorder, sink := audittest.New()
	svc := &Service{
		nodeRepo: repo,
		audit:    recorder,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false)
	if !errors.Is(err, app_errors.ErrNodeOnlineDeleteRequiresForce) {
		t.Fatalf("delete of an online node returned %v, want the force refusal", err)
	}
	// A refusal is not a delete: nothing may be recorded and nothing removed.
	if actions := sink.Actions(); len(actions) != 0 {
		t.Errorf("a refused delete recorded %v", actions)
	}
	if _, stillThere := repo.nodes[nodeID]; !stillThere {
		t.Error("the node was deleted by a call that was supposed to be refused")
	}
}

// A node in maintenance or restarting is CONNECTED — a wedged mid-install node
// sits there with a live stream — so deleting it strands the device just as an
// online delete would, and must equally require force. Keying the
// gate on online alone let the most common strand slip straight through.
func TestDeletingAConnectedButBusyNodeIsRefusedWithoutForce(t *testing.T) {
	for _, status := range []node.NodeStatus{node.StatusMaintenance, node.StatusRestarting} {
		t.Run(string(status), func(t *testing.T) {
			accountID, nodeID := uuid.New(), uuid.New()
			repo := &deleteFakeRepo{nodes: map[uuid.UUID]*node.Node{
				nodeID: {ID: nodeID, AccountID: accountID, Status: status},
			}}
			recorder, _ := audittest.New()
			svc := &Service{
				nodeRepo: repo,
				audit:    recorder,
				logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false)
			if !errors.Is(err, app_errors.ErrNodeOnlineDeleteRequiresForce) {
				t.Fatalf("delete of a %s node returned %v, want the force refusal", status, err)
			}
			if _, stillThere := repo.nodes[nodeID]; !stillThere {
				t.Errorf("a %s node was deleted by a call that was supposed to be refused", status)
			}
		})
	}
}

// An OFFLINE node needs no force. Its device is not reachable either way, so
// demanding a flag there would be ceremony rather than a guard — and the
// deleted-node record is what covers it.
func TestDeletingAnOfflineNodeNeedsNoForce(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	repo := &deleteFakeRepo{nodes: map[uuid.UUID]*node.Node{
		nodeID: {ID: nodeID, AccountID: accountID, Status: node.StatusOffline},
	}}
	recorder, _ := audittest.New()
	svc := &Service{
		nodeRepo: repo,
		audit:    recorder,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete of an offline node: %v", err)
	}
	if _, stillThere := repo.nodes[nodeID]; stillThere {
		t.Error("the node was not deleted")
	}
}

// The release is best-effort and the delete must survive it failing. Here
// nothing is wired to dispatch commands — which is also every unit fixture in
// this package — so requestIdentityRelease errors, and a delete that failed for
// that reason would leave the operator with neither the node nor a retry.
func TestAForcedDeleteSucceedsWhenTheReleaseCannotBeDispatched(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	repo := &deleteFakeRepo{nodes: map[uuid.UUID]*node.Node{
		nodeID: {ID: nodeID, AccountID: accountID, Status: node.StatusOnline},
	}}
	recorder, sink := audittest.New()
	tombstones := &fakeTombstoneRepo{}
	svc := &Service{
		nodeRepo:      repo,
		tombstoneRepo: tombstones,
		audit:         recorder,
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, true); err != nil {
		t.Fatalf("forced delete: %v", err)
	}
	if _, ok := sink.Find(audit_event.ActionNodeDelete); !ok {
		t.Error("the delete was not audited")
	}
	// And the record must NOT claim a release was requested, or the restore offer
	// would read as useless when it is in fact the only route back.
	if tombstones.created == nil {
		t.Fatal("no deleted-node record was written")
	}
	if tombstones.created.IdentityReleaseRequested {
		t.Error("the record claims a release was requested, but the dispatch failed")
	}
}

// settlingCommandService records the delete's request to settle the node's
// outstanding commands; anything else panics through the nil embed.
type settlingCommandService struct {
	services.NodeCommandService
	nodeID uuid.UUID
	kept   []string
	calls  int
}

func (s *settlingCommandService) SettleCommandsForDeletedNode(_ context.Context, nodeID uuid.UUID, keepSlugs ...string) (int, error) {
	s.calls++
	s.nodeID = nodeID
	s.kept = keepSlugs
	return 0, nil
}

// A deleted node's queued commands must not survive it: restored under its
// original id, the node would otherwise receive them hours later. The
// release_identity the delete itself sends is the one that must stay live.
func TestDeletingANodeSettlesItsOutstandingCommands(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	repo := &deleteFakeRepo{nodes: map[uuid.UUID]*node.Node{
		nodeID: {ID: nodeID, AccountID: accountID, Status: node.StatusOffline},
	}}
	recorder, _ := audittest.New()
	commands := &settlingCommandService{}
	svc := &Service{
		nodeRepo:           repo,
		nodeCommandService: commands,
		audit:              recorder,
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if commands.calls != 1 || commands.nodeID != nodeID {
		t.Fatalf("settle calls = %d for node %s, want one for %s", commands.calls, commands.nodeID, nodeID)
	}
	if len(commands.kept) != 1 || commands.kept[0] != node_command.SlugReleaseIdentity {
		t.Errorf("kept slugs = %v, want only release_identity", commands.kept)
	}
}

// installingCommandService answers the in-flight check with an install_os (or
// nothing) and accepts the delete's settle.
type installingCommandService struct {
	settlingCommandService
	active *node_command.NodeCommand
}

func (s *installingCommandService) FindActiveDeviceCommand(context.Context, uuid.UUID) (*node_command.NodeCommand, error) {
	return s.active, nil
}

// Deleting a node mid-install settles the install as failed, and the
// failed-install rule follows it into the record: restored, the node must come
// back pointed at netboot, not at a disk that may hold half an image. A node
// with no install keeps the boot target it had.
func TestDeletingANodeMidInstallRecordsNetbootForTheRestore(t *testing.T) {
	localOS := true
	for name, active := range map[string]*node_command.NodeCommand{
		"install in flight": {Slug: node_command.SlugInstallOS},
		"no install":        nil,
	} {
		t.Run(name, func(t *testing.T) {
			accountID, nodeID := uuid.New(), uuid.New()
			repo := &deleteFakeRepo{nodes: map[uuid.UUID]*node.Node{
				nodeID: {ID: nodeID, AccountID: accountID, Status: node.StatusOffline, BootToOS: &localOS},
			}}
			recorder, _ := audittest.New()
			tombstones := &fakeTombstoneRepo{}
			svc := &Service{
				nodeRepo:           repo,
				tombstoneRepo:      tombstones,
				nodeCommandService: &installingCommandService{active: active},
				audit:              recorder,
				logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if tombstones.created == nil || tombstones.created.BootToOS == nil {
				t.Fatal("no boot target was recorded")
			}
			want := active == nil
			if got := *tombstones.created.BootToOS; got != want {
				t.Errorf("recorded boot_to_os = %v, want %v", got, want)
			}
		})
	}
}

// fakeTombstoneRepo captures the record a delete writes.
type fakeTombstoneRepo struct {
	repository.NodeTombstoneRepository
	created *node_tombstone.Tombstone
}

func (f *fakeTombstoneRepo) Create(_ context.Context, t *node_tombstone.Tombstone) error {
	copied := *t
	f.created = &copied
	return nil
}

// The floor has to be applied HERE, because this path does not go through
// sendCommandToNode where every other caller gets it.
//
// Without it the command registers, ships, and the agent
// answers "unknown or unsupported command type" — leaving a FAILED command row
// on every forced delete and, worse, a tombstone claiming the device was asked
// to release when it plainly refused. That claim is the only thing the field
// exists to get right.
func TestTheReleaseIsRefusedForAnAgentBelowTheFloor(t *testing.T) {
	floor, required := node_command.MinAgentVersion(node_command.SlugReleaseIdentity)
	if !required {
		t.Fatal("release_identity has no version floor; this test guards one that should exist")
	}

	svc := &Service{
		nodeCommandService: stubRegistrar{},
		nodeCommandClient:  stubDispatchClient{},
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	tooOld := &node.Node{ID: uuid.New(), AccountID: uuid.New(), AgentVersion: "0.4.9"}
	err := svc.requestIdentityRelease(context.Background(), tooOld, uuid.New())
	if err == nil {
		t.Fatalf("an agent on 0.4.9 was sent a command with a %s floor", floor)
	}

	// And an empty version is treated as too old, like every other floor check —
	// a node that has never reported one must not be assumed capable.
	never := &node.Node{ID: uuid.New(), AccountID: uuid.New()}
	if err := svc.requestIdentityRelease(context.Background(), never, uuid.New()); err == nil {
		t.Error("a node with no reported agent version was sent the command")
	}
}

// stubRegistrar/stubDispatchClient exist only to prove the floor short-circuits
// BEFORE either is reached: both fail loudly if called.
type stubRegistrar struct{ services.NodeCommandService }

func (stubRegistrar) RegisterNodeCommand(_ context.Context, _ uuid.UUID, slug string, _ map[string]interface{}, _ interface{}) (*node_command.NodeCommand, error) {
	panic("registered " + slug + " despite the agent being below its floor")
}

type stubDispatchClient struct {
	node_commands.NodeCommandClient
}

func (stubDispatchClient) Send(node_commands.NodeCommandOperation) error {
	panic("dispatched despite the agent being below its floor")
}

// HTTP_HOST says the EEPROM points at a platform, not that it carries THIS
// node's token; only a flash the platform performed for this row proves the
// bake. And the EEPROM identity is presented only when the device netboots, so
// the boot order decides whether it is ever seen again — both captured at
// delete time because the row that knew them is about to go.
func TestTombstoneCapturesTheBakeProofAndTheBootOrder(t *testing.T) {
	flashedAt := time.Now()
	baked := &node.Node{
		ID: uuid.New(), AccountID: uuid.New(),
		Fingerprint:         &node.HardwareFingerprint{HardwareSerial: "10000000abcd0001"},
		BootloaderFlashedAt: &flashedAt,
		SystemInfo: &node.SystemInfo{Hardware: &node.HardwareInfo{Boot: &node.BootInfo{
			BootHTTPHost:     "boot.example.test",
			BootDevicesOrder: []string{"http", "nvme"},
		}}},
	}
	ts := tombstoneFor(baked, uuid.New())
	if !ts.EEPROMBakeConfirmed {
		t.Error("a node the platform flashed is not marked as a confirmed bake")
	}
	if ts.BootOrderNetbootsFirst == nil || !*ts.BootOrderNetbootsFirst {
		t.Errorf("boot order http→nvme netboots first, recorded %v", ts.BootOrderNetbootsFirst)
	}

	pointsAtPlatformOnly := &node.Node{
		ID: uuid.New(), AccountID: uuid.New(),
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: "10000000abcd0003"},
		SystemInfo: &node.SystemInfo{Hardware: &node.HardwareInfo{Boot: &node.BootInfo{
			BootHTTPHost:     "boot.example.test",
			BootDevicesOrder: []string{"nvme", "http"},
		}}},
	}
	ts = tombstoneFor(pointsAtPlatformOnly, uuid.New())
	if ts.AdoptionMethod != node_tombstone.AdoptionEEPROM {
		t.Errorf("adoption method = %q, want eeprom (the HTTP_HOST heuristic is unchanged)", ts.AdoptionMethod)
	}
	if ts.EEPROMBakeConfirmed {
		t.Error("a node the platform never flashed is marked as a confirmed bake")
	}
	if ts.BootOrderNetbootsFirst == nil || *ts.BootOrderNetbootsFirst {
		t.Errorf("boot order nvme→http boots the disk first, recorded %v", ts.BootOrderNetbootsFirst)
	}

	// No report is not "cannot": the field stays nil rather than false.
	never := &node.Node{ID: uuid.New(), AccountID: uuid.New()}
	if got := tombstoneFor(never, uuid.New()).BootOrderNetbootsFirst; got != nil {
		t.Errorf("a node that never reported a boot order recorded %v, want nil", *got)
	}
}

// The release command's terminal success arrives after the node row is gone,
// so it lands on the tombstone: "requested" becomes "the device reported it
// did". The audit row is the system's, because no human acted at that moment,
// and it says the claim is the device's. A replayed report changes nothing.
func TestRecordIdentityReleasedStampsOnceAndAuditsTheDevicesClaim(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	repo := &releaseStampFakeRepo{row: &node_tombstone.Tombstone{
		ID: nodeID, AccountID: accountID, Hostname: "rack3", HardwareSerial: "10000000abcd0001",
		DeletedBy: uuid.New(),
	}}
	recorder, sink := audittest.New()
	svc := &Service{
		tombstoneRepo: repo,
		audit:         recorder,
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := svc.RecordIdentityReleased(context.Background(), accountID, nodeID); err != nil {
		t.Fatalf("first report: %v", err)
	}
	if repo.row.IdentityReleasedAt == nil {
		t.Fatal("the first report did not stamp identity_released_at")
	}
	first := *repo.row.IdentityReleasedAt
	events := sink.Events()
	if len(events) != 1 || events[0].Action != audit_event.ActionNodeIdentityReleased {
		t.Fatalf("audit = %v, want one %s row", sink.Actions(), audit_event.ActionNodeIdentityReleased)
	}
	if events[0].ActorUserID != nil {
		t.Error("the device's report was attributed to a user")
	}
	if !strings.Contains(events[0].Detail, "reported_by=device") {
		t.Errorf("detail %q does not say the claim is the device's", events[0].Detail)
	}

	// Replay: no second stamp, no second audit row.
	if err := svc.RecordIdentityReleased(context.Background(), accountID, nodeID); err != nil {
		t.Fatalf("replayed report: %v", err)
	}
	if !repo.row.IdentityReleasedAt.Equal(first) {
		t.Error("a replayed report moved identity_released_at")
	}
	if len(sink.Events()) != 1 {
		t.Errorf("a replayed report recorded a second audit row: %v", sink.Actions())
	}
}

// releaseStampFakeRepo behaves like the SQL repository's conditional update:
// the stamp lands once, and a second attempt reports nothing to do.
type releaseStampFakeRepo struct {
	repository.NodeTombstoneRepository
	row *node_tombstone.Tombstone
}

func (f *releaseStampFakeRepo) MarkIdentityReleased(_ context.Context, accountID, nodeID uuid.UUID, at time.Time) error {
	if f.row == nil || f.row.ID != nodeID || f.row.AccountID != accountID || f.row.IdentityReleasedAt != nil {
		return app_errors.ErrNotFound
	}
	f.row.IdentityReleasedAt = &at
	return nil
}

func (f *releaseStampFakeRepo) GetByID(_ context.Context, accountID, nodeID uuid.UUID) (*node_tombstone.Tombstone, error) {
	if f.row == nil || f.row.ID != nodeID || f.row.AccountID != accountID {
		return nil, app_errors.ErrNotFound
	}
	copied := *f.row
	return &copied, nil
}

// A device that still presents a deleted node's id AND token is out there and
// trying; the record says so — and only then. Naming the id proves nothing, a
// token nobody can read proves nothing, and a restored record is no longer a
// deleted device's. The refusal itself is untouched: this function returns
// nothing to change it with.
func TestNoteDeletedNodeRefusedStampsOnlyAMatchingCredential(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	mk := func(over func(*node_tombstone.Tombstone)) (*Service, *refusedStampFakeRepo) {
		row := &node_tombstone.Tombstone{
			ID: nodeID, AccountID: accountID, UniqueToken: "the-real-token",
			HardwareSerial: "10000000abcd0001",
		}
		if over != nil {
			over(row)
		}
		repo := &refusedStampFakeRepo{row: row}
		return &Service{tombstoneRepo: repo, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, repo
	}

	svc, repo := mk(nil)
	svc.NoteDeletedNodeRefused(context.Background(), accountID, nodeID, "the-real-token", "10000000ABCD0001")
	if repo.row.LastRefusedAt == nil {
		t.Fatal("a matching token and serial did not stamp last_refused_at")
	}

	cases := map[string]struct {
		over          func(*node_tombstone.Tombstone)
		token, serial string
	}{
		"wrong token":      {nil, "guess", "10000000abcd0001"},
		"empty token":      {nil, "", "10000000abcd0001"},
		"wrong serial":     {nil, "the-real-token", "10000000deadbeef"},
		"unreadable token": {func(r *node_tombstone.Tombstone) { r.UniqueTokenUnreadable = true }, "the-real-token", "10000000abcd0001"},
		"already restored": {func(r *node_tombstone.Tombstone) { now := time.Now(); r.RestoredAt = &now }, "the-real-token", "10000000abcd0001"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			svc, repo := mk(c.over)
			svc.NoteDeletedNodeRefused(context.Background(), accountID, nodeID, c.token, c.serial)
			if repo.row.LastRefusedAt != nil {
				t.Error("stamped last_refused_at without a matching, readable, live credential")
			}
		})
	}

	// A record with no serial (a node that never reported hardware) cannot
	// check one, and the token alone is the evidence.
	svc, repo = mk(func(r *node_tombstone.Tombstone) { r.HardwareSerial = "" })
	svc.NoteDeletedNodeRefused(context.Background(), accountID, nodeID, "the-real-token", "whatever")
	if repo.row.LastRefusedAt == nil {
		t.Error("a serial-less record was not stamped on a matching token")
	}
}

type refusedStampFakeRepo struct {
	repository.NodeTombstoneRepository
	row *node_tombstone.Tombstone
}

func (f *refusedStampFakeRepo) GetByID(_ context.Context, accountID, nodeID uuid.UUID) (*node_tombstone.Tombstone, error) {
	if f.row == nil || f.row.ID != nodeID || f.row.AccountID != accountID {
		return nil, app_errors.ErrNotFound
	}
	copied := *f.row
	return &copied, nil
}

func (f *refusedStampFakeRepo) MarkRefused(_ context.Context, accountID, nodeID uuid.UUID, at time.Time, _ time.Duration) error {
	if f.row != nil && f.row.ID == nodeID && f.row.AccountID == accountID && f.row.RestoredAt == nil {
		f.row.LastRefusedAt = &at
	}
	return nil
}
