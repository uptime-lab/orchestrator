package sql

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/schema"
)

var _ repository.SchemaLedgerRepository = (*SchemaLedgerRepository)(nil)

// pgUndefinedTable is SQLSTATE 42P01. The ledger read tolerates exactly this
// code and nothing else, matched on the code and never on the message text: a
// connection failure, a permission error or a timeout must refuse the boot,
// because reading "no ledger" from an unreadable one is how an older build
// would migrate a newer schema.
const pgUndefinedTable = "42P01"

// SchemaVersionModel is one row of the schema-version ledger: a build that
// started migrating this database, written BEFORE its first product
// migration with migrated=false and marked migrated=true after the last one
// succeeded. No account_id: the schema
// belongs to the install, so no account purge may touch the table.
//
// One row per distinct (schema_version, build_revision), enforced by the
// unique index, so the table grows with releases and never with boots.
type SchemaVersionModel struct {
	ID              int64     `gorm:"column:id;primaryKey;autoIncrement"`
	SchemaVersion   int       `gorm:"column:schema_version;not null;uniqueIndex:idx_schema_versions_version_revision,priority:1"`
	BuildRevision   string    `gorm:"column:build_revision;size:64;not null;uniqueIndex:idx_schema_versions_version_revision,priority:2"`
	PreviousVersion int       `gorm:"column:previous_version;not null;default:0"`
	Migrated        bool      `gorm:"column:migrated;not null"`
	AppliedAt       time.Time `gorm:"column:applied_at;not null;default:now()"`
}

func (SchemaVersionModel) TableName() string { return "schema_versions" }

// SchemaLedgerRepository reads and appends schema_versions.
type SchemaLedgerRepository struct {
	db *gorm.DB
}

func NewSchemaLedgerRepository(db *gorm.DB) *SchemaLedgerRepository {
	if db == nil {
		panic("NewSchemaLedgerRepository: nil db")
	}
	return &SchemaLedgerRepository{db: db}
}

// Setup creates the ledger table. It is called right AFTER the boot's
// decision and before any other Setup(): on a database the boot refused the
// table must not appear (the refusal comes first), and the intent row has to
// exist before the first product migration, so that a build whose migrations
// fail part-way still leaves a row an older build is refused on.
func (r *SchemaLedgerRepository) Setup() error {
	return r.db.AutoMigrate(&SchemaVersionModel{})
}

// Read returns the highest recorded schema version and whether a migration
// to it finished. It issues SELECTs and nothing else — it runs before any
// migration, on a database this build may be about to refuse.
//
// The table's existence is asked first, so the first boot of a new install
// does not log a failed statement against a relation that cannot exist yet.
// The probe is to_regclass rather than Migrator().HasTable: HasTable swallows
// its own error and answers false, and an unreadable ledger read as "no
// ledger" is the fail-open this read must never have. to_regclass resolves
// the name through search_path exactly as the ledger query does.
func (r *SchemaLedgerRepository) Read(ctx context.Context) (schema.Ledger, error) {
	var exists bool
	if err := r.db.WithContext(ctx).
		Raw(`SELECT to_regclass(?) IS NOT NULL`, SchemaVersionModel{}.TableName()).
		Scan(&exists).Error; err != nil {
		return schema.Ledger{}, fmt.Errorf("probing for the schema-version ledger: %w", err)
	}
	if !exists {
		return schema.Ledger{Present: false}, nil
	}
	var row struct {
		Highest  int
		Complete bool
	}
	err := r.db.WithContext(ctx).
		Raw(`SELECT COALESCE(MAX(schema_version), 0) AS highest,
			COALESCE(bool_or(migrated) FILTER (WHERE schema_version = (SELECT MAX(schema_version) FROM schema_versions)), false) AS complete
			FROM schema_versions`).
		Scan(&row).Error
	if err != nil {
		// The table can still vanish between the probe and the read.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUndefinedTable {
			return schema.Ledger{Present: false}, nil
		}
		return schema.Ledger{}, fmt.Errorf("reading the schema-version ledger: %w", err)
	}
	return schema.Ledger{Present: true, Highest: row.Highest, Complete: row.Complete}, nil
}

// MarkMigrated sets migrated=true on the (version, revision) row: the
// build's migrations finished. A row that is missing is an error — the
// intent row is written before the migrations, so its absence means the
// ledger was changed under the boot.
func (r *SchemaLedgerRepository) MarkMigrated(ctx context.Context, version int, revision string) error {
	if revision == "" {
		revision = schema.UnknownRevision
	}
	res := r.db.WithContext(ctx).Exec(
		`UPDATE schema_versions SET migrated = true WHERE schema_version = ? AND build_revision = ?`,
		version, revision)
	if res.Error != nil {
		return fmt.Errorf("marking schema version %d migrated: %w", version, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("marking schema version %d migrated: no ledger row for revision %s", version, revision)
	}
	return nil
}

// Record appends e unless the same (version, revision) pair is already there.
func (r *SchemaLedgerRepository) Record(ctx context.Context, e schema.Entry) (bool, error) {
	m := SchemaVersionModel{
		SchemaVersion:   e.SchemaVersion,
		BuildRevision:   e.BuildRevision,
		PreviousVersion: e.PreviousVersion,
		Migrated:        e.Migrated,
		AppliedAt:       time.Now().UTC(),
	}
	res := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "schema_version"}, {Name: "build_revision"}},
			DoNothing: true,
		}).
		Create(&m)
	if res.Error != nil {
		return false, fmt.Errorf("recording schema version %d: %w", e.SchemaVersion, res.Error)
	}
	return res.RowsAffected > 0, nil
}
