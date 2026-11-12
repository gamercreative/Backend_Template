package queries

import (
	"reflect"
	"strings"
	"testing"
	"unicode"
)

// snakeCase turns a method name into its sql file stem, e.g. ListRecentMessages
// becomes list_recent_messages.
func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func TestSnakeCase(t *testing.T) {
	tests := map[string]string{
		"Ping":               "ping",
		"CreateAgent":        "create_agent",
		"ListRecentMessages": "list_recent_messages",
		"FinishRun":          "finish_run",
	}
	for in, want := range tests {
		if got := snakeCase(in); got != want {
			t.Errorf("snakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEveryQuerierMethodHasEmbeddedSQL(t *testing.T) {
	iface := reflect.TypeOf((*Querier)(nil)).Elem()
	methods := map[string]bool{}
	for i := 0; i < iface.NumMethod(); i++ {
		name := iface.Method(i).Name
		if name == "InTx" {
			continue // InTx has no SQL of its own.
		}
		stem := snakeCase(name)
		methods[stem] = true
		sql, ok := sqlFiles[stem]
		if !ok {
			t.Errorf("method %s: missing internal/queries/sql/%s.sql", name, stem)
			continue
		}
		if strings.TrimSpace(sql) == "" {
			t.Errorf("method %s: sql/%s.sql is empty", name, stem)
		}
		if strings.HasSuffix(sql, ";") {
			t.Errorf("sql/%s.sql must not end with a semicolon", stem)
		}
	}
	for stem := range sqlFiles {
		if !methods[stem] {
			t.Errorf("sql/%s.sql does not match any Querier method", stem)
		}
	}
}
