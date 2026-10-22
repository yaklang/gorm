package gorm

// BatchCreateDialect optionally bounds the number of rows in a single batch
// statement. A non-positive limit uses GORM's ordinary bind-variable limits.
// A limit of one also opts into reading each INSERT's own LastInsertId; no
// consecutive-ID assumption is made. SaveInBatches never writes IDs back.
//
// This is separate from Dialect so existing dialect implementations continue to
// work without changes.
type BatchCreateDialect interface {
	BatchCreateMaxRows() int
}

// BatchSaveDialect optionally builds a complete upsert statement for dialects
// that cannot express an upsert as an INSERT suffix. The table and every column
// name are already quoted. values contains one placeholder/expression list per
// row; actual bound arguments stay on the GORM scope and must not be inlined.
// conflictColumns identify the row; updateColumns exclude those columns, all
// primary keys, and created_at. A dialect must preserve existing physical IDs
// and creation timestamps when updating a conflict.
//
// Implementations may also implement BatchCreateDialect to bound source rows,
// for example to use one MERGE per row inside the caller's batch transaction.
type BatchSaveDialect interface {
	BatchSaveSQL(table string, columns, conflictColumns, updateColumns []string, values [][]string) (string, error)
}
