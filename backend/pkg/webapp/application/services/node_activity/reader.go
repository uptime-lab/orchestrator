// Package node_activity serves the fleet activity log: the account's device
// commands, newest first.
//
// It is a Reader and nothing else, the same split the audit trail uses. Every
// service that dispatches a command already holds the command repository; a
// type that could also list the account's whole command history would hand
// that reach to all of them, and none of them needs it.
package node_activity

import (
	"context"
	"log/slog"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/node_activity_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// Reader lists the account's device commands.
type Reader struct {
	repo   repository.NodeCommandRepository
	logger *slog.Logger
}

// NewReader builds the reader.
func NewReader(repo repository.NodeCommandRepository, logger *slog.Logger) *Reader {
	if repo == nil {
		panic("node_activity.Reader requires a node command repository")
	}
	if logger == nil {
		panic("node_activity.Reader requires a logger")
	}
	return &Reader{repo: repo, logger: logger.With("service", "node_activity")}
}

// List returns one page of the account's commands plus the total matching the
// filter. The query's visibility set is passed through untouched, including
// the empty-means-nothing case.
func (r *Reader) List(ctx context.Context, query node_activity_list.NodeActivityListQuery) ([]node_command.NodeCommand, int64, error) {
	filter := repository.NodeCommandActivityFilter{
		NodeIDs:  query.VisibleNodeIDs,
		Statuses: query.Statuses,
		Slug:     query.Slug,
		Since:    query.Since,
		Until:    query.Until,
	}
	offset := (query.Page - 1) * query.PerPage
	return r.repo.ListActivity(ctx, query.AuthContext.AccountID, filter, offset, query.PerPage)
}
