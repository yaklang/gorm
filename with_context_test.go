package gorm

import (
	"context"
	"testing"
	"time"
)

func TestWithContext(t *testing.T) {
	sqlDB := setupBatchDB(t)
	gormDB, err := Open("sqlite3", sqlDB)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dbc := gormDB.WithContext(ctx)
	got := dbc.GetContext()

	if got == nil {
		t.Fatal("GetContext returned nil")
	}
	// Verify deadline is propagated
	_, ok := got.Deadline()
	if !ok {
		t.Fatal("deadline not propagated through WithContext")
	}

	// Verify default context when none set
	gotDefault := gormDB.GetContext()
	if gotDefault == nil {
		t.Fatal("default GetContext returned nil")
	}
}
