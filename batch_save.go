package gorm

import (
	"fmt"
	"reflect"
	"strings"
)

// SaveInBatches inserts a slice and updates the rows that already exist
// instead of failing on them. It is gorm v2's
// Save(slice), which is Create plus OnConflict{UpdateAll: true}, with the
// conflict target spelled out.
//
// The conflict target is conflictColumns (comma separated names are accepted,
// like the OnConflict helpers) and must be backed by a UNIQUE index. Without
// any it is the primary key, as with Save. MySQL has no conflict-target syntax:
// any unique key triggers the update there, and conflictColumns only decides
// which columns the update leaves alone.
//
//	db.SaveInBatches(codes, 500, "program_name", "code_id")
//
// Everything below follows from "the conflict target identifies the row":
//
//   - Every column except the target, the primary key and created_at is
//     rewritten from the struct, zero values included, and updated_at is set to
//     now. A soft-deleted row is restored when the struct's deleted_at is nil.
//   - When the target is not the primary key, the primary key is never written:
//     a stale id on the struct cannot collide with another row, and an existing
//     row keeps its id.
//   - Primary keys are not written back into the structs, because after an
//     upsert the database cannot say which rows were inserted.
//   - nil elements are skipped, the same as CreateInBatches.
//
// The call owns one transaction or reuses the current one. An error rolls back
// a transaction owned by this call; callers supplying a transaction retain
// responsibility for its commit or rollback. batchSize <= 0 means
// DefaultCreateBatchSize. SQLite, PostgreSQL and MySQL use native INSERT
// suffixes. Additional dialects can implement BatchSaveDialect; their
// BatchCreateDialect row limit also applies to saves.
//
// One chunk must not contain the same conflict key twice: SQLite applies the
// rows in order so the later one wins, but PostgreSQL rejects the statement
// ("cannot affect row a second time"). Callers that can repeat a key dedupe
// first.
func (s *DB) SaveInBatches(value interface{}, batchSize int, conflictColumns ...string) *DB {
	if batchSize <= 0 {
		batchSize = DefaultCreateBatchSize
	}
	return s.createInBatches(value, batchSize, newBatchSave(conflictColumns))
}

// batchSave is the upsert one SaveInBatches call asks the batch insert to do.
// It travels as an argument instead of as state on the DB handle, so a handle
// that was used for a save behaves exactly as before afterwards.
type batchSave struct {
	conflict []string // conflict target as given; empty means the primary key
}

func newBatchSave(args []string) *batchSave {
	save := &batchSave{}
	for _, arg := range args {
		for _, name := range strings.Split(arg, ",") {
			if name = strings.TrimSpace(name); name != "" {
				save.conflict = append(save.conflict, name)
			}
		}
	}
	return save
}

// targets reports whether name in the conflict list refers to field. It uses
// the same matching as Scope.FieldByName.
func (b *batchSave) targets(field *Field) bool {
	for _, name := range b.conflict {
		if field.Name == name || field.DBName == name || field.DBName == ToColumnName(name) {
			return true
		}
	}
	return false
}

// keepsPrimaryKey reports whether the primary key column belongs in the INSERT.
// It does only when the key is the conflict target.
func (b *batchSave) keepsPrimaryKey(field *Field) bool {
	return len(b.conflict) == 0 || b.targets(field)
}

// target resolves the conflict target to database column names.
func (b *batchSave) target(scope *Scope) ([]string, error) {
	if len(b.conflict) == 0 {
		var names []string
		for _, field := range scope.PrimaryFields() {
			names = append(names, field.DBName)
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("gorm: SaveInBatches on %T needs conflict columns, the model has no primary key", scope.Value)
		}
		return names, nil
	}
	names := make([]string, 0, len(b.conflict))
	for _, name := range b.conflict {
		field, ok := scope.FieldByName(name)
		if !ok || !field.IsNormal || field.IsIgnored {
			return nil, fmt.Errorf("gorm: SaveInBatches conflict column %q is not a column of %T", name, scope.Value)
		}
		names = append(names, field.DBName)
	}
	return names, nil
}

