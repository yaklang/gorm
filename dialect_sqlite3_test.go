package gorm

import "testing"

// The old probe looked for "INDEX <name> ON" inside the stored CREATE statement,
// so an index created with a quoted name was never found.
func TestSQLiteHasIndexMatchesQuotedName(t *testing.T) {
	sqlDB := setupBatchDB(t)
	defer sqlDB.Close()
	db, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if r := db.Exec(`CREATE UNIQUE INDEX "ux_batch_users_name" ON "batch_users" ("name")`); r.Error != nil {
		t.Fatal(r.Error)
	}
	if !db.Dialect().HasIndex("batch_users", "ux_batch_users_name") {
		t.Fatal("a quoted UNIQUE index must be visible to HasIndex")
	}
	if db.Dialect().HasIndex("batch_users", "ux_batch_users_nam") {
		t.Fatal("HasIndex must match the whole index name, not a prefix of it")
	}
	if db.Dialect().HasIndex("other_table", "ux_batch_users_name") {
		t.Fatal("HasIndex must look at the given table only")
	}
}
