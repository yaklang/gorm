package gorm

import (
	"strings"
	"testing"
	"time"
)

// savedDoc mirrors the shape SaveInBatches exists for: a surrogate primary key,
// a natural unique key, soft delete, and timestamps.
type savedDoc struct {
	Model
	Program string `gorm:"unique_index:ux_saved_docs_key"`
	CodeID  int64  `gorm:"unique_index:ux_saved_docs_key"`
	Name    string
	Age     int
}

func (d *savedDoc) TableName() string { return "saved_docs" }

func setupSavedDocs(t *testing.T) *DB {
	t.Helper()
	sqlDB := setupBatchDB(t)
	t.Cleanup(func() { sqlDB.Close() })
	db, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if r := db.AutoMigrate(&savedDoc{}); r.Error != nil {
		t.Fatal(r.Error)
	}
	return db
}

func mustSave(t *testing.T, db *DB, value interface{}, size int, conflict ...string) {
	t.Helper()
	if r := db.SaveInBatches(value, size, conflict...); r.Error != nil {
		t.Fatalf("SaveInBatches: %v", r.Error)
	}
}

func loadDoc(t *testing.T, db *DB, program string, code int64) savedDoc {
	t.Helper()
	var got savedDoc
	if err := db.Where("program = ? AND code_id = ?", program, code).First(&got).Error; err != nil {
		t.Fatalf("load %s/%d: %v", program, code, err)
	}
	return got
}

func countDocs(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	if err := db.Unscoped().Model(&savedDoc{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSaveInBatches_InsertsAndUpdatesInOneCall(t *testing.T) {
	db := setupSavedDocs(t)
	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 1, Name: "a1", Age: 1}, {Program: "p", CodeID: 2, Name: "b1", Age: 2}}, 10, "program", "code_id")
	before := loadDoc(t, db, "p", 1)

	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 1, Name: "a2", Age: 11}, {Program: "p", CodeID: 3, Name: "c1", Age: 30}}, 10, "program", "code_id")

	if got := loadDoc(t, db, "p", 1); got.Name != "a2" || got.Age != 11 {
		t.Fatalf("existing row not updated: %+v", got)
	}
	if got := loadDoc(t, db, "p", 1); got.ID != before.ID {
		t.Fatalf("an update must keep the row id: %d -> %d", before.ID, got.ID)
	}
	if got := loadDoc(t, db, "p", 3); got.Name != "c1" {
		t.Fatalf("new row not inserted: %+v", got)
	}
	if n := countDocs(t, db); n != 3 {
		t.Fatalf("want 3 rows, got %d", n)
	}
}

// The caller no longer has to reset a struct before saving it. A struct that
// carries ids, timestamps and a soft-delete mark from an earlier load or save
// still lands on the row its conflict columns name.
func TestSaveInBatches_DoesNotNeedCallerSideReset(t *testing.T) {
	db := setupSavedDocs(t)
	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 1, Name: "one"}, {Program: "p", CodeID: 2, Name: "two"}}, 10, "program", "code_id")
	one := loadDoc(t, db, "p", 1)
	two := loadDoc(t, db, "p", 2)

	// Stale surrogate id taken from a DIFFERENT row, plus stale timestamps.
	stale := &savedDoc{
		Model:   Model{ID: two.ID, CreatedAt: one.CreatedAt.Add(-time.Hour), UpdatedAt: one.UpdatedAt.Add(-time.Hour)},
		Program: "p", CodeID: 1, Name: "renewed",
	}
	mustSave(t, db, []*savedDoc{stale}, 10, "program", "code_id")

	got := loadDoc(t, db, "p", 1)
	if got.ID != one.ID {
		t.Fatalf("stale id must not move or collide with another row: want id %d, got %d", one.ID, got.ID)
	}
	if got.Name != "renewed" {
		t.Fatalf("row not updated: %+v", got)
	}
	if other := loadDoc(t, db, "p", 2); other.Name != "two" || other.ID != two.ID {
		t.Fatalf("the row the stale id pointed at must be untouched: %+v", other)
	}
	if !got.CreatedAt.Equal(one.CreatedAt) {
		t.Fatalf("a stale created_at on the struct must not overwrite the stored one: %v -> %v", one.CreatedAt, got.CreatedAt)
	}
}

