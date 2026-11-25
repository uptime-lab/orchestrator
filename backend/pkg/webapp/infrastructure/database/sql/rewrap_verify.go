package sql

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// KeyCoverage is how many encrypted values in one place are already readable by
// the PRIMARY key alone, and how many still need a fallback (old) key.
type KeyCoverage struct {
	Target     string
	OnPrimary  int
	OnOldKey   int
	Unreadable int
	// Truncated means the scan stopped before the end of the target, so this
	// report answers "is the configured key right" but NOT "is the rotation
	// finished". KeyCoverageIsClean treats it as not-clean: claiming the old key
	// can be dropped after looking at part of the data is the one mistake this
	// whole file exists to prevent.
	Truncated bool
}

// CoverageScope bounds how much of each target is examined.
type CoverageScope int

const (
	// CoverageFull examines every encrypted value. Required to answer "can the
	// old key be dropped", because a sample cannot: the row that still needs the
	// old key is exactly the one a sample missed.
	CoverageFull CoverageScope = iota

	// CoverageSample examines a bounded number of values per target. Enough for
	// the wrong-key canary — with the wrong key EVERY value fails, so any
	// non-empty sample proves it — and it keeps an unbounded table off the boot
	// path. node_commands has no retention by default, so a full trial decryption of
	// it on every start is a sweep that grows forever, which is the shape
	// this check exists to avoid.
	CoverageSample
)

// coverageSampleSize is how many values per target CoverageSample looks at.
// Large enough that the small tables are covered completely anyway, small enough
// to be free on the large one.
const coverageSampleSize = 500

// VerifyEncryptionKeyCoverage reports, per encrypted target, how many values are
// still readable only by an old key.
//
// It exists because rotation had no hard signal: the `v1:` prefix marks the
// ciphertext FORMAT, not which key wrote it, so "did the rewrap actually finish"
// could only be answered by trial decryption. Dropping SECRETS_ENCRYPTION_KEY_OLD
// while rows still depend on it makes those rows permanently unreadable, and the
// first symptom is a user opening a secret weeks later.
//
// The targets come from encryptedTargets(), the same list the rewrap walks, so
// the two cannot cover different sets.
//
// Read-only: it decrypts in memory and writes nothing.
func VerifyEncryptionKeyCoverage(db *gorm.DB, enc rewrapEncryptor, scope CoverageScope) []KeyCoverage {
	targets := encryptedTargets()
	report := make([]KeyCoverage, 0, len(targets))
	for _, target := range targets {
		coverage := target.Coverage(db, enc, scope)
		coverage.Target = target.Label
		report = append(report, coverage)
	}
	return report
}

// classify counts one ciphertext against the keys that can read it.
func classify(enc rewrapEncryptor, value string, into *KeyCoverage) {
	switch {
	case enc.EncryptedWithPrimary(value):
		into.OnPrimary++
	default:
		if _, err := enc.Decrypt(value); err == nil {
			into.OnOldKey++
		} else {
			into.Unreadable++
		}
	}
}

// stringColumnCoverage classifies every ciphertext in a whole-value column.
//
// Keyset-batched in the column's own page size, like tokenColumnCoverage, and
// for the same reason: a single flat Scan holds the whole column in memory at
// once, and node_files.content is a table of 512 KiB-capped file versions, so
// the CoverageFull scan the rotation runs — and even a 500-value sample on the
// boot path — is hundreds of megabytes inside a memory-limited container. Values are
// classified as they arrive and only counters are kept.
func stringColumnCoverage(db *gorm.DB, enc rewrapEncryptor, c encryptedStringColumn, scope CoverageScope) KeyCoverage {
	cov := KeyCoverage{Target: c.table + "." + c.col}

	type row struct {
		PK  uuid.UUID
		Val string
	}

	var last uuid.UUID
	scanned := 0
	for {
		var rows []row
		q := db.Table(c.table).
			Select(fmt.Sprintf("%s AS pk, %s AS val", c.pk, c.col)).
			Where(fmt.Sprintf("%s IS NOT NULL AND %s <> ''", c.col, c.col)).
			Where(fmt.Sprintf("%s > ?", c.pk), last).
			Order(fmt.Sprintf("%s ASC", c.pk)).
			Limit(c.batch)
		if c.extraWhere != "" {
			q = q.Where(c.extraWhere)
		}
		if err := q.Scan(&rows).Error; err != nil {
			cov.Unreadable = -1 // query failed; reported as unknown
			return cov
		}
		if len(rows) == 0 {
			return cov
		}
		for _, r := range rows {
			last = r.PK
			classify(enc, r.Val, &cov)
		}
		scanned += len(rows)
		if scope == CoverageSample && scanned >= coverageSampleSize {
			// Only a sample was examined; say so rather than letting the counters
			// read as a complete answer.
			cov.Truncated = true
			return cov
		}
		if len(rows) < c.batch {
			return cov
		}
	}
}

