package sql

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The two abandonment queries differ by exactly one clause — whether reboot is
// excluded — and getting that clause backwards is not a compile error, it is a
// reboot failed at the moment it succeeded (or a node pinned 'restarting'
// forever). Both are expressed in SQL, so they are tested against a real
// Postgres:
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./pkg/webapp/infrastructure/database/sql/

// passthroughEncryptor keeps the payload path out of the way: these tests are
// about the WHERE clause, not about encryption.
type passthroughEncryptor struct{}

func (passthroughEncryptor) Encrypt(plaintext string) (string, error)  { return plaintext, nil }
func (passthroughEncryptor) Decrypt(ciphertext string) (string, error) { return ciphertext, nil }

func staleCommandRepo(t *testing.T) *NodeCommandRepository {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := db.Exec(`DROP TABLE IF EXISTS node_commands CASCADE`).Error; err != nil {
		t.Fatalf("dropping: %v", err)
	}
	if err := db.AutoMigrate(&NodeCommandModel{}); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	// The sweeps tell connected nodes from offline ones by joining nodes, so
	// the table has to exist — rebuilt from nothing, as agentInstanceRepo does.
	if err := db.Exec(`DROP TABLE IF EXISTS nodes CASCADE`).Error; err != nil {
		t.Fatalf("dropping nodes: %v", err)
	}
	if err := db.AutoMigrate(&NodeModel{}); err != nil {
		t.Fatalf("migrating nodes: %v", err)
	}
	return NewNodeCommandRepository(db, passthroughEncryptor{})
}

// seedNodeWithStatus writes a node row in the given status and returns its id.
func seedNodeWithStatus(t *testing.T, repo *NodeCommandRepository, status node.NodeStatus) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := repo.db.Exec(
		`INSERT INTO nodes (id, account_id, name, unique_token, status, approval_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'approved', now(), now())`,
		id, uuid.New(), "sweep-test-"+id.String()[:8], uuid.New().String(), string(status)).Error; err != nil {
		t.Fatalf("seeding node: %v", err)
	}
	return id
}

// seedCommand writes a row with a chosen updated_at, which GORM would otherwise
// overwrite with now() — and updated_at is half of the abandonment predicate.
func seedCommand(t *testing.T, repo *NodeCommandRepository, nodeID uuid.UUID, slug string, status node_command.NodeCommandStatus, queueable bool, updatedAt time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	err := repo.db.Exec(`INSERT INTO node_commands
		 (id, node_id, account_id, slug, status, progress, queueable, attempts, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 0, ?, 0, ?, ?)`,
		id, nodeID, uuid.New(), slug, string(status), queueable, updatedAt, updatedAt).Error
	if err != nil {
		t.Fatalf("seeding %s/%s: %v", slug, status, err)
	}
	return id
}

func containsID(commands []node_command.NodeCommand, id uuid.UUID) bool {
	for _, cmd := range commands {
		if cmd.ID == id {
			return true
		}
	}
	return false
}

// The asymmetry is the point: on reconnect a reboot has just completed, so it
// must be left alone; for a node that never came back it is the one command
// that certainly did not.
func TestOnlyTheUnreachableQueryClaimsReboot(t *testing.T) {
	repo := staleCommandRepo(t)
	nodeID := uuid.New()
	stale := time.Now().Add(-time.Hour)
	reboot := seedCommand(t, repo, nodeID, node_command.SlugReboot, node_command.StatusInProgress, false, stale)

	onReconnect, err := repo.GetStalePendingNonRebootCommandsByNodeID(context.Background(), nodeID, node.AgentInstanceUnknown)
	if err != nil {
		t.Fatalf("reconnect query: %v", err)
	}
	if containsID(onReconnect, reboot) {
		t.Fatal("the reconnect query claimed a reboot — that fails the command at the moment it succeeded")
	}

	unreachable, err := repo.GetStalePendingCommandsByNodeID(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("unreachable query: %v", err)
	}
	if !containsID(unreachable, reboot) {
		t.Fatal("the unreachable query skipped the reboot — this is what pinned the node in 'restarting' forever")
	}
}

