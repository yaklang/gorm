package gorm

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Use a real SQLite database behind an optional dialect to exercise the core
// contract independently of the DM driver or its database. The dialect rejects
// multi-row statements, so these tests also verify the single-row bound.
type batchExtensionDialect struct{ sqlite3 }

func (*batchExtensionDialect) GetName() string         { return "batch_extension" }
func (*batchExtensionDialect) BatchCreateMaxRows() int { return 1 }
func (*batchExtensionDialect) BatchSaveSQL(table string, columns, target, updates []string, values [][]string) (string, error) {
	if len(values) != 1 {
		return "", errors.New("batch extension expected one source row")
	}
	present := make(map[string]bool, len(columns))
	for _, column := range columns {
		present[column] = true
	}
	for _, column := range target {
		if !present[column] {
			return "", fmt.Errorf("missing conflict column %s", column)
		}
	}
	statement := "INSERT INTO " + table + " (" + strings.Join(columns, ",") + ") VALUES (" + strings.Join(values[0], ",") + ") ON CONFLICT(" + strings.Join(target, ",") + ")"
	if len(updates) == 0 {
		return statement + " DO NOTHING", nil
	}
	assignments := make([]string, len(updates))
	for i, column := range updates {
		assignments[i] = column + "=excluded." + column
	}
	return statement + " DO UPDATE SET " + strings.Join(assignments, ","), nil
}

func init() { RegisterDialect("batch_extension", &batchExtensionDialect{}) }

type batchExtensionDoc struct {
	Model
	Key       string `gorm:"unique_index:ux_batch_extension_key"`
	Value     int
	Events    *[]string `gorm:"-"`
	FailAfter bool      `gorm:"-"`
}

func (*batchExtensionDoc) TableName() string { return "batch_extension_docs" }
func (d *batchExtensionDoc) event(name string) {
	if d.Events != nil {
		*d.Events = append(*d.Events, d.Key+":"+name)
	}
}
func (d *batchExtensionDoc) BeforeSave() error   { d.event("before_save"); return nil }
func (d *batchExtensionDoc) BeforeCreate() error { d.event("before_create"); return nil }
func (d *batchExtensionDoc) AfterCreate(scope *Scope) error {
	d.event("after_create")
	var found batchExtensionDoc
	if err := scope.NewDB().Unscoped().Where("key = ?", d.Key).First(&found).Error; err != nil {
		return fmt.Errorf("after_create did not see its write in the transaction: %w", err)
	}
	if found.Value != d.Value {
		return errors.New("after_create observed old row")
	}
	return nil
}
func (d *batchExtensionDoc) AfterSave() error {
	d.event("after_save")
	if d.FailAfter {
		return errors.New("requested after_save failure")
	}
	return nil
}