// tokenColumnCoverage classifies every encv1: token inside jsonb columns.
//
// Keyset-batched rather than one wide Scan: node_commands holds a payload for
// every command ever issued and has no retention, so reading the column for the
// whole table at once is a memory spike at boot on the largest table in the
// schema. Values are classified as they arrive and only counters are kept.
func tokenColumnCoverage(db *gorm.DB, enc rewrapEncryptor, table, pk string, cols []string, where string, scope CoverageScope) KeyCoverage {
	cov := KeyCoverage{Target: table + "." + strings.Join(cols, "/")}

	// The row type below holds three blob columns; guard rather than silently
	// ignore a fourth. The guard has already fired once: sealing
	// node_commands.details widened its target to three columns while this
	// held two, and the -1 it returns is what failed CI — a silent truncation
	// would instead have reported coverage over a column the rotation report
	// never read.
	if len(cols) == 0 || len(cols) > 3 {
		cov.Unreadable = -1
		return cov
	}

	selects := make([]string, 0, len(cols)+1)
	selects = append(selects, fmt.Sprintf("%s AS pk", pk))
	for i, col := range cols {
		// ::text so a jsonb column arrives as the raw document the token parser
		// expects, whatever the driver would otherwise do with it.
		selects = append(selects, fmt.Sprintf("COALESCE(%s::text, '') AS c%d", col, i))
	}

	type row struct {
		PK uuid.UUID
		C0 string
		C1 string
		C2 string
	}

	var last uuid.UUID
	scanned := 0
	for {
		var rows []row
		q := db.Table(table).
			Select(strings.Join(selects, ", ")).
			Where(where).
			Where(fmt.Sprintf("%s > ?", pk), last).
			Order(fmt.Sprintf("%s ASC", pk)).
			Limit(backfillBatchSize).
			Scan(&rows)
		if q.Error != nil {
			cov.Unreadable = -1
			return cov
		}
		if len(rows) == 0 {
			return cov
		}
		for _, r := range rows {
			last = r.PK
			for _, blob := range []string{r.C0, r.C1, r.C2} {
				for _, token := range extractCipherTokens(blob) {
					classify(enc, strings.TrimPrefix(token, commandCipherPrefix), &cov)
				}
			}
		}
		scanned += len(rows)
		if scope == CoverageSample && scanned >= coverageSampleSize {
			// Only a sample was examined; say so rather than letting the counters
			// read as a complete answer.
			cov.Truncated = true
			return cov
		}
		if len(rows) < backfillBatchSize {
			return cov
		}
	}
}

// FormatKeyCoverage renders the report as one log-friendly line.
func FormatKeyCoverage(report []KeyCoverage) string {
	parts := make([]string, 0, len(report))
	for _, c := range report {
		part := fmt.Sprintf("%s: primary=%d old_key=%d unreadable=%d",
			c.Target, c.OnPrimary, c.OnOldKey, c.Unreadable)
		if c.Truncated {
			part += " (sampled)"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

// KeyCoverageIsClean reports whether every value is readable by the primary key,
// i.e. whether the old key can be dropped. A truncated (sampled) report is never
// clean: it cannot answer that question.
func KeyCoverageIsClean(report []KeyCoverage) bool {
	for _, c := range report {
		if c.OnOldKey != 0 || c.Unreadable != 0 || c.Truncated {
			return false
		}
	}
	return true
}

// KeyCoverageHasUnreadable reports whether any value cannot be read by ANY
// configured key — the wrong-key signal. A sampled report answers that one
// perfectly well, since a wrong key fails every value it meets.
func KeyCoverageHasUnreadable(report []KeyCoverage) bool {
	for _, c := range report {
		if c.Unreadable > 0 {
			return true
		}
	}
	return false
}
