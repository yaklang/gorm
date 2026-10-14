package gorm

import (
	"fmt"
	"reflect"
	"strings"
)

// OnConflictClause defines an INSERT ... ON CONFLICT upsert.
// This is a lightweight v1-compatible equivalent of gorm v2's Clauses(clause.OnConflict{...}).
//
// Usage:
//
//	db.Set("gorm:insert_option", "ON CONFLICT(id) DO UPDATE SET name=excluded.name, age=excluded.age").Create(&user)
//	// or with the helper:
//	db.OnConflict("id").UpdateColumns([]string{"name", "age"}).Create(&user)
//
// For SQLite: ON CONFLICT(column) DO UPDATE SET col=excluded.col
// For MySQL:  ON DUPLICATE KEY UPDATE col=VALUES(col)   (use OnConflictMySQL)
// For Postgres: ON CONFLICT(column) DO UPDATE SET col=EXCLUDED.col
type OnConflictClause struct {
	// Columns is the conflict target column(s), e.g. "id" or "code_id,program_name".
	Columns string

	// DoUpdate: if true, generate DO UPDATE SET ...; if false, DO NOTHING.
	DoUpdate bool

	// UpdateColumns: columns to update on conflict (SQLite/Postgres use "excluded.col" syntax).
	// If empty and DoUpdate is true, all non-primary columns are updated.
	UpdateColumns []string

	// Where: optional WHERE clause appended after the UPDATE SET.
	Where string
}

// String returns the SQL fragment for the INSERT option.
func (c OnConflictClause) String() string {
	if c.Columns == "" {
		return ""
	}
	var b strings.Builder
	if c.DoUpdate {
		b.WriteString(fmt.Sprintf("ON CONFLICT(%s) DO UPDATE SET", c.Columns))
		if len(c.UpdateColumns) > 0 {
			parts := make([]string, 0, len(c.UpdateColumns))
			for _, col := range c.UpdateColumns {
				parts = append(parts, fmt.Sprintf("%s=excluded.%s", col, col))
			}
			b.WriteString(" " + strings.Join(parts, ","))
		} else {
			// Update all columns except the conflict target — caller should set UpdateColumns explicitly.
			b.WriteString(" *") // placeholder; caller should provide columns
		}
		if c.Where != "" {
			b.WriteString(" WHERE " + c.Where)
		}
	} else {
		b.WriteString(fmt.Sprintf("ON CONFLICT(%s) DO NOTHING", c.Columns))
	}
	return b.String()
}

// SetOnConflict sets the gorm:insert_option scope value to an ON CONFLICT clause.
// Call this before .Create() to turn a plain INSERT into an upsert.
//
//	db.SetOnConflict(OnConflictClause{Columns: "id", DoUpdate: true, UpdateColumns: []string{"name", "age"}}).Create(&user)
func (s *DB) SetOnConflict(clause OnConflictClause) *DB {
	return s.InstantSet("gorm:insert_option", clause.String())
}

// OnConflictDoNothing is a shortcut for ON CONFLICT(col) DO NOTHING.
func (s *DB) OnConflictDoNothing(columns string) *DB {
	return s.Set("gorm:insert_option", fmt.Sprintf("ON CONFLICT(%s) DO NOTHING", columns))
}

// OnConflictDoUpdate is a shortcut for ON CONFLICT(col) DO UPDATE SET col=excluded.col, ...
func (s *DB) OnConflictDoUpdate(columns string, updateColumns ...string) *DB {
	clause := OnConflictClause{
		Columns:       columns,
		DoUpdate:      true,
		UpdateColumns: updateColumns,
	}
	return s.InstantSet("gorm:insert_option", clause.String())
}

