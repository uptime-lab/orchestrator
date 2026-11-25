package organizational_unit

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// recordingAuditRepo captures audit events for assertions. Safe for the
// detached fleet-push goroutine, which shares the recorder with the request
// path.
type recordingAuditRepo struct {
	repository.AuditEventRepository
	mu     sync.Mutex
	events []audit_event.AuditEvent
}

func (r *recordingAuditRepo) Create(_ context.Context, e *audit_event.AuditEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, *e)
	return nil
}

func (r *recordingAuditRepo) recorded() []audit_event.AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit_event.AuditEvent(nil), r.events...)
}

// testAuditRecorder returns a real Recorder over a recording repo, so fixtures
// exercise the actual actor→event mapping instead of stubbing it away.
func testAuditRecorder() (*audit.Recorder, *recordingAuditRepo) {
	repo := &recordingAuditRepo{}
	return audit.NewRecorder(repo, slog.New(slog.NewTextHandler(io.Discard, nil))), repo
}
