package grpc

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

type fileReadFakeNodeService struct {
	services.NodeService
	calls  int
	result map[string]string
}

func (f *fileReadFakeNodeService) CompleteTrackedFileRead(_ context.Context, _ *node_command.NodeCommand, result map[string]string) error {
	f.calls++
	f.result = result
	return nil
}

// A successful read_file reaches the tracked-file recorder with the device's
// result, so tracking completes without a client polling; any other outcome
// or slug does not.
func TestSettleTrackedFileReadHandsTheResultOver(t *testing.T) {
	read := &node_command.NodeCommand{ID: uuid.New(), NodeID: uuid.New(), AccountID: uuid.New(), Slug: node_command.SlugReadFile}
	fake := &fileReadFakeNodeService{}
	srv := &Server{nodeService: fake, logger: slog.Default()}

	srv.settleTrackedFileRead(context.Background(), read, "success", map[string]string{"content": "x"})
	if fake.calls != 1 || fake.result["content"] != "x" {
		t.Fatalf("a successful read was not handed over: %d calls, %v", fake.calls, fake.result)
	}

	srv.settleTrackedFileRead(context.Background(), read, "failed", map[string]string{"content": "x"})
	other := &node_command.NodeCommand{ID: uuid.New(), Slug: node_command.SlugRunScript}
	srv.settleTrackedFileRead(context.Background(), other, "success", nil)
	if fake.calls != 1 {
		t.Fatalf("a failed read or another slug reached the recorder: %d calls", fake.calls)
	}
}
