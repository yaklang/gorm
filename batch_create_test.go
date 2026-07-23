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

type BatchDefaultUser struct {
	ID   int    `gorm:"primary_key"`
	Name string `gorm:"default:'anonymous'"`
}

func (u *BatchDefaultUser) TableName() string { return "batch_default_users" }

func setupBatchDB(t testing.TB) *sql.DB {
	t.Helper()
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

func TestCreateInBatches_MixedPrimaryKeyShapes(t *testing.T) {
	sqlDB := setupBatchDB(t)
	defer sqlDB.Close()
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	users := []*BatchUser{
		{Name: "generated-before"},
		{ID: 42, Name: "explicit"},
		{Name: "generated-after"},
	}
	if result := gormDB.CreateInBatches(users, 10); result.Error != nil {
		t.Fatalf("CreateInBatches failed: %v", result.Error)
	}
	wantIDs := []int{1, 42, 43}
	for i, user := range users {
		if user.ID != wantIDs[i] {
			t.Fatalf("user %d ID = %d, want %d", i, user.ID, wantIDs[i])
		}
	}
}

func TestCreateInBatches_NilElementRollsBack(t *testing.T) {
	sqlDB := setupBatchDB(t)
	defer sqlDB.Close()
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	users := []*BatchUser{{Name: "must-not-persist"}, nil}
	if result := gormDB.CreateInBatches(users, 10); result.Error == nil {
		t.Fatal("CreateInBatches with a nil element unexpectedly succeeded")
	}
	var count int
	if err := gormDB.Model(&BatchUser{}).Count(&count).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0 after rollback", count)
	}
}

func TestMaxCreateBatchRowsHonorsBindLimits(t *testing.T) {
	tests := []struct {
		name        string
		dialect     string
		columns     int
		requested   int
		wantMaxRows int
	}{
		{name: "sqlite wide model", dialect: "sqlite3", columns: 100, requested: 500, wantMaxRows: 327},
		{name: "sqlite narrow model", dialect: "sqlite3", columns: 10, requested: 500, wantMaxRows: 500},
		{name: "mssql", dialect: "mssql", columns: 30, requested: 500, wantMaxRows: 70},
		{name: "unknown dialect", dialect: "custom", columns: 100, requested: 500, wantMaxRows: 500},
		{name: "default values", dialect: "sqlite3", columns: 0, requested: 500, wantMaxRows: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := maxCreateBatchRows(test.dialect, test.columns, test.requested); got != test.wantMaxRows {
				t.Fatalf("maxCreateBatchRows(%q, %d, %d) = %d, want %d", test.dialect, test.columns, test.requested, got, test.wantMaxRows)
			}
		})
	}
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

	// Verify IDs match the actual SQLite row IDs, not just that they are non-zero.
	for i, u := range users {
		wantID := i + 1
		if u.ID != wantID {
			t.Fatalf("user %d (%s) ID = %d, want %d", i, u.Name, u.ID, wantID)
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

	result := gormDB.CreateInBatches([]*BatchUser{})
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

func TestCreateInBatches_DefaultSize(t *testing.T) {
	sqlDB := setupBatchDB(t)
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	// Use default batchSize (no second arg)
	users := make([]*BatchUser, 0, 1200)
	for i := 0; i < 1200; i++ {
		users = append(users, &BatchUser{Name: fmt.Sprintf("default_%d", i), Age: i})
	}
	result := gormDB.CreateInBatches(users)
	if result.Error != nil {
		t.Fatalf("CreateInBatches (default size) failed: %v", result.Error)
	}
	if result.RowsAffected != 1200 {
		t.Fatalf("expected 1200 rows affected, got %d", result.RowsAffected)
	}

	var count int
	gormDB.Model(&BatchUser{}).Count(&count)
	if count != 1200 {
		t.Fatalf("expected 1200 records, got %d", count)
	}
}

func TestCreateInBatches_PreservesDatabaseDefaults(t *testing.T) {
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	_, err = sqlDB.Exec(`CREATE TABLE batch_default_users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL DEFAULT 'anonymous'
	)`)
	if err != nil {
		t.Fatal(err)
	}

	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	users := []*BatchDefaultUser{{}, {Name: "explicit"}}
	result := gormDB.CreateInBatches(users, 10)
	if result.Error != nil {
		t.Fatalf("CreateInBatches failed: %v", result.Error)
	}

	var names []string
	if err := gormDB.Model(&BatchDefaultUser{}).Order("id ASC").Pluck("name", &names).Error; err != nil {
		t.Fatalf("query names: %v", err)
	}
	want := []string{"anonymous", "explicit"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
}

func BenchmarkCreateRows(b *testing.B) {
	const rowCount = 500
	for _, benchmark := range []struct {
		name  string
		batch bool
	}{
		{name: "CreateOneByOne", batch: false},
		{name: "CreateInBatches", batch: true},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			b.StopTimer()
			sqlDB := setupBatchDB(b)
			defer sqlDB.Close()
			gormDB, err := Open("sqlite3", sqlDB)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ReportMetric(rowCount, "rows/op")

			for iteration := 0; iteration < b.N; iteration++ {
				users := make([]*BatchUser, rowCount)
				for index := range users {
					users[index] = &BatchUser{Name: fmt.Sprintf("user-%d-%d", iteration, index), Age: index}
				}

				b.StartTimer()
				if benchmark.batch {
					if result := gormDB.CreateInBatches(users); result.Error != nil {
						b.Fatal(result.Error)
					}
				} else {
					for _, user := range users {
						if result := gormDB.Create(user); result.Error != nil {
							b.Fatal(result.Error)
						}
					}
				}
				b.StopTimer()
			}
		})
	}
}
