package gorm

import (
	"database/sql"
	"reflect"
	"testing"
)

type scopeScanDuplicateColumns struct {
	FirstID  int64 `gorm:"column:id"`
	SecondID int64 `gorm:"column:id"`
	ThirdID  int64 `gorm:"column:id"`
}

func TestBuildScanPlanPreservesDuplicateColumnOrder(t *testing.T) {
	fields := (&Scope{Value: &scopeScanDuplicateColumns{}}).Fields()
	plan := buildScanPlan([]string{"id", "id", "missing", "id"}, fields)
	if want := []int{0, 1, -1, 2}; !reflect.DeepEqual(plan, want) {
		t.Fatalf("scan plan = %v, want %v", plan, want)
	}
}

type scopeScanNullRecord struct {
	Plain    string
	Pointer  *string
	Nullable sql.NullString
}

func TestScanWithPlanPreservesNullAndScannerSemantics(t *testing.T) {
	db, sqlDB := newScopeAllocationBenchmarkDB(t)
	defer sqlDB.Close()

	rows, err := sqlDB.Query(`
		SELECT NULL AS plain, NULL AS pointer, NULL AS nullable
		UNION ALL
		SELECT 'plain', 'pointer', 'nullable'
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}

	var (
		plan    []int
		records []scopeScanNullRecord
	)
	for rows.Next() {
		var record scopeScanNullRecord
		scope := db.NewScope(&record)
		fields := scope.Fields()
		if plan == nil {
			plan = buildScanPlan(columns, fields)
		}
		scope.scanWithPlan(rows, fields, plan)
		if scope.HasError() {
			t.Fatal(scope.db.Error)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2", len(records))
	}
	if records[0].Plain != "" || records[0].Pointer != nil || records[0].Nullable.Valid {
		t.Fatalf("NULL row changed zero values: %#v", records[0])
	}
	if records[1].Plain != "plain" || records[1].Pointer == nil || *records[1].Pointer != "pointer" {
		t.Fatalf("value row mismatch: %#v", records[1])
	}
	if !records[1].Nullable.Valid || records[1].Nullable.String != "nullable" {
		t.Fatalf("scanner value mismatch: %#v", records[1].Nullable)
	}
}

func legacyScanMetadataForBenchmark(columns []string, fields []*Field) int {
	plan := buildScanPlan(columns, fields)
	resetFields := map[int]*Field{}
	for index, fieldIndex := range plan {
		if fieldIndex >= 0 && fields[fieldIndex].Field.Kind() != reflect.Ptr {
			resetFields[index] = fields[fieldIndex]
		}
	}
	return len(resetFields)
}

func plannedScanMetadataForBenchmark(plan []int, fields []*Field) int {
	resetFields := make([]*Field, len(plan))
	count := 0
	for index, fieldIndex := range plan {
		if fieldIndex >= 0 && fields[fieldIndex].Field.Kind() != reflect.Ptr {
			resetFields[index] = fields[fieldIndex]
			count++
		}
	}
	benchmarkScanResetFields = resetFields
	return count
}

var (
	benchmarkScanResetFields []*Field
	benchmarkScanResetCount  int
)

func BenchmarkScopeScanMetadata400Rows(b *testing.B) {
	fields := (&Scope{Value: &scopeFieldsAllocationModel{
		ScopeFieldsAllocationEmbedded: &ScopeFieldsAllocationEmbedded{},
	}}).Fields()
	columns := make([]string, len(fields))
	for index, field := range fields {
		columns[index] = field.DBName
	}

	const rows = 400
	b.Run("legacy-plan-and-maps-per-row", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			count := 0
			for row := 0; row < rows; row++ {
				count += legacyScanMetadataForBenchmark(columns, fields)
			}
			benchmarkScanResetCount = count
		}
	})
	b.Run("reused-plan-and-reset-slice", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			plan := buildScanPlan(columns, fields)
			count := 0
			for row := 0; row < rows; row++ {
				count += plannedScanMetadataForBenchmark(plan, fields)
			}
			benchmarkScanResetCount = count
		}
	})
}
