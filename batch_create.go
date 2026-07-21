package gorm

import (
	"fmt"
	"reflect"
	"strings"
)

// CreateInBatches inserts a slice of records in batches of the given size.
// Each batch is a single multi-row INSERT statement, reducing round-trips
// from N to ceil(N/batchSize).
//
// BeforeSave/BeforeCreate/AfterCreate/AfterSave hooks are called for every
// element individually, just like v1's Create.
//
//	db.CreateInBatches(&users, 100)
//	db.CreateInBatches(users, 100)   // slice of structs or pointers
func (s *DB) CreateInBatches(value interface{}, batchSize int) *DB {
	// Already a *DB, so chain and return.
	if batchSize <= 0 {
		batchSize = 100
	}

	// Resolve the slice.
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		// Not a slice — fall back to single Create.
		return s.Create(value)
	}

	n := rv.Len()
	if n == 0 {
		return s.clone()
	}

	// Determine element type for creating per-element scopes.
	elemType := rv.Type().Elem()
	isPtr := elemType.Kind() == reflect.Ptr
	structType := elemType
	if isPtr {
		structType = elemType.Elem()
	}
	_ = structType

	// Detect if we're already inside a transaction (v1 Begin() fails on a
	// sql.Tx because it's not a sqlDb). If so, use the current DB directly
	// without nesting Begin/Commit. Otherwise, wrap in a transaction.
	var txn *DB
	var ownTxn bool
	if _, ok := s.db.(sqlDb); ok {
		txn = s.Begin()
		ownTxn = true
	} else {
		// Already inside a transaction — use the clone directly.
		txn = s.clone()
		ownTxn = false
	}

	defer func() {
		if r := recover(); r != nil {
			if ownTxn {
				txn.Rollback()
			}
			panic(r)
		}
	}()

	totalRowsAffected := int64(0)

	for start := 0; start < n; start += batchSize {
		end := start + batchSize
		if end > n {
			end = n
		}

		rowsAffected, err := createBatch(txn, rv, start, end)
		if err != nil {
			txn.AddError(err)
			if ownTxn {
				txn.Rollback()
			}
			return txn
		}
		totalRowsAffected += rowsAffected
	}

	if ownTxn {
		txn.Commit()
	}
	txn.RowsAffected = totalRowsAffected
	return txn
}

