package sql

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// pgUniqueViolation is Postgres' SQLSTATE for a unique constraint violation.
const pgUniqueViolation = "23505"

// isUniqueViolation reports whether an error is a duplicate-key rejection.
//
// It checks BOTH representations on purpose. GORM's `TranslateError` produces
// the tidy `gorm.ErrDuplicatedKey`, and that is what the repositories built on
// the model callbacks match on — but the translation happens inside those
// callbacks, so an error returned by a raw `Exec`/`Raw` statement arrives
// untranslated as the driver's own `*pgconn.PgError`. A retry loop written
// against the sentinel alone therefore never fires on the raw-SQL paths, which
// is where the statements complex enough to need one live.
func isUniqueViolation(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
