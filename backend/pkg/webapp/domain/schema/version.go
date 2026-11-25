// Package schema holds the version of the database schema this build migrates
// to, and the boot-time decision that compares it with the schema-version
// ledger (the schema_versions table) before any migration runs.
//
// Why it exists: migrations are GORM AutoMigrate, which only ever ADDS tables
// and columns. An older image started against a schema a newer one migrated
// does not fail at boot; it runs, writes rows that miss the newer columns'
// invariants, and fails later and somewhere else. The ledger turns that into a
// refused boot with the database untouched, which is what lets a release
// promise an upgrade path: forward migrates, backward is refused, and the way
// back is restoring the backup taken before the upgrade.
//
// The refusal protects only from builds that carry it: an image older than
// the ledger itself does not read it.
package schema

import "fmt"

// Version is the schema version this build migrates to. ONE place, bumped by
// hand: increase it by one in the same change that adds or changes a table, a
// column, an index or a Setup() data migration.
//
// Forgetting the bump is made visible by
// sql.TestSchemaVersionIsBumpedWithTheModels, which hashes the GORM models,
// every Setup()/Ensure*() body, every helper reachable from them and every
// function carrying migration SQL, and compares the hash with
// SourceFingerprint below: a change to any of them fails the test until both
// constants are updated together.
// The ledger also records the build revision on every row, so a row stays
// useful even for a build where the integer was not bumped.
const Version = 4

// SourceFingerprint is the hash of the schema-defining source at Version. It
// is not read at runtime; it exists only so that the test above can tell a
// schema change from an unrelated edit. Update it together with Version — or,
// when the edit really changed nothing in the database (a renamed Go field
// with the same column tag), alone, and say so in the commit.
const SourceFingerprint = "9c6108727e27b3aa7427d78b40c5fcfc"

// Decision is what the boot does after reading the ledger.
type Decision int

const (
	// Migrate: the ledger is absent (a fresh database, or one created before
	// the ledger existed), records an older version, or records this version
	// without a finished migration (a boot of this build whose migrations
	// failed part-way). Run the migrations — AutoMigrate is idempotent — and
	// then mark this build's row migrated.
	Migrate Decision = iota
	// Current: the ledger already records this version. The migrations still
	// run — AutoMigrate is idempotent and is how a same-version build repairs
	// a missing index — and a row is added only for a build revision the
	// ledger has not seen at this version.
	Current
	// Refuse: the ledger records a NEWER version than this build knows. Exit
	// before any migration.
	Refuse
)

func (d Decision) String() string {
	switch d {
	case Migrate:
		return "migrate"
	case Current:
		return "current"
	case Refuse:
		return "refuse"
	default:
		return fmt.Sprintf("decision(%d)", int(d))
	}
}

// Ledger is what the boot read from the schema_versions table.
type Ledger struct {
	// Present is false when the table does not exist. Only a missing table is
	// tolerated; any other read failure is an error, never an absent ledger.
	Present bool
	// Highest is the highest schema version recorded, 0 for an empty table.
	Highest int
	// Complete reports that a row at Highest records a FINISHED migration
	// (migrated=true). The boot writes its row BEFORE the migrations, with
	// migrated=false, and marks it after the last Setup() succeeded — so a
	// newer build whose migrations failed part-way still leaves a row that
	// refuses an older build, and the same
	// build seeing its own unfinished row migrates again.
	Complete bool
}

// Decide compares the ledger with the build's schema version. Highest above
// the build is refused whether or not that migration finished: a part-applied
// newer schema is still not one an older build knows.
func Decide(l Ledger, build int) Decision {
	switch {
	case !l.Present || l.Highest < build:
		return Migrate
	case l.Highest == build && !l.Complete:
		return Migrate
	case l.Highest == build:
		return Current
	default:
		return Refuse
	}
}

// NewerSchemaError is the refusal: the database was migrated by a newer build.
type NewerSchemaError struct {
	Stored int
	Build  int
}

func (e *NewerSchemaError) Error() string {
	return fmt.Sprintf("database schema is version %d but this build knows only version %d: "+
		"it was migrated by a newer release. The database was NOT touched. "+
		"Start the newer release again, or restore the database backup taken before "+
		"the upgrade and then start this one", e.Stored, e.Build)
}

// Check returns a *NewerSchemaError when the ledger forbids this build to run,
// and the decision otherwise.
func Check(l Ledger, build int) (Decision, error) {
	d := Decide(l, build)
	if d == Refuse {
		return d, &NewerSchemaError{Stored: l.Highest, Build: build}
	}
	return d, nil
}