// The struct is the snapshot: a deleted_at it carries is written, so a caller
// that wants a soft-deleted row restored passes a struct with deleted_at nil.
// (gorm v2's Save(slice) behaves the same.)
func TestSaveInBatches_StructDeletedAtIsAuthoritative(t *testing.T) {
	db := setupSavedDocs(t)
	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 1, Name: "live"}}, 10, "program", "code_id")

	gone := time.Now().Add(-time.Hour)
	mustSave(t, db, []*savedDoc{{Model: Model{DeletedAt: &gone}, Program: "p", CodeID: 1, Name: "gone"}}, 10, "program", "code_id")
	var n int
	db.Model(&savedDoc{}).Where("code_id = ?", 1).Count(&n)
	if n != 0 {
		t.Fatal("a struct carrying deleted_at must be saved as deleted")
	}

	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 1, Name: "back"}}, 10, "program", "code_id")
	if got := loadDoc(t, db, "p", 1); got.Name != "back" || got.DeletedAt != nil {
		t.Fatalf("a struct with deleted_at nil must restore the row: %+v", got)
	}
}

func TestSaveInBatches_StaleIDOnNewKeyInserts(t *testing.T) {
	db := setupSavedDocs(t)
	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 1, Name: "one"}}, 10, "program", "code_id")
	one := loadDoc(t, db, "p", 1)

	mustSave(t, db, []*savedDoc{{Model: Model{ID: one.ID}, Program: "p", CodeID: 2, Name: "two"}}, 10, "program", "code_id")

	if n := countDocs(t, db); n != 2 {
		t.Fatalf("want 2 rows, got %d", n)
	}
	if got := loadDoc(t, db, "p", 2); got.ID == one.ID {
		t.Fatal("new natural key took over the surrogate id of another row")
	}
}

func TestSaveInBatches_ZeroValuesOverwrite(t *testing.T) {
	db := setupSavedDocs(t)
	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 1, Name: "old", Age: 55}}, 10, "program", "code_id")
	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 1}}, 10, "program", "code_id")
	if got := loadDoc(t, db, "p", 1); got.Name != "" || got.Age != 0 {
		t.Fatalf("zero values must overwrite the previous snapshot, got %+v", got)
	}
}

func TestSaveInBatches_RestoresSoftDeletedRowInPlace(t *testing.T) {
	db := setupSavedDocs(t)
	doc := &savedDoc{Program: "p", CodeID: 7, Name: "old"}
	mustSave(t, db, []*savedDoc{doc}, 10, "program", "code_id")
	original := loadDoc(t, db, "p", 7)
	if r := db.Delete(&original); r.Error != nil {
		t.Fatal(r.Error)
	}

	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 7, Name: "new"}}, 10, "program", "code_id")

	got := loadDoc(t, db, "p", 7) // the default scope hides soft-deleted rows
	if got.ID != original.ID || got.Name != "new" || got.DeletedAt != nil {
		t.Fatalf("soft-deleted row must be restored in place: %+v (was id %d)", got, original.ID)
	}
	if n := countDocs(t, db); n != 1 {
		t.Fatalf("want 1 row, got %d", n)
	}
}

func TestSaveInBatches_CreatedAtKeptAndUpdatedAtAdvances(t *testing.T) {
	db := setupSavedDocs(t)
	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 1, Name: "v1"}}, 10, "program", "code_id")
	first := loadDoc(t, db, "p", 1)

	// A struct that was loaded keeps its old UpdatedAt; saving must still bump
	// it. Saving stamps the struct itself, so remember the stored values first.
	createdBefore, updatedBefore := first.CreatedAt, first.UpdatedAt
	first.Name = "v2"
	mustSave(t, db, []*savedDoc{&first}, 10, "program", "code_id")
	second := loadDoc(t, db, "p", 1)

	if !second.CreatedAt.Equal(createdBefore) {
		t.Fatalf("created_at must survive an update: %v -> %v", createdBefore, second.CreatedAt)
	}
	if !second.UpdatedAt.After(updatedBefore) {
		t.Fatalf("updated_at must advance: %v -> %v", updatedBefore, second.UpdatedAt)
	}
	if second.Name != "v2" {
		t.Fatalf("name not updated: %q", second.Name)
	}
}