func setupBatchExtensionDB(t *testing.T) *DB {
	t.Helper()
	sqlDB := setupBatchDB(t)
	t.Cleanup(func() { sqlDB.Close() })
	db, err := Open("batch_extension", sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&batchExtensionDoc{}).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func extensionDoc(t *testing.T, db *DB, key string) batchExtensionDoc {
	t.Helper()
	var found batchExtensionDoc
	if err := db.Unscoped().Where("key = ?", key).First(&found).Error; err != nil {
		t.Fatal(err)
	}
	return found
}

func TestBatchDialectCreateReadsEachIdentityAndHooks(t *testing.T) {
	db := setupBatchExtensionDB(t)
	var events []string
	rows := []*batchExtensionDoc{
		{Key: "first", Value: 1, Events: &events},
		{Model: Model{ID: 42}, Key: "explicit", Value: 2, Events: &events},
		nil,
		{Key: "after-gap", Value: 3, Events: &events},
	}
	result := db.CreateInBatches(rows, 10)
	if result.Error != nil || result.RowsAffected != 3 {
		t.Fatalf("create: rows=%d err=%v", result.RowsAffected, result.Error)
	}
	for i, wantID := range []uint{1, 42, 43} {
		row := rows[i]
		if i == 2 {
			row = rows[3]
		}
		if row.ID != wantID || extensionDoc(t, db, row.Key).ID != wantID {
			t.Fatalf("row %s identity=%d want=%d", row.Key, row.ID, wantID)
		}
	}
	want := []string{
		"first:before_save", "first:before_create", "explicit:before_save", "explicit:before_create", "after-gap:before_save", "after-gap:before_create",
		"first:after_create", "first:after_save", "explicit:after_create", "explicit:after_save", "after-gap:after_create", "after-gap:after_save",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("hook order: got %v want %v", events, want)
	}
}

func TestBatchDialectSavePreservesIdentityAndCreationTime(t *testing.T) {
	db := setupBatchExtensionDB(t)
	original := &batchExtensionDoc{Key: "existing", Value: 99}
	other := &batchExtensionDoc{Key: "other", Value: 11}
	if err := db.CreateInBatches([]*batchExtensionDoc{original, other}, 50).Error; err != nil {
		t.Fatal(err)
	}
	stored := extensionDoc(t, db, "existing")
	if err := db.Delete(original).Error; err != nil {
		t.Fatal(err)
	}
	var events []string
	stale := &batchExtensionDoc{Model: Model{ID: other.ID, CreatedAt: stored.CreatedAt.Add(-time.Hour)}, Key: "existing", Events: &events}
	fresh := &batchExtensionDoc{Key: "fresh", Value: 7, Events: &events}
	result := db.SaveInBatches([]*batchExtensionDoc{stale, nil, fresh}, 20, "Key")
	if result.Error != nil || result.RowsAffected != 2 {
		t.Fatalf("save: rows=%d err=%v", result.RowsAffected, result.Error)
	}
	got := extensionDoc(t, db, "existing")
	if got.ID != stored.ID || !got.CreatedAt.Equal(stored.CreatedAt) || got.Value != 0 || got.DeletedAt != nil || !got.UpdatedAt.After(stored.UpdatedAt) {
		t.Fatalf("upsert must preserve identity/create time and update snapshot: old=%+v got=%+v", stored, got)
	}
	if stale.ID != other.ID || fresh.ID != 0 {
		t.Fatalf("save must not assign generated IDs to structs: stale=%d fresh=%d", stale.ID, fresh.ID)
	}
	if gotOther := extensionDoc(t, db, "other"); gotOther.Value != 11 || gotOther.ID != other.ID {
		t.Fatalf("stale ID affected another key: %+v", gotOther)
	}
	want := []string{"existing:before_save", "existing:before_create", "fresh:before_save", "fresh:before_create", "existing:after_create", "existing:after_save", "fresh:after_create", "fresh:after_save"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("hook order: got %v want %v", events, want)
	}
}

func TestBatchDialectSaveGeneratedPrimaryUsesInsertWithoutIDWriteback(t *testing.T) {
	db := setupBatchExtensionDB(t)
	fresh := &batchExtensionDoc{Key: "fresh", Value: 3}
	if err := db.SaveInBatches([]*batchExtensionDoc{fresh}, 10).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.ID != 0 {
		t.Fatalf("save wrote ID %d back", fresh.ID)
	}
	loaded := extensionDoc(t, db, "fresh")
	loaded.Value = 8
	if err := db.SaveInBatches([]*batchExtensionDoc{&loaded}, 10).Error; err != nil {
		t.Fatal(err)
	}
	if got := extensionDoc(t, db, "fresh"); got.ID != loaded.ID || got.Value != 8 {
		t.Fatalf("primary key save: %+v", got)
	}
}

func TestBatchDialectRollbackAcrossChunksAndCallerTransaction(t *testing.T) {
	for _, save := range []bool{false, true} {
		t.Run(fmt.Sprint("save=", save), func(t *testing.T) {
			db := setupBatchExtensionDB(t)
			initial := &batchExtensionDoc{Key: "existing", Value: 1}
			if err := db.Create(initial).Error; err != nil {
				t.Fatal(err)
			}
			rows := []*batchExtensionDoc{{Key: "existing", Value: 2}, {Key: "new", Value: 3}, {Key: "failing", Value: 4, FailAfter: true}}
			if !save {
				rows[0].Key = "first-new"
			}
			var result *DB
			if save {
				result = db.SaveInBatches(rows, 2, "key")
			} else {
				result = db.CreateInBatches(rows, 2)
			}
			if result.Error == nil || !strings.Contains(result.Error.Error(), "requested after_save failure") {
				t.Fatalf("wanted callback failure: %v", result.Error)
			}
			var count int
			if err := db.Model(&batchExtensionDoc{}).Count(&count).Error; err != nil || count != 1 || extensionDoc(t, db, "existing").Value != 1 {
				t.Fatalf("owned batch transaction was not rolled back: count=%d err=%v", count, err)
			}
			tx := db.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			if err := tx.Create(&batchExtensionDoc{Key: "caller", Value: 9}).Error; err != nil {
				t.Fatal(err)
			}
			if save {
				result = tx.SaveInBatches([]*batchExtensionDoc{{Key: "existing", Value: 10}}, 2, "key")
			} else {
				result = tx.CreateInBatches([]*batchExtensionDoc{{Key: "shared", Value: 10}}, 2)
			}
			if result.Error != nil {
				t.Fatal(result.Error)
			}
			// Another write proves the helper did not commit its caller's transaction.
			if err := tx.Create(&batchExtensionDoc{Key: "after", Value: 12}).Error; err != nil {
				t.Fatal(err)
			}
			if err := tx.Rollback().Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&batchExtensionDoc{}).Count(&count).Error; err != nil || count != 1 || extensionDoc(t, db, "existing").Value != 1 {
				t.Fatalf("shared transaction escaped rollback: count=%d err=%v", count, err)
			}
		})
	}
}

