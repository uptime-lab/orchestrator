package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/schema"
)

// schemaLedgerTimeout bounds each ledger statement at boot. A database that
// does not answer the read within it refuses the boot like any other read
// failure.
const schemaLedgerTimeout = 30 * time.Second

// checkSchemaLedger is the boot's first question to the database, and it MUST
// run before the first repository Setup(): AutoMigrate is the only thing a
// refusal protects the database from, so a check placed after any Setup()
// would refuse a database it had already altered. TestSchemaLedgerCheckRunsBeforeAnySetup
// holds main() to that order.
//
// It returns the ledger as read, for the intent row recorded before the
// migrations, and an error that the caller turns into exit 1: a *schema.NewerSchemaError
// when a newer build migrated this database, or the read failure itself — a
// ledger that cannot be read fails CLOSED, because only a missing table means
// "no ledger".
func checkSchemaLedger(ctx context.Context, repo repository.SchemaLedgerRepository, build int, logger *slog.Logger) (schema.Ledger, error) {
	ctx, cancel := context.WithTimeout(ctx, schemaLedgerTimeout)
	defer cancel()
	ledger, err := repo.Read(ctx)
	if err != nil {
		return schema.Ledger{}, fmt.Errorf("%w; refusing to start without knowing which schema this database carries — the database was NOT touched", err)
	}
	decision, err := schema.Check(ledger, build)
	if err != nil {
		return ledger, err
	}
	logger.Info("schema-version ledger read",
		"ledger_present", ledger.Present,
		"stored_version", ledger.Highest,
		"build_version", build,
		"decision", decision.String())
	return ledger, nil
}

// recordSchemaIntent creates the ledger table if needed and writes this
// build's row with migrated=false — right after checkSchemaLedger decided to
// proceed and BEFORE the first product Setup(). The ledger table's own DDL
// is the one statement allowed between the decision and the migrations: the
// row has to exist before them, so that a build whose migrations fail
// part-way still leaves a row an older build is refused on. Before that
// change the row was written only after every
// Setup() succeeded, and a failed upgrade left half-migrated tables that an
// older build then started on.
func recordSchemaIntent(ctx context.Context, repo repository.SchemaLedgerRepository, setup func() error, before schema.Ledger, build int, revision string, logger *slog.Logger) error {
	if err := setup(); err != nil {
		return fmt.Errorf("creating the schema-version ledger: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, schemaLedgerTimeout)
	defer cancel()
	entry := schema.NewEntry(before, build, revision)
	written, err := repo.Record(ctx, entry)
	if err != nil {
		return err
	}
	logger.Info("schema-version ledger: migration intent recorded",
		"schema_version", entry.SchemaVersion,
		"previous_version", entry.PreviousVersion,
		"build_revision", entry.BuildRevision,
		"new_row", written)
	return nil
}

// markSchemaMigrated records that every Setup() succeeded: this build's row
// becomes migrated=true. Called only after the last Setup(); a boot whose
// migrations failed exits before here and leaves the row unfinished, which
// the next boot of the same build reads as "migrate again" and an older
// build reads as a newer schema.
func markSchemaMigrated(ctx context.Context, repo repository.SchemaLedgerRepository, build int, revision string, logger *slog.Logger) error {
	ctx, cancel := context.WithTimeout(ctx, schemaLedgerTimeout)
	defer cancel()
	if err := repo.MarkMigrated(ctx, build, revision); err != nil {
		return err
	}
	logger.Info("schema-version ledger: migrations recorded as finished",
		"schema_version", build, "build_revision", revision)
	return nil
}
