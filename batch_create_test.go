package gorm

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type BatchUser struct {
	ID        int `gorm:"primary_key"`
	Name      string
	Age       int
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (u *BatchUser) TableName() string { return "batch_users" }

func setupBatchDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE batch_users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT,
		age INTEGER,
		created_at DATETIME,
		updated_at DATETIME
	)`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestCreateInBatches_StructPtrSlice(t *testing.T) {
	sqlDB := setupBatchDB(t)
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	users := []*BatchUser{
		{Name: "alice", Age: 30},
		{Name: "bob", Age: 25},
		{Name: "charlie", Age: 35},
	}

	result := gormDB.CreateInBatches(users, 2)
	if result.Error != nil {
		t.Fatalf("CreateInBatches failed: %v", result.Error)
	}
	if result.RowsAffected != 3 {
		t.Fatalf("expected 3 rows affected, got %d", result.RowsAffected)
	}

	// Verify IDs were set.
	for i, u := range users {
		if u.ID == 0 {
			t.Fatalf("user %d (%s) ID not set", i, u.Name)
		}
	}

	// Verify data.
	var count int
	gormDB.Model(&BatchUser{}).Count(&count)
	if count != 3 {
		t.Fatalf("expected 3 records, got %d", count)
	}

	var names []string
	gormDB.Model(&BatchUser{}).Pluck("name", &names)
	if len(names) != 3 || names[0] != "alice" || names[1] != "bob" || names[2] != "charlie" {
		t.Fatalf("unexpected names: %v", names)
	}

	// Verify timestamps were set.
	for _, u := range users {
		if u.CreatedAt.IsZero() {
			t.Fatalf("CreatedAt not set for %s", u.Name)
		}
	}
}

func TestCreateInBatches_StructSlice(t *testing.T) {
	sqlDB := setupBatchDB(t)
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	users := []BatchUser{
		{Name: "dave", Age: 40},
		{Name: "eve", Age: 28},
	}

	result := gormDB.CreateInBatches(&users, 10)
	if result.Error != nil {
		t.Fatalf("CreateInBatches failed: %v", result.Error)
	}
	if result.RowsAffected != 2 {
		t.Fatalf("expected 2 rows affected, got %d", result.RowsAffected)
	}

	var count int
	gormDB.Model(&BatchUser{}).Count(&count)
	if count != 2 {
		t.Fatalf("expected 2 records, got %d", count)
	}
}

func TestCreateInBatches_LargeBatch(t *testing.T) {
	sqlDB := setupBatchDB(t)
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	var users []*BatchUser
	for i := 0; i < 500; i++ {
		users = append(users, &BatchUser{
			Name: fmt.Sprintf("user_%d", i),
			Age:  i,
		})
	}

	result := gormDB.CreateInBatches(users, 100)
	if result.Error != nil {
		t.Fatalf("CreateInBatches failed: %v", result.Error)
	}
	if result.RowsAffected != 500 {
		t.Fatalf("expected 500 rows affected, got %d", result.RowsAffected)
	}

	var count int
	gormDB.Model(&BatchUser{}).Count(&count)
	if count != 500 {
		t.Fatalf("expected 500 records, got %d", count)
	}
}

func TestCreateInBatches_Empty(t *testing.T) {
	sqlDB := setupBatchDB(t)
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	result := gormDB.CreateInBatches([]*BatchUser{}, 100)
	if result.Error != nil {
		t.Fatalf("CreateInBatches empty slice should not error: %v", result.Error)
	}
}

func TestCreateInBatches_Hooks(t *testing.T) {
	sqlDB := setupBatchDB(t)
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	users := []*BatchUser{
		{Name: "hook1", Age: 1},
		{Name: "hook2", Age: 2},
	}

	result := gormDB.CreateInBatches(users, 10)
	if result.Error != nil {
		t.Fatalf("CreateInBatches with hooks failed: %v", result.Error)
	}

	// Verify hooks ran by checking timestamps (set by updateTimeStampForCreateCallback).
	for _, u := range users {
		if u.CreatedAt.IsZero() {
			t.Fatalf("BeforeCreate timestamp hook didn't run for %s", u.Name)
		}
	}
}
