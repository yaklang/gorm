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