// createBatch executes a single multi-row INSERT for rv[start:end].
// It also calls BeforeSave/BeforeCreate/AfterCreate/AfterSave hooks for
// each element. Returns rows affected and any error.
func createBatch(txn *DB, rv reflect.Value, start, end int) (int64, error) {
	// Build a dummy scope from the first element to get model struct + columns.
	firstElem := rv.Index(start)
	for firstElem.Kind() == reflect.Ptr {
		firstElem = firstElem.Elem()
	}

	// We need a scope for the struct type. Use the first element.
	var firstVal interface{}
	if rv.Index(start).Kind() == reflect.Ptr {
		firstVal = rv.Index(start).Interface()
	} else {
		firstVal = rv.Index(start).Addr().Interface()
	}

	scope := txn.NewScope(firstVal)
	quotedTable := scope.QuotedTableName()

	// Collect column names once (same for all rows in the batch, assuming
	// homogeneous struct — which is guaranteed for a typed slice).
	var columns []string
	for _, field := range scope.Fields() {
		if scope.changeableField(field) {
			if field.IsNormal && !field.IsIgnored {
				if !field.IsPrimaryKey || !field.IsBlank {
					columns = append(columns, scope.Quote(field.DBName))
				}
			} else if field.Relationship != nil && field.Relationship.Kind == "belongs_to" {
				for _, foreignKey := range field.Relationship.ForeignDBNames {
					if foreignField, ok := scope.FieldByName(foreignKey); ok && !scope.changeableField(foreignField) {
						columns = append(columns, scope.Quote(foreignField.DBName))
					}
				}
			}
		}
	}

	if len(columns) == 0 {
		// Fallback: no columns detected, use single-row Create per element.
		var rows int64
		for i := start; i < end; i++ {
			elem := rv.Index(i)
			var val interface{}
			if elem.Kind() == reflect.Ptr {
				val = elem.Interface()
			} else {
				val = elem.Addr().Interface()
			}
			r := txn.Create(val)
			if r.Error != nil {
				return rows, r.Error
			}
			rows += r.RowsAffected
		}
		return rows, nil
	}

	// Build multi-row INSERT.
	// We use a fresh scope per row to collect vars (AddToVars appends to
	// scope.SQLVars and returns a placeholder).
	var allPlaceholders []string

	// We reuse the first scope for the INSERT SQL, but need to reset
	// SQLVars per row. Instead, build manually.
	// For each element: run hooks, collect field values, build placeholder row.
	var rowsAffected int64

	// Determine the dialect's placeholder style.
	// v1 gorm uses ? for most dialects (SQLite, MySQL) and $1,$2 for Postgres.
	// The AddToVars method handles this. But AddToVars mutates scope.SQLVars.
	// We'll build our own placeholder + vars to avoid scope state issues.

	dialect := scope.Dialect()
	_ = dialect

	// For hooks: we need per-element scopes to call BeforeSave/BeforeCreate.
	// For timestamps: set CreatedAt/UpdatedAt.
	now := NowFunc()

	for i := start; i < end; i++ {
		elem := rv.Index(i)
		isPtrElem := elem.Kind() == reflect.Ptr
		indirectElem := elem
		if isPtrElem {
			if elem.IsNil() {
				continue
			}
			indirectElem = elem.Elem()
		}

		// Create a scope for this element to run hooks and collect field values.
		var elemVal interface{}
		if isPtrElem {
			elemVal = elem.Interface()
		} else {
			elemVal = indirectElem.Addr().Interface()
		}
		elemScope := txn.NewScope(elemVal)

		// BeforeSave / BeforeCreate hooks.
		if !elemScope.HasError() {
			elemScope.CallMethod("BeforeSave")
		}
		if !elemScope.HasError() {
			elemScope.CallMethod("BeforeCreate")
		}
		if elemScope.HasError() {
			return rowsAffected, elemScope.db.Error
		}

		// Set timestamps.
		if createdAtField, ok := elemScope.FieldByName("CreatedAt"); ok && createdAtField.IsBlank {
			createdAtField.Set(now)
		}
		if updatedAtField, ok := elemScope.FieldByName("UpdatedAt"); ok && updatedAtField.IsBlank {
			updatedAtField.Set(now)
		}

		// Collect values for this row matching the columns order.
		var rowPlaceholders []string
		for _, field := range elemScope.Fields() {
			if elemScope.changeableField(field) {
				if field.IsNormal && !field.IsIgnored {
					if !field.IsPrimaryKey || !field.IsBlank {
						rowPlaceholders = append(rowPlaceholders, scope.AddToVars(field.Field.Interface()))
					}
				} else if field.Relationship != nil && field.Relationship.Kind == "belongs_to" {
					for _, foreignKey := range field.Relationship.ForeignDBNames {
						if foreignField, ok := elemScope.FieldByName(foreignKey); ok && !elemScope.changeableField(foreignField) {
							rowPlaceholders = append(rowPlaceholders, scope.AddToVars(foreignField.Field.Interface()))
						}
					}
				}
			}
		}
		allPlaceholders = append(allPlaceholders, "("+strings.Join(rowPlaceholders, ",")+")")
		_ = rowPlaceholders
	}

	// Build and execute the multi-row INSERT.
	// Use scope.Raw() to replace $$$ placeholders with ? (v1 dialect convention).
	scope.Raw(fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES %s",
		quotedTable,
		strings.Join(columns, ","),
		strings.Join(allPlaceholders, ","),
	))

	result, err := scope.SQLDB().Exec(scope.SQL, scope.SQLVars...)
	if err != nil {
		return rowsAffected, err
	}
	rowsAffected, _ = result.RowsAffected()

	// Set auto-increment primary key for each element (best effort — only
	// works reliably for SQLite/MySQL with LastInsertId). For batch inserts,
	// LastInsertId returns the first row's ID; subsequent IDs are consecutive.
	primaryField := scope.PrimaryField()
	if primaryField != nil && primaryField.IsBlank {
		if lastID, err := result.LastInsertId(); err == nil {
			// Set IDs on each element in the batch.
			for i := start; i < end; i++ {
				elem := rv.Index(i)
				if elem.Kind() == reflect.Ptr {
					if elem.IsNil() {
						continue
					}
					elem = elem.Elem()
				}
				// Find the primary key field on this element.
				pf := elem.FieldByName(primaryField.Name)
				if pf.IsValid() && pf.CanSet() && isBlank(pf) {
					pf.Set(reflect.ValueOf(lastID).Convert(pf.Type()))
					lastID++
				}
			}
		}
	}

	// AfterCreate / AfterSave hooks.
	for i := start; i < end; i++ {
		elem := rv.Index(i)
		isPtrElem := elem.Kind() == reflect.Ptr
		if isPtrElem && elem.IsNil() {
			continue
		}
		var elemVal interface{}
		if isPtrElem {
			elemVal = elem.Interface()
		} else {
			elemVal = elem.Addr().Interface() // won't work if not addressable; fallback below
		}
		// If not addressable, create a new pointer.
		if !isPtrElem && !rv.Index(i).CanAddr() {
			// Copy to addressable.
			tmp := reflect.New(rv.Type().Elem())
			tmp.Elem().Set(rv.Index(i))
			elemVal = tmp.Interface()
			// Copy back.
			rv.Index(i).Set(tmp.Elem())
		}
		hookScope := txn.NewScope(elemVal)
		if !hookScope.HasError() {
			hookScope.CallMethod("AfterCreate")
		}
		if !hookScope.HasError() {
			hookScope.CallMethod("AfterSave")
		}
		if hookScope.HasError() {
			return rowsAffected, hookScope.db.Error
		}
	}

	return rowsAffected, nil
}
