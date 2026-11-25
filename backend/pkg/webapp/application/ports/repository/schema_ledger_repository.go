package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/schema"
)

// SchemaLedgerRepository reads and appends the schema-version ledger. Global:
// the schema belongs to the install, not to a tenant.
//
// The halves run at opposite ends of the boot's migration block. Read runs
// BEFORE any migration and must not write; the ledger table's own Setup and
// the intent Record run right after the decision and before any product
// table's Setup(); MarkMigrated runs AFTER every other Setup() succeeded.
type SchemaLedgerRepository interface {
	// Read returns the ledger. A missing table is Present=false; any other
	// failure is an error, which the boot treats as a refusal.
	Read(ctx context.Context) (schema.Ledger, error)
	// Record appends the entry unless the ledger already holds the same
	// (version, revision) pair, and reports whether a row was written.
	Record(ctx context.Context, e schema.Entry) (bool, error)
	// MarkMigrated records that the (version, revision) row's migrations
	// finished.
	MarkMigrated(ctx context.Context, version int, revision string) error
}
