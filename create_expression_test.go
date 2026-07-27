package gorm

import (
	"database/sql"
	"testing"
	"time"
)

type createExpressionRecord struct {
	ID        int `gorm:"primary_key"`
	CreatedAt time.Time
	Payload   string
	Plain     string
}

func (record *createExpressionRecord) TableName() string {
	return "create_expression_records"
}

func setupCreateExpressionDB(t testing.TB) (*DB, *sql.DB) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_, err = sqlDB.Exec(`CREATE TABLE create_expression_records (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME,
		payload TEXT,
		plain TEXT
	)`)
	if err != nil {
		sqlDB.Close()
		t.Fatal(err)
	}
	db, err := Open("sqlite3", sqlDB)
	if err != nil {
		sqlDB.Close()
		t.Fatal(err)
	}
	return db, sqlDB
}

func TestCreateWithColumnExpressions(t *testing.T) {
	db, sqlDB := setupCreateExpressionDB(t)
	defer sqlDB.Close()

	record := &createExpressionRecord{Payload: "model-value", Plain: "plain-value"}
	result := db.CreateWithColumnExpressions(record, map[string]*SqlExpr{
		"payload": Expr("CAST(? AS TEXT)", []byte("expression-value")),
	})
	if result.Error != nil {
		t.Fatalf("create: %v", result.Error)
	}
	if record.ID == 0 {
		t.Fatal("primary key was not populated")
	}
	if record.CreatedAt.IsZero() {
		t.Fatal("created_at was not populated")
	}

	var payload, plain, payloadType string
	err := sqlDB.QueryRow(
		"SELECT payload, plain, typeof(payload) FROM create_expression_records WHERE id = ?",
		record.ID,
	).Scan(&payload, &plain, &payloadType)
	if err != nil {
		t.Fatal(err)
	}
	if payload != "expression-value" {
		t.Fatalf("payload = %q", payload)
	}
	if plain != "plain-value" {
		t.Fatalf("plain = %q", plain)
	}
	if payloadType != "text" {
		t.Fatalf("typeof(payload) = %q", payloadType)
	}
}

func TestCreateWithColumnExpressionsIgnoresNilExpression(t *testing.T) {
	db, sqlDB := setupCreateExpressionDB(t)
	defer sqlDB.Close()

	record := &createExpressionRecord{Payload: "model-value", Plain: "plain-value"}
	result := db.CreateWithColumnExpressions(record, map[string]*SqlExpr{"payload": nil})
	if result.Error != nil {
		t.Fatalf("create: %v", result.Error)
	}

	var payload string
	if err := sqlDB.QueryRow("SELECT payload FROM create_expression_records WHERE id = ?", record.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != "model-value" {
		t.Fatalf("payload = %q", payload)
	}
}
