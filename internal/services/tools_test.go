package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestToolDefaultToolsRegistered(t *testing.T) {
	tools := DefaultTools()
	if len(tools) == 0 {
		t.Fatal("no default tools")
	}
	seen := map[string]bool{}
	for _, tl := range tools {
		if tl.Name == "" || tl.Description == "" || tl.Handler == nil {
			t.Errorf("incomplete tool %+v", tl)
		}
		if seen[tl.Name] {
			t.Errorf("duplicate tool %q", tl.Name)
		}
		seen[tl.Name] = true
	}
	if !seen["get_current_time"] {
		t.Error("get_current_time missing")
	}
}

func TestToolGetCurrentTime(t *testing.T) {
	fixed := time.Date(2026, 3, 1, 12, 30, 0, 0, time.UTC)
	tool := toolCurrentTime(func() time.Time { return fixed })

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"empty input", ``, "2026-03-01T12:30:00Z", false},
		{"null input", `null`, "2026-03-01T12:30:00Z", false},
		{"empty object", `{}`, "2026-03-01T12:30:00Z", false},
		{"blank timezone", `{"timezone":"  "}`, "2026-03-01T12:30:00Z", false},
		{"utc", `{"timezone":"UTC"}`, "2026-03-01T12:30:00Z", false},
		{"tokyo", `{"timezone":"Asia/Tokyo"}`, "2026-03-01T21:30:00+09:00", false},
		{"bad timezone", `{"timezone":"Mars/Olympus"}`, "", true},
		{"local rejected", `{"timezone":"Local"}`, "", true},
		{"bad json", `{"timezone":`, "", true},
		{"wrong type", `{"timezone":5}`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tool.Handler(context.Background(), json.RawMessage(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolGetCurrentTimeRealClock(t *testing.T) {
	out, err := DefaultTools()[0].Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339, out); err != nil {
		t.Errorf("not RFC3339: %q", out)
	}
}
