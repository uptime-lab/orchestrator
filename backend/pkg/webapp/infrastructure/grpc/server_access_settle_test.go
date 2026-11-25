package grpc

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// accessFakeNodeService records the two generation calls the settle can make;
// the embedded nil interface panics on anything else, proving this path touches
// nothing more.
type accessFakeNodeService struct {
	services.NodeService

	recorded     string
	recordCalls  int
	clearedCalls int
}

func (f *accessFakeNodeService) RecordAccessApplied(_ context.Context, _, _ uuid.UUID, generation string) error {
	f.recordCalls++
	f.recorded = generation
	return nil
}

func (f *accessFakeNodeService) ClearAccessApplied(_ context.Context, _, _ uuid.UUID) error {
	f.clearedCalls++
	return nil
}

func accessSettleServer(fake *accessFakeNodeService) *Server {
	return &Server{nodeService: fake, logger: slog.Default()}
}

func accessCmd() *node_command.NodeCommand {
	return &node_command.NodeCommand{
		ID:        uuid.New(),
		NodeID:    uuid.New(),
		AccountID: uuid.New(),
		Slug:      node_command.SlugApplyAccessConfig,
	}
}

func TestSettleAccessApplyRecordsTheConfirmedGeneration(t *testing.T) {
	fake := &accessFakeNodeService{}
	accessSettleServer(fake).settleAccessApply(context.Background(), accessCmd(), "success",
		map[string]string{"generation": "hash-1", "noop": "false", "skipped_unmanaged": "[]"})

	if fake.recordCalls != 1 || fake.recorded != "hash-1" {
		t.Fatalf("expected the generation to be recorded once, got %d calls %q", fake.recordCalls, fake.recorded)
	}
}

func TestSettleAccessApplyIgnoresAFailedApply(t *testing.T) {
	// The agent echoes the DESIRED generation in its failure report — it is the
	// hash it was asked for, not one it reached — so trusting it would record
	// convergence for a reconcile that did not happen.
	fake := &accessFakeNodeService{}
	accessSettleServer(fake).settleAccessApply(context.Background(), accessCmd(), "failed",
		map[string]string{"generation": "hash-1"})

	if fake.recordCalls != 0 || fake.clearedCalls != 0 {
		t.Fatal("a failed apply must make no claim about what the device has")
	}
}

func TestSettleAccessApplyClearsOnAPartialApply(t *testing.T) {
	// A success that skipped an unmanaged user leaves the agent's own state
	// partial. Recording the generation would make the next push of the same
	// config a no-op, so an operator who fixes the cause and re-applies would be
	// told "already current" while nothing changed.
	fake := &accessFakeNodeService{}
	accessSettleServer(fake).settleAccessApply(context.Background(), accessCmd(), "success",
		map[string]string{"generation": "hash-1", "skipped_unmanaged": `["deploy"]`})

	if fake.recordCalls != 0 {
		t.Error("a partial apply must not record a generation")
	}
	if fake.clearedCalls != 1 {
		t.Error("a partial apply must clear any stale generation rather than leave it")
	}
}

func TestSettleAccessApplyIgnoresAResultWithNoGeneration(t *testing.T) {
	// A non-Linux agent fails the command with no result map at all; an older
	// build may report success without the key. Neither is a claim.
	fake := &accessFakeNodeService{}
	accessSettleServer(fake).settleAccessApply(context.Background(), accessCmd(), "success",
		map[string]string{"noop": "true"})

	if fake.recordCalls != 0 || fake.clearedCalls != 0 {
		t.Fatal("a result with no generation must change nothing")
	}
}

func TestSettleAccessApplyIgnoresOtherSlugs(t *testing.T) {
	fake := &accessFakeNodeService{}
	cmd := accessCmd()
	cmd.Slug = node_command.SlugReboot
	accessSettleServer(fake).settleAccessApply(context.Background(), cmd, "success",
		map[string]string{"generation": "hash-1"})

	if fake.recordCalls != 0 {
		t.Fatal("only apply_access_config settles an access generation")
	}
}

func TestAccessApplySkippedUsersReadsTheEncodedList(t *testing.T) {
	// The agent encodes an EMPTY list as "[]", not "". Testing the raw string for
	// emptiness would read every clean apply as partial, so no node would ever
	// record a generation.
	for _, clean := range []string{"", "[]", "  []  "} {
		if accessApplySkippedUsers(clean) {
			t.Errorf("%q means nothing was skipped", clean)
		}
	}
	for _, partial := range []string{`["deploy"]`, `["a","b"]`, "not json at all"} {
		if !accessApplySkippedUsers(partial) {
			t.Errorf("%q must count as a partial apply", partial)
		}
	}
}
