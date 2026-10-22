// Package dameng supplies an optional DM8 dialect and native driver bridge for GORM v1.
// Importing this package registers the dialect; -tags dameng enables its protocol driver.
package dameng

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/yaklang/gorm"
)

const Dialect = "dm"

var (
	_ gorm.Dialect            = (*dmDialect)(nil)
	_ gorm.BatchCreateDialect = (*dmDialect)(nil)
	_ gorm.BatchSaveDialect   = (*dmDialect)(nil)
)

func init() { gorm.RegisterDialect(Dialect, &dmDialect{}) }

type dmDialect struct {
	db gorm.SQLCommon
	gorm.DefaultForeignKeyNamer
}

func (*dmDialect) GetName() string           { return Dialect }
func (d *dmDialect) SetDB(db gorm.SQLCommon) { d.db = db }
func (*dmDialect) BindVar(int) string        { return "?" }
func (*dmDialect) Quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (d *dmDialect) DataTypeOf(field *gorm.StructField) string {
	value, typ, size, extra := gorm.ParseFieldStructForDialect(field, d)
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "text", "longtext", "mediumtext", "json", "jsonb":
		typ = "CLOB"
	case "blob", "bytea":
		typ = "BLOB"
	case "bool", "boolean":
		typ = "BIT"
	}
	if typ == "" {
		switch value.Kind() {
		case reflect.Bool:
			typ = "BIT"
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			typ = "BIGINT"
			increment, explicit := field.TagSettingsGet("AUTO_INCREMENT")
			if (field.IsPrimaryKey && !explicit) || (explicit && !strings.EqualFold(increment, "false")) {
				typ += " IDENTITY(1,1)"
			}
		case reflect.Float32, reflect.Float64:
			typ = "DOUBLE"
		case reflect.String:
			if size <= 0 {
				size = 255
			}
			typ = fmt.Sprintf("VARCHAR(%d)", size)
		case reflect.Struct:
			if value.Type() == reflect.TypeOf(time.Time{}) {
				typ = "TIMESTAMP(6)"
			}
		case reflect.Slice, reflect.Array:
			if value.Type().Elem().Kind() == reflect.Uint8 {
				typ = "BLOB"
			}
		}
	}
	if typ == "" {
		panic(fmt.Sprintf("unsupported DM field %s (%s)", field.Name, value.Kind()))
	}
	return strings.TrimSpace(typ + " " + extra)
}

func (d *dmDialect) count(query string, args ...interface{}) bool {
	var count int64
	return d.db.QueryRow(query, args...).Scan(&count) == nil && count > 0
}

