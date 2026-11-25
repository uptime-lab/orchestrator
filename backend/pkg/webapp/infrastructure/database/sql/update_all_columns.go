package sql

import "gorm.io/gorm"

// updateAllColumns writes every column of model onto the row q matches, and can
// only ever UPDATE.
//
// It exists because gorm's Save is not an update. When its UPDATE matches no
// row, Save silently re-runs the write as an `INSERT ... ON CONFLICT DO UPDATE`
// (gorm.DB.Save, v1.31), which has two consequences this package cannot afford.
// A row deleted underneath an in-flight request is RE-CREATED from the caller's
// stale in-memory copy — a purged account, a swept execution, a retired command
// coming back with whatever columns that copy happened to hold. And because the
// insert reports RowsAffected 1, every `RowsAffected == 0 -> not found` check
// downstream became unreachable, so the resurrection was reported to the caller
// as a successful update.
//
// Select("*") is exactly what Save prepends before its update callback, so the
// statement produced here is the same UPDATE — all columns, zero values
// included, honouring any Omit — minus the insert fallback. A model with a zero
// primary key now fails with gorm.ErrMissingWhereClause instead of inserting.
func updateAllColumns(q *gorm.DB, model interface{}) *gorm.DB {
	return q.Select("*").Updates(model)
}