func TestSaveInBatches_SkipsNilElements(t *testing.T) {
	db := setupSavedDocs(t)
	mustSave(t, db, []*savedDoc{nil, {Program: "p", CodeID: 1, Name: "a"}, nil, {Program: "p", CodeID: 2, Name: "b"}, nil}, 10, "program", "code_id")
	if n := countDocs(t, db); n != 2 {
		t.Fatalf("want 2 rows, got %d", n)
	}

	// All nil, empty, and a nil first element in a later chunk.
	mustSave(t, db, []*savedDoc{nil, nil}, 10, "program", "code_id")
	mustSave(t, db, []*savedDoc{}, 10, "program", "code_id")
	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 3, Name: "c"}, {Program: "p", CodeID: 4, Name: "d"}, nil, {Program: "p", CodeID: 5, Name: "e"}}, 2, "program", "code_id")
	if n := countDocs(t, db); n != 5 {
		t.Fatalf("want 5 rows, got %d", n)
	}

	// Plain CreateInBatches skips nil elements the same way.
	if r := db.CreateInBatches([]*savedDoc{{Program: "p", CodeID: 6, Name: "f"}, nil}, 10); r.Error != nil {
		t.Fatalf("CreateInBatches with a nil element: %v", r.Error)
	}
	if n := countDocs(t, db); n != 6 {
		t.Fatalf("want 6 rows, got %d", n)
	}
}

func TestSaveInBatches_SpansChunks(t *testing.T) {
	db := setupSavedDocs(t)
	const rows = 25
	first := make([]*savedDoc, rows)
	for i := range first {
		first[i] = &savedDoc{Program: "p", CodeID: int64(i), Name: "before", Age: i}
	}
	mustSave(t, db, first, 4, "program", "code_id")

	second := make([]*savedDoc, rows+5)
	for i := range second {
		second[i] = &savedDoc{Program: "p", CodeID: int64(i), Name: "after", Age: i + 100}
	}
	mustSave(t, db, second, 4, "program", "code_id")

	var stale int
	db.Model(&savedDoc{}).Where("name <> ?", "after").Count(&stale)
	if stale != 0 {
		t.Fatalf("%d rows were not updated across chunks", stale)
	}
	if n := countDocs(t, db); n != rows+5 {
		t.Fatalf("want %d rows, got %d", rows+5, n)
	}
}

func TestSaveInBatches_RejectedStatementKeepsPreviousRows(t *testing.T) {
	db := setupSavedDocs(t)
	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 1, Name: "old"}, {Program: "p", CodeID: 2, Name: "old2"}}, 10, "program", "code_id")
	if r := db.Exec(`CREATE TRIGGER reject_saved_doc BEFORE UPDATE ON saved_docs
		WHEN NEW.name = 'reject' BEGIN SELECT RAISE(ABORT, 'rejected doc'); END`); r.Error != nil {
		t.Fatal(r.Error)
	}

	// Two chunks; the second one is rejected, so the first must roll back too.
	r := db.SaveInBatches([]*savedDoc{{Program: "p", CodeID: 1, Name: "changed"}, {Program: "p", CodeID: 2, Name: "reject"}}, 1, "program", "code_id")
	if r.Error == nil || !strings.Contains(r.Error.Error(), "rejected doc") {
		t.Fatalf("want the trigger error, got %v", r.Error)
	}
	if got := loadDoc(t, db, "p", 1); got.Name != "old" {
		t.Fatalf("a failed save must leave every previous row untouched, got %q", got.Name)
	}
}

// A handle that was used for a save must behave as before: no conflict clause
// may stay behind on it.
func TestSaveInBatches_LeavesReceiverUntouched(t *testing.T) {
	db := setupSavedDocs(t)
	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 1, Name: "one"}}, 10, "program", "code_id")

	if r := db.CreateInBatches([]*savedDoc{{Program: "p", CodeID: 1, Name: "dup"}}, 10); r.Error == nil {
		t.Fatal("a plain insert of an existing natural key must still fail on the same handle")
	}
	if r := db.Create(&savedDoc{Program: "p", CodeID: 1, Name: "dup"}); r.Error == nil {
		t.Fatal("Create must still fail on a duplicate natural key")
	}
}

