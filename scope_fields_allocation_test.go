package gorm

import (
	"reflect"
	"testing"
)

type ScopeFieldsAllocationEmbedded struct {
	EmbeddedValue string
}

type scopeFieldsAllocationModel struct {
	ID uint64 `gorm:"primary_key"`
	*ScopeFieldsAllocationEmbedded
	Field01 string
	Field02 string
	Field03 string
	Field04 string
	Field05 string
	Field06 string
	Field07 string
	Field08 string
	Field09 string
	Field10 string
	Field11 string
	Field12 string
	Field13 string
	Field14 string
	Field15 string
	Field16 string
	Field17 string
	Field18 string
	Field19 string
	Field20 string
	Field21 string
	Field22 string
	Field23 string
	Field24 string
	Field25 string
	Field26 string
	Field27 string
	Field28 string
	Field29 string
	Field30 string
	Field31 string
	Field32 string
}

func TestScopeFieldsUseStableIndependentStorage(t *testing.T) {
	model := &scopeFieldsAllocationModel{Field01: "first", Field02: "second"}
	scope := &Scope{Value: model}
	fields := scope.Fields()
	if len(fields) != len(scope.fieldStorage) {
		t.Fatalf("field count = %d, storage count = %d", len(fields), len(scope.fieldStorage))
	}
	if len(fields) < 3 {
		t.Fatalf("field count = %d, want at least 3", len(fields))
	}
	if fields[0] == fields[1] {
		t.Fatal("field wrappers alias each other")
	}
	for index, field := range fields {
		if field != &scope.fieldStorage[index] {
			t.Fatalf("field %d does not point into contiguous scope storage", index)
		}
	}

	again := scope.Fields()
	for index := range fields {
		if fields[index] != again[index] {
			t.Fatalf("field %d pointer changed across Fields calls", index)
		}
	}

	field01, ok := scope.FieldByName("Field01")
	if !ok {
		t.Fatal("Field01 not found")
	}
	field02, ok := scope.FieldByName("Field02")
	if !ok {
		t.Fatal("Field02 not found")
	}
	if err := field01.Set("updated"); err != nil {
		t.Fatalf("set Field01: %v", err)
	}
	if model.Field01 != "updated" || model.Field02 != "second" {
		t.Fatalf("unexpected model values: Field01=%q Field02=%q", model.Field01, model.Field02)
	}
	if field01.IsBlank || field02.IsBlank {
		t.Fatalf("unexpected blank state: Field01=%v Field02=%v", field01.IsBlank, field02.IsBlank)
	}
}

func TestScopeFieldsStillInitializesEmbeddedPointer(t *testing.T) {
	model := &scopeFieldsAllocationModel{}
	scope := &Scope{Value: model}
	field, ok := scope.FieldByName("EmbeddedValue")
	if !ok {
		t.Fatal("EmbeddedValue not found")
	}
	if model.ScopeFieldsAllocationEmbedded == nil {
		t.Fatal("embedded pointer was not initialized")
	}
	if err := field.Set("embedded"); err != nil {
		t.Fatalf("set embedded field: %v", err)
	}
	if model.EmbeddedValue != "embedded" {
		t.Fatalf("embedded value = %q", model.EmbeddedValue)
	}
}

func legacyScopeFieldsForBenchmark(scope *Scope) []*Field {
	var fields []*Field
	indirectScopeValue := scope.IndirectValue()
	isStruct := indirectScopeValue.Kind() == reflect.Struct
	for _, structField := range scope.GetModelStruct().StructFields {
		if isStruct {
			fieldValue := indirectScopeValue
			for _, name := range structField.Names {
				if fieldValue.Kind() == reflect.Ptr && fieldValue.IsNil() {
					fieldValue.Set(reflect.New(fieldValue.Type().Elem()))
				}
				fieldValue = reflect.Indirect(fieldValue).FieldByName(name)
			}
			fields = append(fields, &Field{StructField: structField, Field: fieldValue, IsBlank: isBlank(fieldValue)})
		} else {
			fields = append(fields, &Field{StructField: structField, IsBlank: true})
		}
	}
	return fields
}

var benchmarkScopeFields []*Field

func BenchmarkScopeFieldsAllocation(b *testing.B) {
	model := &scopeFieldsAllocationModel{
		ScopeFieldsAllocationEmbedded: &ScopeFieldsAllocationEmbedded{},
		Field01:                       "value",
	}
	(&Scope{Value: model}).GetModelStruct()

	b.Run("legacy-per-field-allocation", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkScopeFields = legacyScopeFieldsForBenchmark(&Scope{Value: model})
		}
	})
	b.Run("contiguous-scope-storage", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkScopeFields = (&Scope{Value: model}).Fields()
		}
	})
}
