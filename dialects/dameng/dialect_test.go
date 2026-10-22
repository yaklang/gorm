package dameng

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erikstmartin/go-testdb"
	"github.com/yaklang/gorm"
)

func TestIdentifierCaseAndTypeMetadata(t *testing.T) {
	d := &dmDialect{}
	if got := d.Quote(`Mi"XeD`); got != `"Mi""XeD"` {
		t.Fatalf("identifier escaped incorrectly: %s", got)
	}
	type row struct {
		ID          uint
		ProgramName string
		OtherName   string
		Short       string `gorm:"size:64"`
		Body        string `gorm:"type:text"`
		JSON        string `gorm:"type:jsonb"`
		Enabled     bool
		Data        []byte
		At          time.Time
	}
	scope := (&gorm.Scope{Value: &row{}})
	fields := scope.GetModelStruct().StructFields
	want := map[string]string{"id": "BIGINT IDENTITY(1,1)", "program_name": "VARCHAR(255)", "other_name": "VARCHAR(255)", "short": "VARCHAR(64)", "body": "CLOB", "json": "CLOB", "enabled": "BIT", "data": "BLOB", "at": "TIMESTAMP(6)"}
	for _, field := range fields {
		if got := d.DataTypeOf(field); got != want[field.DBName] {
			t.Errorf("%s: got %q want %q", field.DBName, got, want[field.DBName])
		}
	}
	if index, column := d.NormalizeIndexAndColumn("idx_mixed", "MiXeD"); index != `"idx_mixed"` || column != "MiXeD" {
		t.Fatalf("normalization lost quoting policy: %q %q", index, column)
	}
}

func TestMetadataAndModifyColumnRetainQuotedNames(t *testing.T) {
	defer testdb.Reset()
	db, err := sql.Open("testdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := &dmDialect{db: db}
	var got []driver.Value
	testdb.SetQueryWithArgsFunc(func(query string, args []driver.Value) (driver.Rows, error) {
		if strings.Contains(query, "CURRENT_SCHEMA") {
			return testdb.RowsFromCSVString([]string{"schema"}, "CURRENT_OWNER"), nil
		}
		got = args
		return testdb.RowsFromCSVString([]string{"count"}, "1"), nil
	})
	if !d.HasTable("Owner.lower_table") || !reflect.DeepEqual(got, []driver.Value{"Owner", "lower_table"}) {
		t.Fatalf("qualified table metadata: %v", got)
	}
	if !d.HasColumn("lower_table", "MiXeD") || !reflect.DeepEqual(got, []driver.Value{"CURRENT_OWNER", "lower_table", "MiXeD"}) {
		t.Fatalf("column metadata: %v", got)
	}
	if !d.HasIndex("lower_table", "idx_unquoted") || got[2] != "IDX_UNQUOTED" {
		t.Fatalf("unquoted index metadata: %v", got)
	}
	if !d.HasIndex("lower_table", `"idx_quoted"`) || got[2] != "idx_quoted" {
		t.Fatalf("quoted index metadata: %v", got)
	}
	var statement string
	testdb.SetExecFunc(func(query string) (driver.Result, error) { statement = query; return driver.RowsAffected(0), nil })
	if err := d.ModifyColumn(`"Owner"."lower_table"`, `"MiXeD"`, "VARCHAR(64)"); err != nil {
		t.Fatal(err)
	}
	if want := `ALTER TABLE "Owner"."lower_table" MODIFY "MiXeD" VARCHAR(64)`; statement != want {
		t.Fatalf("double quoted operands: %s", statement)
	}
	if err := d.RemoveIndex("lower_table", "idx_unquoted"); err != nil {
		t.Fatal(err)
	}
	if statement != `DROP INDEX "CURRENT_OWNER"."IDX_UNQUOTED"` {
		t.Fatalf("wrong index removal: %s", statement)
	}
}

func TestMergeSQLPreservesKeysAndBoundValues(t *testing.T) {
	d := &dmDialect{}
	query, err := d.BatchSaveSQL(`"records"`, []string{`"key"`, `"body"`, `"created_at"`}, []string{`"key"`}, []string{`"body"`}, [][]string{{"?", "?", "?"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(query, `MERGE INTO "records"`) || !strings.Contains(query, `ON ("gorm_target"."key"="gorm_source"."key")`) || !strings.Contains(query, `UPDATE SET "body"="gorm_source"."body" WHEN NOT MATCHED`) {
		t.Fatalf("unexpected merge: %s", query)
	}
	if strings.Contains(query, "DELETE") || strings.Contains(query, `UPDATE SET "created_at"`) || strings.Count(query, "?") != 3 {
		t.Fatalf("merge changed row identity or bindings: %s", query)
	}
	if _, err := d.BatchSaveSQL(`"records"`, []string{`"body"`}, []string{`"missing"`}, nil, [][]string{{"?"}}); err == nil {
		t.Fatal("missing conflict target accepted")
	}
}

func TestConnectionErrorsAreRedactedAndUnwrap(t *testing.T) {
	err := &connectionError{cause: context.Canceled}
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "password") {
		t.Fatalf("bad public error: %s", err)
	}
	for _, raw := range []string{"sqlite:///private", "dm://user:secret@host?schema=bad%20schema", "dm://%zz"} {
		db, err := OpenDB(raw)
		if db != nil || err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("invalid URL should produce redacted error")
		}
	}
}