func TestBatchDialectSaveRejectsInsertModifier(t *testing.T) {
	db := setupBatchExtensionDB(t)
	result := db.Set("gorm:insert_modifier", "OR REPLACE").SaveInBatches([]*batchExtensionDoc{{Key: "x"}}, 10, "key")
	if result.Error == nil || !strings.Contains(result.Error.Error(), "gorm:insert_modifier") {
		t.Fatalf("modifier must not be ignored: %v", result.Error)
	}
}

type batchExtensionDefaultKey struct {
	ID    int    `gorm:"primary_key"`
	Key   string `gorm:"unique_index;default:'default-key'"`
	Value int
}

func TestBatchDialectSaveRejectsMissingLogicalTargetAndRollsBack(t *testing.T) {
	db := setupBatchExtensionDB(t)
	if err := db.AutoMigrate(&batchExtensionDefaultKey{}).Error; err != nil {
		t.Fatal(err)
	}
	rows := []*batchExtensionDefaultKey{{Key: "would-insert", Value: 1}, {Value: 2}}
	result := db.SaveInBatches(rows, 1, "key")
	if result.Error == nil || !strings.Contains(result.Error.Error(), "missing conflict column") {
		t.Fatalf("missing logical key was silently bypassed: %v", result.Error)
	}
	var count int
	if err := db.Model(&batchExtensionDefaultKey{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("earlier chunk escaped rollback: count=%d err=%v", count, err)
	}
}

func TestBatchDialectSQLFailureRollsBackEarlierStatements(t *testing.T) {
	db := setupBatchExtensionDB(t)
	if err := db.Create(&batchExtensionDoc{Key: "duplicate", Value: 1}).Error; err != nil {
		t.Fatal(err)
	}
	result := db.CreateInBatches([]*batchExtensionDoc{{Key: "first-new", Value: 2}, {Key: "another-new", Value: 3}, {Key: "duplicate", Value: 4}}, 2)
	if result.Error == nil {
		t.Fatal("expected the last chunk to violate uniqueness")
	}
	var count int
	if err := db.Model(&batchExtensionDoc{}).Count(&count).Error; err != nil || count != 1 || extensionDoc(t, db, "duplicate").Value != 1 {
		t.Fatalf("SQL error did not roll back earlier chunks: count=%d err=%v", count, err)
	}
}

type batchExtensionPointerID struct {
	ID    *uint `gorm:"primary_key"`
	Value string
}

func TestBatchDialectSaveGeneratedPointerPrimaryUsesInsert(t *testing.T) {
	db := setupBatchExtensionDB(t)
	if err := db.AutoMigrate(&batchExtensionPointerID{}).Error; err != nil {
		t.Fatal(err)
	}
	fresh := &batchExtensionPointerID{Value: "fresh"}
	if err := db.SaveInBatches([]*batchExtensionPointerID{fresh}, 10).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.ID != nil {
		t.Fatalf("save must not write generated pointer ID back: %v", *fresh.ID)
	}
	var loaded batchExtensionPointerID
	if err := db.Where("value = ?", "fresh").First(&loaded).Error; err != nil {
		t.Fatal(err)
	}
	if loaded.ID == nil || *loaded.ID == 0 {
		t.Fatalf("no generated pointer primary key: %+v", loaded)
	}
	loaded.Value = "updated"
	if err := db.SaveInBatches([]*batchExtensionPointerID{&loaded}, 10).Error; err != nil {
		t.Fatal(err)
	}
	var got batchExtensionPointerID
	if err := db.First(&got, *loaded.ID).Error; err != nil || got.Value != "updated" {
		t.Fatalf("existing pointer primary key save failed: got=%+v err=%v", got, err)
	}
}