// A queued command for an offline node is doing exactly what it was designed to
// do. Failing it would break the queue feature every time a node stayed away.
func TestAQueuedCommandIsNeverClaimedByEitherQuery(t *testing.T) {
	repo := staleCommandRepo(t)
	nodeID := uuid.New()
	queued := seedCommand(t, repo, nodeID, node_command.SlugApplyAccessConfig, node_command.StatusRegistered, true, time.Now().Add(-time.Hour))

	for name, query := range map[string]func(context.Context, uuid.UUID) ([]node_command.NodeCommand, error){
		"reconnect": func(ctx context.Context, id uuid.UUID) ([]node_command.NodeCommand, error) {
			return repo.GetStalePendingNonRebootCommandsByNodeID(ctx, id, node.AgentInstanceUnknown)
		},
		"unreachable": repo.GetStalePendingCommandsByNodeID,
	} {
		commands, err := query(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("%s query: %v", name, err)
		}
		if containsID(commands, queued) {
			t.Fatalf("the %s query claimed a queued command; waiting for an offline node is its whole purpose", name)
		}
	}
}

// An in_progress command that is still reporting is alive. install_os reports
// continuously for many minutes and must survive.
func TestAnInProgressCommandStillReportingIsNotAbandoned(t *testing.T) {
	repo := staleCommandRepo(t)
	nodeID := uuid.New()
	fresh := seedCommand(t, repo, nodeID, node_command.SlugInstallOS, node_command.StatusInProgress, false, time.Now())
	silent := seedCommand(t, repo, nodeID, node_command.SlugInstallOS, node_command.StatusInProgress, false, time.Now().Add(-time.Hour))

	commands, err := repo.GetStalePendingCommandsByNodeID(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if containsID(commands, fresh) {
		t.Fatal("claimed a command that reported progress seconds ago")
	}
	if !containsID(commands, silent) {
		t.Fatal("skipped a command whose agent stopped reporting an hour ago")
	}
}

// A queued command that the agent actually started is a different case from one
// still waiting: execution does not resume across sessions.
func TestAQueuedCommandThatStartedAndWentSilentIsAbandoned(t *testing.T) {
	repo := staleCommandRepo(t)
	nodeID := uuid.New()
	started := seedCommand(t, repo, nodeID, node_command.SlugApplyAccessConfig, node_command.StatusInProgress, true, time.Now().Add(-time.Hour))

	commands, err := repo.GetStalePendingCommandsByNodeID(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !containsID(commands, started) {
		t.Fatal("a queueable command whose agent died mid-execution was left non-terminal")
	}
}

// Terminal commands must never be revisited, or a completed run would be
// rewritten as failed.
func TestTerminalCommandsAreLeftAlone(t *testing.T) {
	repo := staleCommandRepo(t)
	nodeID := uuid.New()
	stale := time.Now().Add(-time.Hour)
	success := seedCommand(t, repo, nodeID, node_command.SlugReboot, node_command.StatusSuccess, false, stale)
	failed := seedCommand(t, repo, nodeID, node_command.SlugReboot, node_command.StatusFailed, false, stale)

	commands, err := repo.GetStalePendingCommandsByNodeID(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if containsID(commands, success) || containsID(commands, failed) {
		t.Fatal("claimed a command that had already finished")
	}
}

// Tenancy: the query is keyed on the node, and another node's abandoned command
// must not be settled by this node's sweep.
func TestAnotherNodesCommandsAreNotClaimed(t *testing.T) {
	repo := staleCommandRepo(t)
	mine, theirs := uuid.New(), uuid.New()
	other := seedCommand(t, repo, theirs, node_command.SlugReboot, node_command.StatusInProgress, false, time.Now().Add(-time.Hour))

	commands, err := repo.GetStalePendingCommandsByNodeID(context.Background(), mine)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if containsID(commands, other) {
		t.Fatal("claimed a command belonging to a different node")
	}
}

// SupersedeRegisteredByKey must return the ids it retired, not just a count, so
// the caller can settle each — otherwise a superseded command linked to a
// workflow node leaves its block running forever and eats the active cap.
func TestSupersedeRegisteredByKeyReturnsTheSupersededIDs(t *testing.T) {
	repo := staleCommandRepo(t)
	ctx := context.Background()
	nodeID := uuid.New()
	key := "apply_access_config:" + nodeID.String()
	now := time.Now().UTC()

	seedWithKey := func(id uuid.UUID, status node_command.NodeCommandStatus, createdAt time.Time) {
		t.Helper()
		err := repo.db.Exec(`INSERT INTO node_commands
			 (id, node_id, account_id, slug, status, progress, queueable, attempts, supersede_key, created_at, updated_at)
			 VALUES (?, ?, ?, 'apply_access_config', ?, 0, true, 0, ?, ?, ?)`,
			id, nodeID, uuid.New(), string(status), key, createdAt, createdAt).Error
		if err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}

	older := uuid.New()
	seedWithKey(older, node_command.StatusRegistered, now.Add(-time.Second))
	alreadySent := uuid.New()
	seedWithKey(alreadySent, node_command.StatusSent, now.Add(-time.Second)) // executing on the node, must NOT be recalled
	newer := uuid.New()
	seedWithKey(newer, node_command.StatusRegistered, now)

	ids, err := repo.SupersedeRegisteredByKey(ctx, nodeID, key, newer, now)
	if err != nil {
		t.Fatalf("supersede: %v", err)
	}
	if len(ids) != 1 || ids[0] != older {
		t.Fatalf("returned ids = %v, want exactly [%s]", ids, older)
	}

	// The older one is now superseded; the sent one is untouched; the newer one
	// (excluded) stays registered.
	assertStatus := func(id uuid.UUID, want node_command.NodeCommandStatus) {
		t.Helper()
		var status string
		if err := repo.db.Raw(`SELECT status FROM node_commands WHERE id = ?`, id).Scan(&status).Error; err != nil {
			t.Fatalf("reading status: %v", err)
		}
		if status != string(want) {
			t.Errorf("command %s status = %s, want %s", id, status, want)
		}
	}
	assertStatus(older, node_command.StatusSuperseded)
	assertStatus(alreadySent, node_command.StatusSent)
	assertStatus(newer, node_command.StatusRegistered)
}

// Two concurrent registrations of the same slug used to retire EACH OTHER —
// each call excluded only its own id — leaving the node with zero pending
// commands. The created_at bound means only the newest survives,
// whichever order the two supersede sweeps run in.
func TestConcurrentRegistrationsDoNotSupersedeEachOtherToZero(t *testing.T) {
	repo := staleCommandRepo(t)
	ctx := context.Background()
	nodeID := uuid.New()
	key := "apply_access_config:" + nodeID.String()
	base := time.Now().UTC()

	seed := func(id uuid.UUID, createdAt time.Time) {
		t.Helper()
		err := repo.db.Exec(`INSERT INTO node_commands
			 (id, node_id, account_id, slug, status, progress, queueable, attempts, supersede_key, created_at, updated_at)
			 VALUES (?, ?, ?, 'apply_access_config', 'registered', 0, true, 0, ?, ?, ?)`,
			id, nodeID, uuid.New(), key, createdAt, createdAt).Error
		if err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}

	first := uuid.New()
	firstAt := base.Add(-time.Millisecond)
	seed(first, firstAt)
	second := uuid.New()
	seed(second, base)

	// The interleaving that used to zero the queue: the OLDER registration's
	// sweep runs after the newer row exists.
	ids, err := repo.SupersedeRegisteredByKey(ctx, nodeID, key, first, firstAt)
	if err != nil {
		t.Fatalf("older sweep: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("the older registration must not retire the newer one, retired %v", ids)
	}
	ids, err = repo.SupersedeRegisteredByKey(ctx, nodeID, key, second, base)
	if err != nil {
		t.Fatalf("newer sweep: %v", err)
	}
	if len(ids) != 1 || ids[0] != first {
		t.Fatalf("the newer registration should retire exactly the older, got %v", ids)
	}

	var status string
	if err := repo.db.Raw(`SELECT status FROM node_commands WHERE id = ?`, second).Scan(&status).Error; err != nil {
		t.Fatalf("reading survivor: %v", err)
	}
	if status != string(node_command.StatusRegistered) {
		t.Fatalf("the newest command must survive, status = %s", status)
	}
}

// A command dispatched moments before a stream blip must survive it.
//
// Without a time bound on the unacknowledged arm, ANY reconnect would fail every
// registered/sent command on the node — and a reconnect is not a restarted
// agent. A reverse-proxy restart, a network switch or a backend deploy reconnects the
// stream while the agent PROCESS keeps holding the command and still reports it;
// on install_os that would settle a run as failed while the disk is being written.
func TestAFreshlyDispatchedCommandSurvivesAReconnect(t *testing.T) {
	repo := staleCommandRepo(t)
	nodeID := uuid.New()
	fresh := seedCommand(t, repo, nodeID, node_command.SlugInstallOS, node_command.StatusSent, false, time.Now().Add(-30*time.Second))

	commands, err := repo.GetStalePendingNonRebootCommandsByNodeID(context.Background(), nodeID, node.AgentInstanceUnknown)
	if err != nil {
		t.Fatalf("reconnect query: %v", err)
	}
	if containsID(commands, fresh) {
		t.Fatal("a command dispatched 30 seconds ago was declared abandoned by a reconnect; the agent still has it")
	}
}

// The grace is a delay, not an amnesty: a command the agent never acknowledged
// still has to reach a terminal state, or it pins the node in maintenance.
func TestAnOldUnacknowledgedCommandIsStillClaimed(t *testing.T) {
	repo := staleCommandRepo(t)
	nodeID := uuid.New()
	old := seedCommand(t, repo, nodeID, node_command.SlugInstallOS, node_command.StatusSent, false, time.Now().Add(-time.Hour))

	commands, err := repo.GetStalePendingNonRebootCommandsByNodeID(context.Background(), nodeID, node.AgentInstanceUnknown)
	if err != nil {
		t.Fatalf("reconnect query: %v", err)
	}
	if !containsID(commands, old) {
		t.Fatal("an hour-old unacknowledged command was left pending; it pins the node in maintenance forever")
	}
}

// The continuity verdict replaces the grace guess for the in_progress arm.
// Each of these is a behaviour the pre-verdict query got wrong in its
// in_progress half: a >grace disconnect failed a live install, and a
// crash-and-quick-reconnect left lost work hanging until the grace expired.

// Same instance: the process that acknowledged the command reconnected — its
// in_progress work must survive the sweep no matter how stale updated_at is,
// because updated_at CANNOT advance while the stream is down.
func TestSameInstanceLeavesInProgressAloneHoweverStale(t *testing.T) {
	repo := staleCommandRepo(t)
	nodeID := uuid.New()
	inProgress := seedCommand(t, repo, nodeID, node_command.SlugInstallOS, node_command.StatusInProgress, false, time.Now().Add(-2*time.Hour))
	unacked := seedCommand(t, repo, nodeID, node_command.SlugSetHostname, node_command.StatusSent, false, time.Now().Add(-2*time.Hour))

	commands, err := repo.GetStalePendingNonRebootCommandsByNodeID(context.Background(), nodeID, node.AgentInstanceSame)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if containsID(commands, inProgress) {
		t.Error("a surviving process's in_progress command was claimed as abandoned — this is the live-install-reported-failed defect")
	}
	// The registered/sent arm keeps its grace in EVERY mode: "the process
	// survived" says nothing about whether a frame lost mid-disconnect was
	// ever delivered.
	if !containsID(commands, unacked) {
		t.Error("a stale unacknowledged command must still be claimed under Same — delivery is uncertain regardless of process survival")
	}
}

// Changed instance: execution does not resume across restarts, so in_progress
// work is claimed immediately — no grace, however fresh the row is.
func TestChangedInstanceClaimsFreshInProgress(t *testing.T) {
	repo := staleCommandRepo(t)
	nodeID := uuid.New()
	fresh := seedCommand(t, repo, nodeID, node_command.SlugInstallOS, node_command.StatusInProgress, false, time.Now().Add(-time.Minute))
	freshUnacked := seedCommand(t, repo, nodeID, node_command.SlugSetHostname, node_command.StatusSent, false, time.Now().Add(-time.Minute))

	commands, err := repo.GetStalePendingNonRebootCommandsByNodeID(context.Background(), nodeID, node.AgentInstanceChanged)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !containsID(commands, fresh) {
		t.Error("a restarted process's in_progress command must be claimed immediately — before the verdict it hung until the grace expired AND another reconnect happened")
	}
	// A fresh unacknowledged command stays exempt even under Changed: the NEW
	// process may be about to receive a command dispatched moments ago.
	if containsID(commands, freshUnacked) {
		t.Error("a fresh unacknowledged command must keep its grace under Changed")
	}
}

// Unknown keeps the pre-verdict behaviour byte for byte: grace on both arms.
func TestUnknownInstanceKeepsTheGraceHeuristic(t *testing.T) {
	repo := staleCommandRepo(t)
	nodeID := uuid.New()
	fresh := seedCommand(t, repo, nodeID, node_command.SlugInstallOS, node_command.StatusInProgress, false, time.Now().Add(-time.Minute))
	stale := seedCommand(t, repo, nodeID, node_command.SlugSetHostname, node_command.StatusInProgress, false, time.Now().Add(-time.Hour))

	commands, err := repo.GetStalePendingNonRebootCommandsByNodeID(context.Background(), nodeID, node.AgentInstanceUnknown)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if containsID(commands, fresh) {
		t.Error("Unknown must not claim an in_progress command inside the grace")
	}
	if !containsID(commands, stale) {
		t.Error("Unknown must still claim an in_progress command past the grace")
	}
}

// The backstop's exclusion is a safety property expressed only in SQL, so a
// unit test cannot see it: removing the clause compiles and passes everything
// else. What it protects is real work — capture_image streams a multi-gigabyte
// disk and install_os rewrites one, and neither can be told apart from wedged
// by a clock. Those keep the two evidence-based sweeps, which fire when the
// agent is demonstrably gone.
func TestTheBackstopLeavesTheDiskOperationsAlone(t *testing.T) {
	repo := staleCommandRepo(t)
	nodeID := uuid.New()
	silent := time.Now().Add(-2 * time.Hour)

	utility := seedCommand(t, repo, nodeID, node_command.SlugApplyUtilityConfig, node_command.StatusInProgress, true, silent)
	capture := seedCommand(t, repo, nodeID, node_command.SlugCaptureImage, node_command.StatusInProgress, false, silent)
	install := seedCommand(t, repo, nodeID, node_command.SlugInstallOS, node_command.StatusInProgress, false, silent)
	erase := seedCommand(t, repo, nodeID, node_command.SlugEraseDrive, node_command.StatusInProgress, false, silent)

	got, err := repo.GetAbandonedInProgressCommands(context.Background(), time.Now().Add(-30*time.Minute), 100)
	if err != nil {
		t.Fatalf("backstop query: %v", err)
	}
	if !containsID(got, utility) {
		t.Error("the backstop skipped a utility apply that had been silent for two hours")
	}
	for name, id := range map[string]uuid.UUID{
		"capture_image": capture,
		"install_os":    install,
		"erase_drive":   erase,
	} {
		if containsID(got, id) {
			t.Errorf("the backstop claimed %s — a clock cannot tell that operation from a slow one, and failing it destroys real work", name)
		}
	}
}

// Silence is the whole predicate, so a command that reported recently must
// survive it however long it has been running.
func TestTheBackstopIgnoresACommandThatIsStillReporting(t *testing.T) {
	repo := staleCommandRepo(t)
	nodeID := uuid.New()

	fresh := seedCommand(t, repo, nodeID, node_command.SlugApplyUtilityConfig, node_command.StatusInProgress, true, time.Now().Add(-2*time.Minute))
	settled := seedCommand(t, repo, nodeID, node_command.SlugApplyUtilityConfig, node_command.StatusSuccess, true, time.Now().Add(-2*time.Hour))

	got, err := repo.GetAbandonedInProgressCommands(context.Background(), time.Now().Add(-30*time.Minute), 100)
	if err != nil {
		t.Fatalf("backstop query: %v", err)
	}
	if containsID(got, fresh) {
		t.Error("the backstop claimed a command that reported two minutes ago")
	}
	if containsID(got, settled) {
		t.Error("the backstop claimed a command that had already succeeded")
	}
}

// A node the monitor moved from 'maintenance' straight to 'offline' kept its
// install_os in_progress forever: the unreachable sweep only visits transient
// statuses and the connected-node backstop skips disk operations. The offline
// query is the half that claims it — and only it, only when silent, and never a
// command whose purpose is to wait for the node.
func TestTheOfflineQueryClaimsWhatAnOfflineNodeWasCarrying(t *testing.T) {
	repo := staleCommandRepo(t)
	offline := seedNodeWithStatus(t, repo, node.StatusOffline)
	online := seedNodeWithStatus(t, repo, node.StatusOnline)
	silent := time.Now().Add(-2 * time.Hour)

	install := seedCommand(t, repo, offline, node_command.SlugInstallOS, node_command.StatusInProgress, false, silent)
	sent := seedCommand(t, repo, offline, node_command.SlugRunScript, node_command.StatusSent, false, silent)
	queued := seedCommand(t, repo, offline, node_command.SlugSetHostname, node_command.StatusRegistered, true, silent)
	fresh := seedCommand(t, repo, offline, node_command.SlugEraseDrive, node_command.StatusInProgress, false, time.Now().Add(-time.Minute))
	done := seedCommand(t, repo, offline, node_command.SlugInstallOS, node_command.StatusSuccess, false, silent)
	onOnline := seedCommand(t, repo, online, node_command.SlugInstallOS, node_command.StatusInProgress, false, silent)

	got, err := repo.GetUnsettledCommandsOfOfflineNodes(context.Background(), time.Now().Add(-30*time.Minute), 100)
	if err != nil {
		t.Fatalf("offline query: %v", err)
	}
	if !containsID(got, install) {
		t.Error("the install of an offline node was left in_progress")
	}
	if !containsID(got, sent) {
		t.Error("a delivered command of an offline node was left unsettled")
	}
	for name, id := range map[string]uuid.UUID{
		"a queued command (waiting is its purpose)":    queued,
		"a command that reported a minute ago":         fresh,
		"a command that already finished":              done,
		"a command of a node the platform still hears": onOnline,
	} {
		if containsID(got, id) {
			t.Errorf("the offline query claimed %s", name)
		}
	}
}

// A command's own silence is not the evidence: a capture shrinking a large
// filesystem says nothing for longer than the grace. A node marked offline a
// moment ago by a dropped stream may still be doing that work, so the grace
// counts from the node's own last sighting too.
func TestTheOfflineQueryWaitsForTheNodeToBeAwayForTheGrace(t *testing.T) {
	repo := staleCommandRepo(t)
	justDropped := seedNodeWithStatus(t, repo, node.StatusOffline)
	longGone := seedNodeWithStatus(t, repo, node.StatusOffline)
	if err := repo.db.Exec(`UPDATE nodes SET last_seen_at = ?, last_heartbeat = ? WHERE id = ?`,
		time.Now().Add(-time.Minute), time.Now().Add(-time.Minute), justDropped).Error; err != nil {
		t.Fatalf("stamping last sighting: %v", err)
	}
	if err := repo.db.Exec(`UPDATE nodes SET last_seen_at = ?, last_heartbeat = ? WHERE id = ?`,
		time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour), longGone).Error; err != nil {
		t.Fatalf("stamping last sighting: %v", err)
	}
	silent := time.Now().Add(-2 * time.Hour)
	quietCapture := seedCommand(t, repo, justDropped, node_command.SlugCaptureImage, node_command.StatusInProgress, false, silent)
	abandoned := seedCommand(t, repo, longGone, node_command.SlugCaptureImage, node_command.StatusInProgress, false, silent)

	got, err := repo.GetUnsettledCommandsOfOfflineNodes(context.Background(), time.Now().Add(-30*time.Minute), 100)
	if err != nil {
		t.Fatalf("offline query: %v", err)
	}
	if containsID(got, quietCapture) {
		t.Error("the offline query failed a quiet command on a node that was seen a minute ago")
	}
	if !containsID(got, abandoned) {
		t.Error("the offline query left the command of a node away for two hours unsettled")
	}
}

// The connected-node backstop releases the node back to online after it
// settles, which is a lie for an offline node — so offline nodes are the other
// query's, and this one must not see them.
func TestTheConnectedBackstopLeavesOfflineNodesToTheirOwnSweep(t *testing.T) {
	repo := staleCommandRepo(t)
	offline := seedNodeWithStatus(t, repo, node.StatusOffline)
	online := seedNodeWithStatus(t, repo, node.StatusOnline)
	silent := time.Now().Add(-2 * time.Hour)

	onOffline := seedCommand(t, repo, offline, node_command.SlugApplyUtilityConfig, node_command.StatusInProgress, true, silent)
	onOnline := seedCommand(t, repo, online, node_command.SlugApplyUtilityConfig, node_command.StatusInProgress, true, silent)

	got, err := repo.GetAbandonedInProgressCommands(context.Background(), time.Now().Add(-30*time.Minute), 100)
	if err != nil {
		t.Fatalf("backstop query: %v", err)
	}
	if containsID(got, onOffline) {
		t.Error("the connected-node backstop claimed a command of an offline node")
	}
	if !containsID(got, onOnline) {
		t.Error("the connected-node backstop skipped a silent command of an online node")
	}
}

// Node delete settles what the node still owed, and nothing it had finished.
func TestUnsettledCommandsByNodeAreTheNonTerminalOnes(t *testing.T) {
	repo := staleCommandRepo(t)
	nodeID := uuid.New()
	now := time.Now()

	queued := seedCommand(t, repo, nodeID, node_command.SlugSetHostname, node_command.StatusRegistered, true, now)
	sent := seedCommand(t, repo, nodeID, node_command.SlugReboot, node_command.StatusSent, false, now)
	running := seedCommand(t, repo, nodeID, node_command.SlugInstallOS, node_command.StatusInProgress, false, now)
	done := seedCommand(t, repo, nodeID, node_command.SlugReboot, node_command.StatusSuccess, false, now)
	other := seedCommand(t, repo, uuid.New(), node_command.SlugSetHostname, node_command.StatusRegistered, true, now)

	got, err := repo.GetUnsettledCommandsByNodeID(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for name, id := range map[string]uuid.UUID{"queued": queued, "sent": sent, "in_progress": running} {
		if !containsID(got, id) {
			t.Errorf("the %s command was not returned", name)
		}
	}
	if containsID(got, done) {
		t.Error("a finished command was returned; settling it would rewrite its outcome")
	}
	if containsID(got, other) {
		t.Error("another node's command was returned")
	}
}