// names are matched exactly because GORM creates quoted, case-sensitive objects.
// Schema-qualified table names are supported without changing the active schema.
func (d *dmDialect) tableName(table string) (string, string) {
	parts := strings.SplitN(table, ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return d.CurrentDatabase(), table
}
func (d *dmDialect) HasTable(table string) bool {
	owner, name := d.tableName(table)
	return d.count(`SELECT COUNT(*) FROM ALL_TABLES WHERE OWNER=? AND TABLE_NAME=?`, owner, name)
}
func (d *dmDialect) HasColumn(table, column string) bool {
	owner, name := d.tableName(table)
	return d.count(`SELECT COUNT(*) FROM ALL_TAB_COLUMNS WHERE OWNER=? AND TABLE_NAME=? AND COLUMN_NAME=?`, owner, name, column)
}
func (d *dmDialect) HasIndex(table, index string) bool {
	owner, name := d.tableName(table)
	return d.count(`SELECT COUNT(*) FROM ALL_INDEXES WHERE OWNER=? AND TABLE_NAME=? AND INDEX_NAME=?`, owner, name, indexIdentifier(index))
}
func (d *dmDialect) HasForeignKey(table, key string) bool {
	owner, name := d.tableName(table)
	return d.count(`SELECT COUNT(*) FROM ALL_CONSTRAINTS WHERE OWNER=? AND TABLE_NAME=? AND CONSTRAINT_NAME=? AND CONSTRAINT_TYPE='R'`, owner, name, key)
}
func (d *dmDialect) RemoveIndex(table, index string) error {
	owner, _ := d.tableName(table)
	_, err := d.db.Exec("DROP INDEX " + d.Quote(owner) + "." + d.Quote(indexIdentifier(index)))
	return err
}
func (d *dmDialect) ModifyColumn(table, column, typ string) error {
	_, err := d.db.Exec("ALTER TABLE " + table + " MODIFY " + column + " " + typ)
	return err
}
func (d *dmDialect) CurrentDatabase() string {
	var schema string
	_ = d.db.QueryRow(`SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM DUAL`).Scan(&schema)
	return schema
}
func (*dmDialect) LimitAndOffsetSQL(limit, offset interface{}) (string, error) {
	var sql string
	for _, entry := range []struct {
		name  string
		value interface{}
	}{{"LIMIT", limit}, {"OFFSET", offset}} {
		if entry.value == nil {
			continue
		}
		n, err := strconv.ParseInt(fmt.Sprint(entry.value), 10, 64)
		if err != nil {
			return "", err
		}
		if n >= 0 {
			sql += fmt.Sprintf(" %s %d", entry.name, n)
		}
	}
	return sql, nil
}
func (*dmDialect) SelectFromDummyTable() string                                   { return " FROM DUAL" }
func (*dmDialect) LastInsertIDOutputInterstitial(string, string, []string) string { return "" }
func (*dmDialect) LastInsertIDReturningSuffix(string, string) string              { return "" }
func (*dmDialect) DefaultValueStr() string                                        { return "DEFAULT VALUES" }
func (d *dmDialect) NormalizeIndexAndColumn(index, column string) (string, string) {
	return d.Quote(indexIdentifierQuoted(index)), column
}

// Root AddIndex emits indexName as an SQL fragment, unlike quoted table and
// column operands. Unquoted index names therefore fold to uppercase in DM.
func indexIdentifier(name string) string {
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		return strings.ReplaceAll(name[1:len(name)-1], `""`, `"`)
	}
	return strings.ToUpper(name)
}
func indexIdentifierQuoted(name string) string {
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		return strings.ReplaceAll(name[1:len(name)-1], `""`, `"`)
	}
	return name
}

// BatchCreateMaxRows avoids guessing consecutive identities for a multi-row
// INSERT. The batch remains one caller-owned or automatically owned transaction.
func (*dmDialect) BatchCreateMaxRows() int { return 1 }

// BatchSaveSQL uses a true in-place MERGE. In particular, matching rows keep
// their identities and created_at values; callbacks and transaction ownership
// remain under GORM's batch machinery. All inputs are quoted SQL fragments
// produced by GORM, and values contain bound placeholders or SQL expressions.
func (*dmDialect) BatchSaveSQL(table string, columns, conflicts, updates []string, values [][]string) (string, error) {
	if len(values) != 1 || len(columns) == 0 || len(values[0]) != len(columns) || len(conflicts) == 0 {
		return "", fmt.Errorf("gorm: DM MERGE requires one source row and a conflict target")
	}
	present := make(map[string]bool, len(columns))
	selects := make([]string, len(columns))
	insertValues := make([]string, len(columns))
	for i, column := range columns {
		present[column] = true
		selects[i] = values[0][i] + " AS " + column
		insertValues[i] = `"gorm_source".` + column
	}
	on := make([]string, len(conflicts))
	for i, column := range conflicts {
		if !present[column] {
			return "", fmt.Errorf("gorm: DM MERGE conflict column %s is missing from the source row", column)
		}
		on[i] = `"gorm_target".` + column + `="gorm_source".` + column
	}
	query := "MERGE INTO " + table + ` "gorm_target" USING (SELECT ` + strings.Join(selects, ",") + ` FROM DUAL) "gorm_source" ON (` + strings.Join(on, " AND ") + ")"
	if len(updates) > 0 {
		assignments := make([]string, len(updates))
		for i, column := range updates {
			if !present[column] {
				return "", fmt.Errorf("gorm: DM MERGE update column %s is missing from the source row", column)
			}
			assignments[i] = column + `="gorm_source".` + column
		}
		query += " WHEN MATCHED THEN UPDATE SET " + strings.Join(assignments, ",")
	}
	query += " WHEN NOT MATCHED THEN INSERT (" + strings.Join(columns, ",") + ") VALUES (" + strings.Join(insertValues, ",") + ")"
	return query, nil
}
