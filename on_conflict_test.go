package gorm

import (
	"testing"
)

func TestOnConflictDoNothing(t *testing.T) {
	sqlDB := setupBatchDB(t)
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	u1 := &BatchUser{ID: 1, Name: "alice", Age: 30}
	if r := gormDB.Create(u1); r.Error != nil {
		t.Fatal(r.Error)
	}

	// Verify first insert
	var count int
	gormDB.Model(&BatchUser{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 record after first insert, got %d", count)
	}

	u2 := &BatchUser{ID: 1, Name: "bob", Age: 25}
	r := gormDB.OnConflictDoNothing("id").Create(u2)
	if r.Error != nil {
		t.Fatalf("OnConflictDoNothing failed: %v", r.Error)
	}

	// Should still have 1 record (DO NOTHING)
	gormDB.Model(&BatchUser{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 record after DO NOTHING, got %d", count)
	}

	var u BatchUser
	if err := gormDB.Model(&BatchUser{}).Where("id = ?", 1).First(&u).Error; err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if u.Name != "alice" {
		t.Fatalf("expected alice, got %s", u.Name)
	}
}

func TestOnConflictDoUpdate(t *testing.T) {
	sqlDB := setupBatchDB(t)
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	u1 := &BatchUser{ID: 1, Name: "alice", Age: 30}
	if r := gormDB.Create(u1); r.Error != nil {
		t.Fatal(r.Error)
	}

	u2 := &BatchUser{ID: 1, Name: "bob", Age: 25}
	r := gormDB.OnConflictDoUpdate("id", "name", "age").Create(u2)
	if r.Error != nil {
		t.Fatalf("OnConflictDoUpdate failed: %v", r.Error)
	}

	var u BatchUser
	gormDB.Model(&BatchUser{}).Where("id = ?", 1).First(&u)
	if u.Name != "bob" || u.Age != 25 {
		t.Fatalf("expected bob/25, got %s/%d", u.Name, u.Age)
	}
}

func TestCreateInBatchesOnConflict(t *testing.T) {
	sqlDB := setupBatchDB(t)
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	users := []*BatchUser{
		{ID: 1, Name: "a1", Age: 10},
		{ID: 2, Name: "b1", Age: 20},
	}
	if r := gormDB.CreateInBatches(users, 10); r.Error != nil {
		t.Fatal(r.Error)
	}

	upsert := []*BatchUser{
		{ID: 1, Name: "a2", Age: 11},
		{ID: 3, Name: "c1", Age: 30},
	}
	r := gormDB.CreateInBatchesOnConflict(upsert, 10, "ON CONFLICT(id) DO UPDATE SET name=excluded.name, age=excluded.age")
	if r.Error != nil {
		t.Fatalf("CreateInBatchesOnConflict failed: %v", r.Error)
	}

	var u1 BatchUser
	gormDB.Model(&BatchUser{}).Where("id = ?", 1).First(&u1)
	if u1.Name != "a2" || u1.Age != 11 {
		t.Fatalf("expected a2/11, got %s/%d", u1.Name, u1.Age)
	}

	var count int
	gormDB.Model(&BatchUser{}).Count(&count)
	if count != 3 {
		t.Fatalf("expected 3 records, got %d", count)
	}
}

func TestUpdateInBatches_UpdatesAndInsertsInOneCall(t *testing.T) {
	sqlDB := setupBatchDB(t)
	defer sqlDB.Close()
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	if r := gormDB.CreateInBatches([]*BatchUser{
		{ID: 1, Name: "a1", Age: 10},
		{ID: 2, Name: "b1", Age: 20},
	}, 10); r.Error != nil {
		t.Fatal(r.Error)
	}

	// One call must overwrite existing rows and insert new ones.
	r := gormDB.UpdateInBatches([]*BatchUser{
		{ID: 1, Name: "a2", Age: 11},
		{ID: 3, Name: "c1", Age: 30},
	}, 10, "id")
	if r.Error != nil {
		t.Fatalf("UpdateInBatches failed: %v", r.Error)
	}

	var u1 BatchUser
	if err := gormDB.Model(&BatchUser{}).Where("id = ?", 1).First(&u1).Error; err != nil {
		t.Fatalf("query id=1: %v", err)
	}
	if u1.Name != "a2" || u1.Age != 11 {
		t.Fatalf("expected a2/11, got %s/%d", u1.Name, u1.Age)
	}
	var u3 BatchUser
	if err := gormDB.Model(&BatchUser{}).Where("id = ?", 3).First(&u3).Error; err != nil {
		t.Fatalf("inserted row missing: %v", err)
	}
	var count int
	gormDB.Model(&BatchUser{}).Count(&count)
	if count != 3 {
		t.Fatalf("expected 3 records, got %d", count)
	}
}

func TestUpdateInBatches_ZeroValuesOverwrite(t *testing.T) {
	sqlDB := setupBatchDB(t)
	defer sqlDB.Close()
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	if r := gormDB.Create(&BatchUser{ID: 1, Name: "old", Age: 55}); r.Error != nil {
		t.Fatal(r.Error)
	}
	if r := gormDB.UpdateInBatches([]*BatchUser{{ID: 1}}, 10, "id"); r.Error != nil {
		t.Fatalf("UpdateInBatches failed: %v", r.Error)
	}

	var u BatchUser
	if err := gormDB.Model(&BatchUser{}).Where("id = ?", 1).First(&u).Error; err != nil {
		t.Fatal(err)
	}
	if u.Name != "" || u.Age != 0 {
		t.Fatalf("zero values must overwrite the snapshot, got %q/%d", u.Name, u.Age)
	}
}

func TestUpdateInBatches_RejectedStatementPreservesPreviousSnapshot(t *testing.T) {
	sqlDB := setupBatchDB(t)
	defer sqlDB.Close()
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	if r := gormDB.Create(&BatchUser{ID: 7, Name: "old", Age: 1}); r.Error != nil {
		t.Fatal(r.Error)
	}
	if r := gormDB.Exec(`CREATE TRIGGER reject_user_update BEFORE UPDATE ON batch_users
		WHEN NEW.name = 'reject' BEGIN SELECT RAISE(ABORT, 'rejected user'); END`); r.Error != nil {
		t.Fatal(r.Error)
	}

	r := gormDB.UpdateInBatches([]*BatchUser{{ID: 7, Name: "reject", Age: 2}}, 10, "id")
	if r.Error == nil {
		t.Fatal("UpdateInBatches unexpectedly succeeded")
	}

	var u BatchUser
	if err := gormDB.Model(&BatchUser{}).Where("id = ?", 7).First(&u).Error; err != nil {
		t.Fatalf("a failed update must preserve the previous row: %v", err)
	}
	if u.Name != "old" || u.Age != 1 {
		t.Fatalf("expected old/1, got %s/%d", u.Name, u.Age)
	}
}

func TestUpdateInBatches_SpansChunks(t *testing.T) {
	sqlDB := setupBatchDB(t)
	defer sqlDB.Close()
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	const rows = 25
	first := make([]*BatchUser, rows)
	for i := range first {
		first[i] = &BatchUser{ID: i + 1, Name: "before", Age: i}
	}
	if r := gormDB.CreateInBatches(first, 10); r.Error != nil {
		t.Fatal(r.Error)
	}

	second := make([]*BatchUser, rows)
	for i := range second {
		second[i] = &BatchUser{ID: i + 1, Name: "after", Age: i + 100}
	}
	if r := gormDB.UpdateInBatches(second, 4, "id"); r.Error != nil {
		t.Fatalf("UpdateInBatches failed: %v", r.Error)
	}

	var stale int
	gormDB.Model(&BatchUser{}).Where("name <> ?", "after").Count(&stale)
	if stale != 0 {
		t.Fatalf("%d rows were not updated across chunks", stale)
	}
}

func TestSQLiteHasIndexMatchesQuotedName(t *testing.T) {
	sqlDB := setupBatchDB(t)
	defer sqlDB.Close()
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if r := gormDB.Exec(`CREATE UNIQUE INDEX "ux_batch_users_name" ON "batch_users" ("name")`); r.Error != nil {
		t.Fatal(r.Error)
	}
	if !gormDB.Dialect().HasIndex("batch_users", "ux_batch_users_name") {
		t.Fatal("quoted UNIQUE index must be visible to HasIndex")
	}
	if gormDB.Dialect().HasIndex("batch_users", "missing") {
		t.Fatal("HasIndex reported an index that does not exist")
	}
}

func TestUpdateInBatches_RequiresConflictColumns(t *testing.T) {
	sqlDB := setupBatchDB(t)
	defer sqlDB.Close()
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if r := gormDB.UpdateInBatches([]*BatchUser{{ID: 1, Name: "x"}}, 10, ""); r.Error == nil {
		t.Fatal("UpdateInBatches with no conflict columns must fail loudly")
	}
}

// batchDoc mirrors the SSA IR shape: a surrogate primary key, a natural unique
// key, and a soft-delete column. SSA resets ID/DeletedAt before writing a
// snapshot, so the upsert must match on the natural key and restore the row.
type batchDoc struct {
	Model
	CodeID  int64  `gorm:"unique_index:ux_batch_docs_code"`
	Program string `gorm:"unique_index:ux_batch_docs_code"`
	Name    string
	Age     int
}

func (d *batchDoc) TableName() string { return "batch_docs" }

func TestUpdateInBatches_NaturalKeyUpsertRestoresSoftDeletedRow(t *testing.T) {
	sqlDB := setupBatchDB(t)
	defer sqlDB.Close()
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if r := gormDB.AutoMigrate(&batchDoc{}); r.Error != nil {
		t.Fatal(r.Error)
	}

	doc := &batchDoc{CodeID: 7, Program: "prog", Name: "old", Age: 1}
	if r := gormDB.Create(doc); r.Error != nil {
		t.Fatal(r.Error)
	}
	originalID := doc.ID
	if r := gormDB.Delete(doc); r.Error != nil {
		t.Fatal(r.Error)
	}

	// SSA clears ID/CreatedAt/UpdatedAt/DeletedAt before writing a snapshot.
	fresh := &batchDoc{CodeID: 7, Program: "prog", Name: "new", Age: 2}
	if r := gormDB.UpdateInBatches([]*batchDoc{fresh}, 10, "program,code_id"); r.Error != nil {
		t.Fatalf("UpdateInBatches failed: %v", r.Error)
	}

	var got batchDoc
	if err := gormDB.Model(&batchDoc{}).Where("program = ? AND code_id = ?", "prog", 7).First(&got).Error; err != nil {
		t.Fatalf("row must be restored: %v", err)
	}
	if got.ID != originalID {
		t.Fatalf("soft-deleted row must be restored in place: id=%d want %d", got.ID, originalID)
	}
	if got.Name != "new" || got.Age != 2 {
		t.Fatalf("expected new/2, got %s/%d", got.Name, got.Age)
	}
	if got.DeletedAt != nil {
		t.Fatal("restored row must clear deleted_at")
	}
	var count int
	gormDB.Model(&batchDoc{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 row, got %d", count)
	}
}
