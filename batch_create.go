package gorm

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// DefaultCreateBatchSize is the default number of rows per multi-row INSERT
// when batchSize is not specified or <= 0.
//
// This value balances SQLite's bind-parameter limit (32766) and transaction
// lock duration. For a struct with ~20 columns, 500 * 20 = 10000 < 32766.
const DefaultCreateBatchSize = 500

const (
	sqliteMaxBatchBindVars   = 32766
	mysqlMaxBatchBindVars    = 65535
	postgresMaxBatchBindVars = 65535
	mssqlMaxBatchBindVars    = 2100
)

type batchCreateRow struct {
	value        interface{}
	scope        *Scope
	primaryField *Field
	columns      []string
	values       []interface{}
}

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
	if txn.Error != nil {
		return txn
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

// createBatch prepares rows once, then splits them by column shape and the
// target database's bind-variable limit. Rows with different default/primary
// key shapes cannot share one VALUES clause without changing Create semantics.
func createBatch(txn *DB, rv reflect.Value, start, end int) (int64, error) {
	rows := make([]*batchCreateRow, 0, end-start)
	now := txn.nowFunc()
	var expectedColumns []string
	for i := start; i < end; i++ {
		row, err := prepareBatchCreateRow(txn, rv.Index(i), now, expectedColumns)
		if err != nil {
			return 0, err
		}
		rows = append(rows, row)
		expectedColumns = row.columns
	}

	var totalRowsAffected int64
	for groupStart := 0; groupStart < len(rows); {
		maxRows := maxCreateBatchRows(rows[groupStart].scope.Dialect().GetName(), len(rows[groupStart].columns), len(rows)-groupStart)
		groupEnd := groupStart + 1
		for groupEnd < len(rows) && groupEnd-groupStart < maxRows && sameBatchColumns(rows[groupStart].columns, rows[groupEnd].columns) {
			groupEnd++
		}

		rowsAffected, err := executeBatchCreateRows(txn, rows[groupStart:groupEnd])
		if err != nil {
			return totalRowsAffected, err
		}
		totalRowsAffected += rowsAffected
		groupStart = groupEnd
	}
	return totalRowsAffected, nil
}

func prepareBatchCreateRow(txn *DB, elem reflect.Value, now time.Time, expectedColumns []string) (*batchCreateRow, error) {
	for elem.Kind() == reflect.Interface {
		if elem.IsNil() {
			return nil, fmt.Errorf("gorm: nil interface in CreateInBatches")
		}
		elem = elem.Elem()
	}

	var value interface{}
	if elem.Kind() == reflect.Ptr {
		if elem.IsNil() {
			return nil, fmt.Errorf("gorm: nil pointer in CreateInBatches")
		}
		value = elem.Interface()
	} else {
		if !elem.CanAddr() {
			return nil, ErrUnaddressable
		}
		value = elem.Addr().Interface()
	}

	scope := txn.NewScope(value)
	if !scope.HasError() {
		scope.CallMethod("BeforeSave")
	}
	if !scope.HasError() {
		scope.CallMethod("BeforeCreate")
	}
	if scope.HasError() {
		return nil, scope.db.Error
	}

	if field, ok := scope.FieldByName("CreatedAt"); ok && field.IsBlank {
		if err := field.Set(now); err != nil {
			return nil, err
		}
	}
	if field, ok := scope.FieldByName("UpdatedAt"); ok && field.IsBlank {
		if err := field.Set(now); err != nil {
			return nil, err
		}
	}

	fields := scope.Fields()
	valueCapacity := len(expectedColumns)
	if valueCapacity == 0 {
		valueCapacity = len(fields)
	}
	row := &batchCreateRow{
		value:  value,
		scope:  scope,
		values: make([]interface{}, 0, valueCapacity),
	}
	columnsMatch := expectedColumns != nil
	columnIndex := 0
	appendColumn := func(column string, value interface{}) {
		if columnsMatch && (columnIndex >= len(expectedColumns) || expectedColumns[columnIndex] != column) {
			row.columns = append(row.columns, expectedColumns[:columnIndex]...)
			columnsMatch = false
		}
		if !columnsMatch {
			row.columns = append(row.columns, column)
		}
		row.values = append(row.values, value)
		columnIndex++
	}

	for _, field := range fields {
		if field.IsPrimaryKey {
			row.primaryField = field
		}
		if !scope.changeableField(field) {
			continue
		}
		if field.IsNormal && !field.IsIgnored {
			if field.IsBlank && field.HasDefaultValue {
				continue
			}
			if !field.IsPrimaryKey || !field.IsBlank {
				appendColumn(scope.Quote(field.DBName), field.Field.Interface())
			}
			continue
		}
		if field.Relationship != nil && field.Relationship.Kind == "belongs_to" {
			for _, foreignKey := range field.Relationship.ForeignDBNames {
				if foreignField, ok := scope.FieldByName(foreignKey); ok && !scope.changeableField(foreignField) {
					appendColumn(scope.Quote(foreignField.DBName), foreignField.Field.Interface())
				}
			}
		}
	}
	if columnsMatch {
		if columnIndex == len(expectedColumns) {
			row.columns = expectedColumns
		} else {
			row.columns = append(row.columns, expectedColumns[:columnIndex]...)
		}
	}
	return row, nil
}

func executeBatchCreateRows(txn *DB, rows []*batchCreateRow) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	scope := txn.NewScope(rows[0].value)
	columns := rows[0].columns

	var extraOption string
	if value, ok := scope.Get("gorm:insert_option"); ok {
		extraOption = fmt.Sprint(value)
	}
	var insertModifier string
	if value, ok := scope.Get("gorm:insert_modifier"); ok {
		insertModifier = strings.ToUpper(fmt.Sprint(value))
		if insertModifier == "INTO" {
			insertModifier = ""
		}
	}

	if len(columns) == 0 {
		if len(rows) != 1 {
			return 0, fmt.Errorf("gorm: cannot batch multiple DEFAULT VALUES rows")
		}
		scope.Raw(fmt.Sprintf(
			"INSERT%v INTO %v %v%v",
			addExtraSpaceIfExist(insertModifier),
			scope.QuotedTableName(),
			scope.Dialect().DefaultValueStr(),
			addExtraSpaceIfExist(extraOption),
		))
	} else {
		var placeholders strings.Builder
		for rowIndex, row := range rows {
			if !sameBatchColumns(columns, row.columns) || len(row.values) != len(columns) {
				return 0, fmt.Errorf("gorm: inconsistent columns in CreateInBatches")
			}
			if rowIndex > 0 {
				placeholders.WriteByte(',')
			}
			placeholders.WriteByte('(')
			for valueIndex, value := range row.values {
				if valueIndex > 0 {
					placeholders.WriteByte(',')
				}
				placeholders.WriteString(scope.AddToVars(value))
			}
			placeholders.WriteByte(')')
		}
		scope.Raw(fmt.Sprintf(
			"INSERT%v INTO %v (%v) VALUES %v%v",
			addExtraSpaceIfExist(insertModifier),
			scope.QuotedTableName(),
			strings.Join(columns, ","),
			placeholders.String(),
			addExtraSpaceIfExist(extraOption),
		))
	}

	result, err := scope.SQLDB().Exec(scope.SQL, scope.SQLVars...)
	if err != nil {
		return 0, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		rowsAffected = 0
	}

	if err := assignBatchPrimaryKeys(scope.Dialect().GetName(), rows, rowsAffected, result.LastInsertId); err != nil {
		return rowsAffected, err
	}
	for _, row := range rows {
		if !row.scope.HasError() {
			row.scope.CallMethod("AfterCreate")
		}
		if !row.scope.HasError() {
			row.scope.CallMethod("AfterSave")
		}
		if row.scope.HasError() {
			return rowsAffected, row.scope.db.Error
		}
	}
	return rowsAffected, nil
}