// statementColumns resolves the target and the update list for the columns
// this statement actually inserts. Quoted names are shared by suffix-based and
// complete-statement dialects, including rows with different default shapes.
func (b *batchSave) statementColumns(scope *Scope, columns []string) (quotedTarget, updates []string, err error) {
	target, err := b.target(scope)
	if err != nil {
		return nil, nil, err
	}

	// The update never rewrites the columns that identify the row, nor created_at.
	// Leaving the target out also matters for speed: assigning an indexed column,
	// even to its current value, makes the database rewrite those index entries.
	fixed := make(map[string]bool, len(target)+2)
	quotedTarget = make([]string, len(target))
	for i, name := range target {
		quotedTarget[i] = scope.Quote(name)
		fixed[quotedTarget[i]] = true
	}
	for _, field := range scope.PrimaryFields() {
		fixed[scope.Quote(field.DBName)] = true
	}
	if field, ok := scope.FieldByName("CreatedAt"); ok {
		fixed[scope.Quote(field.DBName)] = true
	}
	updates = make([]string, 0, len(columns))
	for _, column := range columns {
		if !fixed[column] {
			updates = append(updates, column)
		}
	}

	return quotedTarget, updates, nil
}

// generatedPrimaryInsert reports a fresh row whose sole conflict target is an
// omitted, auto-generated integer primary key. A complete-statement dialect
// cannot match a key that does not exist yet; it must insert this row normally.
func (b *batchSave) generatedPrimaryInsert(scope *Scope, columns []string) bool {
	target, err := b.target(scope)
	primary := scope.PrimaryFields()
	if err != nil || len(target) != 1 || len(primary) != 1 || target[0] != primary[0].DBName || !primary[0].IsBlank {
		return false
	}
	field := primary[0]
	if auto, ok := field.TagSettingsGet("AUTO_INCREMENT"); ok && strings.EqualFold(auto, "false") {
		return false
	}
	fieldType := field.Field.Type()
	for fieldType.Kind() == reflect.Ptr {
		fieldType = fieldType.Elem()
	}
	kind := fieldType.Kind()
	if kind < reflect.Int || kind > reflect.Uint64 {
		return false
	}
	quoted := scope.Quote(field.DBName)
	for _, column := range columns {
		if column == quoted {
			return false
		}
	}
	return true
}

// insertOption returns the native INSERT suffix for the built-in dialects.
func (b *batchSave) insertOption(scope *Scope, columns []string) (string, error) {
	quotedTarget, updates, err := b.statementColumns(scope, columns)
	if err != nil {
		return "", err
	}
	switch dialect := scope.Dialect().GetName(); dialect {
	case "mysql":
		assignments := make([]string, 0, len(updates))
		for _, column := range updates {
			assignments = append(assignments, column+"=VALUES("+column+")")
		}
		if len(assignments) == 0 {
			// MySQL has no DO NOTHING; assigning a key column to itself is the no-op.
			assignments = append(assignments, quotedTarget[0]+"="+quotedTarget[0])
		}
		return "ON DUPLICATE KEY UPDATE " + strings.Join(assignments, ","), nil
	case "sqlite3", "postgres":
		conflict := "ON CONFLICT(" + strings.Join(quotedTarget, ",") + ")"
		if len(updates) == 0 {
			return conflict + " DO NOTHING", nil
		}
		assignments := make([]string, 0, len(updates))
		for _, column := range updates {
			assignments = append(assignments, column+"=excluded."+column)
		}
		return conflict + " DO UPDATE SET " + strings.Join(assignments, ","), nil
	default:
		return "", fmt.Errorf("gorm: SaveInBatches does not support the %q dialect", dialect)
	}
}

// batchErrorDB returns a handle that carries err, keeping the fluent *DB shape
// for argument errors.
func batchErrorDB(db *DB, err error) *DB {
	db.AddError(err)
	return db
}