func TestSaveInBatches_DoesNotWritePrimaryKeysBack(t *testing.T) {
	db := setupSavedDocs(t)
	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 1}, {Program: "p", CodeID: 2}}, 10, "program", "code_id")

	// A new row first, then two rows that exist already: an id derived from the
	// last insert id would be wrong for all three.
	rows := []*savedDoc{{Program: "p", CodeID: 3}, {Program: "p", CodeID: 1}, {Program: "p", CodeID: 2}}
	mustSave(t, db, rows, 10, "program", "code_id")
	for _, row := range rows {
		if row.ID != 0 {
			t.Fatalf("code_id=%d got a back-filled id %d that cannot be trusted after an upsert", row.CodeID, row.ID)
		}
	}
}

func TestSaveInBatches_ScopedReceiverDoesNotFilterRows(t *testing.T) {
	db := setupSavedDocs(t)
	scoped := db.Where("program = ?", "p").Model(&savedDoc{})
	mustSave(t, scoped, []*savedDoc{{Program: "p", CodeID: 1, Name: "a"}, {Program: "p", CodeID: 1, Name: "b"}}, 10, "program", "code_id")
	if got := loadDoc(t, db, "p", 1); got.Name != "b" {
		t.Fatalf("want the later duplicate to win, got %q", got.Name)
	}
}

func TestSaveInBatches_PrimaryKeyIsTheDefaultTarget(t *testing.T) {
	sqlDB := setupBatchDB(t)
	defer sqlDB.Close()
	db, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if r := db.CreateInBatches([]*BatchUser{{ID: 1, Name: "a1", Age: 10}, {ID: 2, Name: "b1", Age: 20}}, 10); r.Error != nil {
		t.Fatal(r.Error)
	}
	if r := db.SaveInBatches([]*BatchUser{{ID: 1, Name: "a2", Age: 11}, {ID: 3, Name: "c1", Age: 30}}, 10); r.Error != nil {
		t.Fatal(r.Error)
	}
	var u1, u3 BatchUser
	db.Where("id = ?", 1).First(&u1)
	db.Where("id = ?", 3).First(&u3)
	if u1.Name != "a2" || u1.Age != 11 || u3.Name != "c1" {
		t.Fatalf("primary-key save wrong: %+v %+v", u1, u3)
	}
	if u1.ID != 1 {
		t.Fatalf("explicit primary key must be kept when it is the target, got %d", u1.ID)
	}
}

// Some rows carry an explicit key and some do not, so one chunk holds two
// statement shapes; each needs a SET list that matches its own columns.
func TestSaveInBatches_MixedColumnShapes(t *testing.T) {
	sqlDB := setupBatchDB(t)
	defer sqlDB.Close()
	if _, err := sqlDB.Exec(`CREATE TABLE batch_default_users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL DEFAULT 'anonymous')`); err != nil {
		t.Fatal(err)
	}
	db, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if r := db.Create(&BatchDefaultUser{ID: 1, Name: "kept"}); r.Error != nil {
		t.Fatal(r.Error)
	}
	rows := []*BatchDefaultUser{{ID: 1, Name: "renamed"}, {Name: "fresh"}, {ID: 9, Name: "nine"}}
	if r := db.SaveInBatches(rows, 10); r.Error != nil {
		t.Fatalf("SaveInBatches: %v", r.Error)
	}
	var names []string
	db.Model(&BatchDefaultUser{}).Order("id ASC").Pluck("name", &names)
	if strings.Join(names, ",") != "renamed,fresh,nine" {
		t.Fatalf("names = %v", names)
	}
}

func TestSaveInBatches_ArgumentErrorsAreLoud(t *testing.T) {
	db := setupSavedDocs(t)
	docs := []*savedDoc{{Program: "p", CodeID: 1}}

	if r := db.SaveInBatches(docs, 10, "program", "no_such_column"); r.Error == nil {
		t.Fatal("an unknown conflict column must fail instead of silently inserting")
	}
	if r := db.SaveInBatches(&savedDoc{Program: "p", CodeID: 1}, 10, "program", "code_id"); r.Error == nil {
		t.Fatal("a non-slice value must fail instead of silently creating")
	}
	if n := countDocs(t, db); n != 0 {
		t.Fatalf("a failed call must not write anything, got %d rows", n)
	}
}

