package gorm

import (
	"fmt"
	"strings"
	"testing"
)

func legacyCommonDialectQuote(key string) string {
	return fmt.Sprintf(`"%s"`, key)
}

func TestCommonDialectQuoteMatchesFormatting(t *testing.T) {
	for _, key := range []string{
		"",
		"id",
		"request",
		`already"quoted`,
		"space separated",
		"中文列名",
		"invalid-\xff",
		strings.Repeat("long-column-", 1024),
	} {
		if got, want := (commonDialect{}).Quote(key), legacyCommonDialectQuote(key); got != want {
			t.Fatalf("quote %q: got %q want %q", key, got, want)
		}
	}
}

func FuzzCommonDialectQuoteMatchesFormatting(f *testing.F) {
	for _, key := range []string{"id", `a"b`, "中文", "invalid-\xff", ""} {
		f.Add(key)
	}
	f.Fuzz(func(t *testing.T, key string) {
		if len(key) > 64*1024 {
			t.Skip()
		}
		if got, want := (commonDialect{}).Quote(key), legacyCommonDialectQuote(key); got != want {
			t.Fatalf("quote %q: got %q want %q", key, got, want)
		}
	})
}

var benchmarkCommonDialectQuote string

func BenchmarkCommonDialectQuote(b *testing.B) {
	for _, key := range []string{"id", "request", strings.Repeat("column-name-", 64)} {
		b.Run(key[:min(len(key), 16)], func(b *testing.B) {
			b.Run("fmt", func(b *testing.B) {
				b.ReportAllocs()
				for index := 0; index < b.N; index++ {
					benchmarkCommonDialectQuote = legacyCommonDialectQuote(key)
				}
			})
			b.Run("concat", func(b *testing.B) {
				b.ReportAllocs()
				dialect := commonDialect{}
				for index := 0; index < b.N; index++ {
					benchmarkCommonDialectQuote = dialect.Quote(key)
				}
			})
		})
	}
}
