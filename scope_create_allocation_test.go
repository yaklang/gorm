package gorm

import (
	"database/sql"
	"testing"
)

func newScopeAllocationBenchmarkDB(tb testing.TB) (*DB, *sql.DB) {
	tb.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		tb.Fatal(err)
	}
	db, err := Open("common", sqlDB)
	if err != nil {
		sqlDB.Close()
		tb.Fatal(err)
	}
	return db, sqlDB
}

func TestAddToVarsCachesSkipBindVarWithoutHidingInstanceSet(t *testing.T) {
	db, sqlDB := newScopeAllocationBenchmarkDB(t)
	defer sqlDB.Close()

	scope := db.NewScope(&scopeFieldsAllocationModel{})
	if got := scope.AddToVars("before"); got != "$$$" {
		t.Fatalf("placeholder before InstanceSet = %q, want %q", got, "$$$")
	}
	if !scope.skipBindVarKnown || scope.skipBindVar {
		t.Fatalf("unexpected initial cache state: known=%v skip=%v", scope.skipBindVarKnown, scope.skipBindVar)
	}

	scope.InstanceSet("skip_bindvar", false)
	if got := scope.AddToVars("after"); got != "?" {
		t.Fatalf("placeholder after InstanceSet = %q, want ?", got)
	}
	if !scope.skipBindVarKnown || !scope.skipBindVar {
		t.Fatalf("unexpected updated cache state: known=%v skip=%v", scope.skipBindVarKnown, scope.skipBindVar)
	}

	preset := db.NewScope(&scopeFieldsAllocationModel{})
	preset.InstanceSet("skip_bindvar", true)
	if got := preset.AddToVars("preset"); got != "?" {
		t.Fatalf("preset placeholder = %q, want ?", got)
	}
}

func legacyAddToVarsForBenchmark(scope *Scope, value interface{}) string {
	_, skipBindVar := scope.InstanceGet("skip_bindvar")
	scope.SQLVars = append(scope.SQLVars, value)
	if skipBindVar {
		return "?"
	}
	return scope.Dialect().BindVar(len(scope.SQLVars))
}

var benchmarkScopePlaceholder string

func BenchmarkScopeAddToVarsRepeatedBindings(b *testing.B) {
	db, sqlDB := newScopeAllocationBenchmarkDB(b)
	defer sqlDB.Close()

	const bindings = 64
	b.Run("legacy-instance-lookup-per-binding", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			scope := db.NewScope(&scopeFieldsAllocationModel{})
			for binding := 0; binding < bindings; binding++ {
				benchmarkScopePlaceholder = legacyAddToVarsForBenchmark(scope, binding)
			}
		}
	})
	b.Run("cached-skip-bind-var-state", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			scope := db.NewScope(&scopeFieldsAllocationModel{})
			for binding := 0; binding < bindings; binding++ {
				benchmarkScopePlaceholder = scope.AddToVars(binding)
			}
		}
	})
}