// A conflict clause the caller put on the handle would be silently replaced by
// the save's own; refuse instead.
func TestSaveInBatches_RefusesCallerInsertOption(t *testing.T) {
	db := setupSavedDocs(t)
	r := db.Set("gorm:insert_option", "ON CONFLICT DO NOTHING").SaveInBatches([]*savedDoc{{Program: "p", CodeID: 1}}, 10, "program", "code_id")
	if r.Error == nil {
		t.Fatal("SaveInBatches must not silently override a caller's insert option")
	}
	if n := countDocs(t, db); n != 0 {
		t.Fatalf("a refused call must not write, got %d rows", n)
	}
}

func TestSaveInBatches_ConflictColumnsAcceptCommaList(t *testing.T) {
	db := setupSavedDocs(t)
	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 1, Name: "a"}}, 10, "program,code_id")
	mustSave(t, db, []*savedDoc{{Program: "p", CodeID: 1, Name: "b"}}, 10, " program , code_id ")
	if n := countDocs(t, db); n != 1 {
		t.Fatalf("want 1 row, got %d", n)
	}
	if got := loadDoc(t, db, "p", 1); got.Name != "b" {
		t.Fatalf("got %q", got.Name)
	}
}

// With nothing left to update the clause must still be valid SQL and keep the
// existing row.
func TestSaveInBatches_NothingToUpdateKeepsRow(t *testing.T) {
	sqlDB := setupBatchDB(t)
	defer sqlDB.Close()
	if _, err := sqlDB.Exec(`CREATE TABLE key_only (a TEXT NOT NULL, b TEXT NOT NULL, UNIQUE(a, b))`); err != nil {
		t.Fatal(err)
	}
	db, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	type KeyOnly struct {
		A string
		B string
	}
	db = db.Table("key_only")
	if r := db.SaveInBatches([]*KeyOnly{{A: "x", B: "y"}}, 10, "a", "b"); r.Error != nil {
		t.Fatal(r.Error)
	}
	if r := db.SaveInBatches([]*KeyOnly{{A: "x", B: "y"}, {A: "x", B: "z"}}, 10, "a", "b"); r.Error != nil {
		t.Fatalf("key-only save must be a no-op for existing rows, got %v", r.Error)
	}
	var n int
	db.Table("key_only").Count(&n)
	if n != 2 {
		t.Fatalf("want 2 rows, got %d", n)
	}
}

func TestBatchSaveInsertOptionPerDialect(t *testing.T) {
	cases := []struct {
		dialect string
		want    string
	}{
		{"sqlite3", "ON CONFLICT(`program`,`code_id`) DO UPDATE SET `name`=excluded.`name`,`age`=excluded.`age`"},
		{"mysql", "ON DUPLICATE KEY UPDATE `name`=VALUES(`name`),`age`=VALUES(`age`)"},
	}
	for _, tc := range cases {
		d, ok := GetDialect(tc.dialect)
		if !ok {
			t.Fatalf("dialect %s not registered", tc.dialect)
		}
		scope := &Scope{db: &DB{dialect: d}, Value: &savedDoc{}, Search: &search{}}
		q := func(s string) string { return d.Quote(s) }
		got, err := newBatchSave([]string{"program", "code_id"}).insertOption(scope, []string{q("program"), q("code_id"), q("name"), q("age")})
		if err != nil {
			t.Fatal(err)
		}
		want := tc.want
		if tc.dialect == "sqlite3" {
			want = strings.ReplaceAll(want, "`", `"`)
		}
		if got != want {
			t.Fatalf("%s:\n got %s\nwant %s", tc.dialect, got, want)
		}
	}

	// postgres quotes like sqlite; an unsupported dialect fails instead of
	// emitting SQL it cannot run.
	scope := &Scope{db: &DB{dialect: &commonDialect{}}, Value: &savedDoc{}, Search: &search{}}
	if _, err := newBatchSave([]string{"program"}).insertOption(scope, []string{`"program"`}); err == nil {
		t.Fatal("an unsupported dialect must be reported")
	}
}
