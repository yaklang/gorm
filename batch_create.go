package gorm

import (
	"fmt"
	"reflect"
	"strings"
)

// DefaultCreateBatchSize is the default number of rows per multi-row INSERT
// when batchSize is not specified or <= 0.
//
// This value balances SQLite's bind-parameter limit (32766) and transaction
// lock duration. For a struct with ~20 columns, 500 * 20 = 10000 < 32766.
const DefaultCreateBatchSize = 500

// CreateInBatches inserts a slice of records in batches.
// Each batch is a single multi-row INSERT statement, reducing round-trips
// from N to ceil(N/batchSize).
//
// BeforeSave/BeforeCreate/AfterCreate/AfterSave hooks are called for every
// element individually, just like v1's Create.
//
// If batchSize <= 0, DefaultCreateBatchSize (500) is used.
//
//	db.CreateInBatches(&users)        // uses DefaultCreateBatchSize
//	db.CreateInBatches(&users, 100)   // explicit batch size
func (s *DB) CreateInBatches(value interface{}, batchSize ...int) *DB {
	size := DefaultCreateBatchSize
	if len(batchSize) > 0 && batchSize[0] > 0 {
		size = batchSize[0]
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

	for start := 0; start < n; start += size {
		end := start + size
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
			return 0, elemScope.db.Error
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
		return 0, err
	}
	rowsAffected, _ := result.RowsAffected()

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
			elemVal = elem.Addr().Interface()
		}
		if !isPtrElem && !rv.Index(i).CanAddr() {
			tmp := reflect.New(rv.Type().Elem())
			tmp.Elem().Set(rv.Index(i))
			elemVal = tmp.Interface()
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
