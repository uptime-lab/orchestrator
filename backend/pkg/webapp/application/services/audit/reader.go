package audit

import (
	"context"
	"log/slog"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/audit_event_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// Reader serves the audit trail's read route. A separate type from Recorder on
// purpose: every writer in the codebase holds a Recorder, and a Recorder that
// could also read would hand every one of those services a read capability
// none of them needs. Only the HTTP handler holds a Reader.
type Reader struct {
	repo   repository.AuditEventRepository
	logger *slog.Logger
}

// NewReader wires the reader. The repository is required, mirroring
// NewRecorder: a Reader that silently serves nothing is a coverage lie.
func NewReader(repo repository.AuditEventRepository, logger *slog.Logger) *Reader {
	if repo == nil {
		panic("audit.Reader requires an audit event repository")
	}
	if logger == nil {
		panic("audit.Reader requires a logger")
	}
	return &Reader{repo: repo, logger: logger.With("service", "audit_reader")}
}

// List returns one page of the account's events, newest first, and the total
// matching the filter. The query must already be Validate()d (the handler
// does); the account scope comes from the query's auth context and nowhere
// else.
func (r *Reader) List(ctx context.Context, query audit_event_list.AuditEventListQuery) ([]audit_event.AuditEvent, int64, error) {
	filter := repository.AuditEventListFilter{
		Action:      query.Action,
		ActorUserID: query.ActorUserID,
	}
	offset := (query.Page - 1) * query.PerPage
	return r.repo.List(ctx, query.AuthContext.AccountID, filter, offset, query.PerPage)
}
