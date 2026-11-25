package schema

// UnknownRevision is recorded when the build carries no revision (a build
// from a copied tree with no -ldflags stamp and no VCS stamp).
const UnknownRevision = "unknown"

// Entry is one ledger row: a build that STARTED migrating this database. It
// is written before the first migration with Migrated=false and marked
// migrated once every migration succeeded. The ledger holds one row per
// distinct (version, revision) pair, never one per boot.
type Entry struct {
	// SchemaVersion is the build's Version.
	SchemaVersion int
	// BuildRevision is the commit the build came from, or UnknownRevision.
	BuildRevision string
	// PreviousVersion is the highest version the ledger held before this
	// boot: 0 for a fresh or pre-ledger database.
	PreviousVersion int
	// Migrated is true once this build's migrations FINISHED. The intent row
	// is written false; the boot sets it after the last Setup() succeeded.
	// (Earlier builds wrote it as "this boot moved the version"; their rows
	// read as finished either way, every one having followed a successful
	// migration.)
	Migrated bool
}

// NewEntry builds the intent row this boot records BEFORE its migrations.
func NewEntry(before Ledger, build int, revision string) Entry {
	if revision == "" {
		revision = UnknownRevision
	}
	prev := 0
	if before.Present {
		prev = before.Highest
	}
	return Entry{
		SchemaVersion:   build,
		BuildRevision:   revision,
		PreviousVersion: prev,
		Migrated:        false,
	}
}