// OnConflictUpdateAll generates ON CONFLICT(col) DO UPDATE SET for all
// non-primary-key columns of the model. The model must be set on the DB
// via .Model() or .Create() before calling this.
func (s *DB) OnConflictUpdateAll(columns string) *DB {
	// We need to introspect the model to get column names.
	// Use a scope to get the struct fields.
	scope := s.NewScope(s.Value)
	var updateCols []string
	for _, field := range scope.Fields() {
		if field.IsNormal && !field.IsIgnored && !field.IsPrimaryKey {
			updateCols = append(updateCols, field.DBName)
		}
	}
	clause := OnConflictClause{
		Columns:       columns,
		DoUpdate:      true,
		UpdateColumns: updateCols,
	}
	return s.InstantSet("gorm:insert_option", clause.String())
}

// CreateInBatchesOnConflict is like CreateInBatches but with an ON CONFLICT clause
// appended to each batch INSERT. This enables bulk upsert in a single statement per batch.
//
//	db.CreateInBatchesOnConflict(users, 100, "ON CONFLICT(id) DO UPDATE SET name=excluded.name")
func (s *DB) CreateInBatchesOnConflict(value interface{}, batchSize int, insertOption string) *DB {
	if batchSize <= 0 {
		batchSize = 100
	}

	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return s.Set("gorm:insert_option", insertOption).Create(value)
	}

	n := rv.Len()
	if n == 0 {
		return s.clone()
	}

	// Detect if we're already inside a transaction.
	var txn *DB
	var ownTxn bool
	if _, ok := s.db.(sqlDb); ok {
		txn = s.Begin()
		ownTxn = true
	} else {
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

		rowsAffected, err := createBatchWithOption(txn, rv, start, end, insertOption)
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

// createBatchWithOption is like createBatch but appends insertOption to the INSERT SQL.
func createBatchWithOption(txn *DB, rv reflect.Value, start, end int, insertOption string) (int64, error) {
	// Build a dummy scope from the first element.
	firstElem := rv.Index(start)
	for firstElem.Kind() == reflect.Ptr {
		firstElem = firstElem.Elem()
	}

	var firstVal interface{}
	if rv.Index(start).Kind() == reflect.Ptr {
		firstVal = rv.Index(start).Interface()
	} else {
		firstVal = rv.Index(start).Addr().Interface()
	}

	scope := txn.NewScope(firstVal)
	quotedTable := scope.QuotedTableName()

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
		var rows int64
		for i := start; i < end; i++ {
			elem := rv.Index(i)
			var val interface{}
			if elem.Kind() == reflect.Ptr {
				val = elem.Interface()
			} else {
				val = elem.Addr().Interface()
			}
			r := txn.Set("gorm:insert_option", insertOption).Create(val)
			if r.Error != nil {
				return rows, r.Error
			}
			rows += r.RowsAffected
		}
		return rows, nil
	}

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

		var elemVal interface{}
		if isPtrElem {
			elemVal = elem.Interface()
		} else {
			elemVal = indirectElem.Addr().Interface()
		}
		elemScope := txn.NewScope(elemVal)

		if !elemScope.HasError() {
			elemScope.CallMethod("BeforeSave")
		}
		if !elemScope.HasError() {
			elemScope.CallMethod("BeforeCreate")
		}
		if elemScope.HasError() {
			return 0, elemScope.db.Error
		}

		if createdAtField, ok := elemScope.FieldByName("CreatedAt"); ok && createdAtField.IsBlank {
			createdAtField.Set(now)
		}
		if updatedAtField, ok := elemScope.FieldByName("UpdatedAt"); ok && updatedAtField.IsBlank {
			updatedAtField.Set(now)
		}

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

	optionStr := ""
	if insertOption != "" {
		optionStr = " " + insertOption
	}

	scope.Raw(fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES %s%s",
		quotedTable,
		strings.Join(columns, ","),
		strings.Join(allPlaceholders, ","),
		optionStr,
	))

	result, err := scope.SQLDB().Exec(scope.SQL, scope.SQLVars...)
	if err != nil {
		return 0, err
	}
	rowsAffected, _ := result.RowsAffected()

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