func assignBatchPrimaryKeys(dialect string, rows []*batchCreateRow, rowsAffected int64, lastInsertID func() (int64, error)) error {
	if len(rows) == 0 || rowsAffected != int64(len(rows)) {
		return nil
	}
	primaryField := rows[0].primaryField
	if primaryField == nil || !primaryField.IsBlank {
		return nil
	}

	firstID, err := lastInsertID()
	if err != nil {
		return nil
	}
	switch dialect {
	case "sqlite3":
		firstID -= rowsAffected - 1
	case "mysql":
		// MySQL reports the first generated ID for a multi-row INSERT.
	default:
		return nil
	}
	if firstID <= 0 {
		return nil
	}

	for index, row := range rows {
		field := row.primaryField
		if field == nil || !field.IsBlank {
			continue
		}
		if err := field.Set(firstID + int64(index)); err != nil {
			return err
		}
	}
	return nil
}

func maxCreateBatchRows(dialect string, columnCount, requested int) int {
	if requested < 1 {
		requested = 1
	}
	if columnCount < 1 {
		return 1
	}

	maxBindVars := 0
	switch dialect {
	case "sqlite3":
		maxBindVars = sqliteMaxBatchBindVars
	case "mysql":
		maxBindVars = mysqlMaxBatchBindVars
	case "postgres":
		maxBindVars = postgresMaxBatchBindVars
	case "mssql":
		maxBindVars = mssqlMaxBatchBindVars
	}
	if maxBindVars == 0 {
		return requested
	}
	if bounded := maxBindVars / columnCount; bounded < requested {
		if bounded < 1 {
			return 1
		}
		return bounded
	}
	return requested
}

func sameBatchColumns(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
