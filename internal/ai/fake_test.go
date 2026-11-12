package ai

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestFake(t *testing.T) {
	upper := Tool{
		Name: "shout",
		Handler: func(_ context.Context, in json.RawMessage) (string, error) {
			var v struct{ Text string }
			if err := json.Unmarshal(in, &v); err != nil {
				return "", err
			}
			return v.Text + "!", nil
		},
	}
	failing := Tool{
		Name:    "broken",
		Handler: func(context.Context, json.RawMessage) (string, error) { return "", errors.New("boom") },
	}
	tools := []Tool{upper, failing}

	tests := []struct {
		name      string
		content   string
		wantText  string
		wantCalls int
		wantErr   bool
	}{
		{"echo", "hello", "echo: hello", 0, false},
		{"tool", `/tool shout {"text":"hi"}`, "tool shout returned: hi!", 1, false},
		{"tool error", `/tool broken {}`, "tool broken returned: boom", 1, false},
		{"tool no input", `/tool broken`, "tool broken returned: boom", 1, false},
		{"unknown tool echoes", `/tool nope {}`, "echo: /tool nope {}", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := NewFake().Run(context.Background(), Request{
				Messages: []Message{{Role: RoleUser, Content: tt.content}},
				Tools:    tools,
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.Text != tt.wantText {
				t.Errorf("text = %q, want %q", res.Text, tt.wantText)
			}
			if len(res.ToolCalls) != tt.wantCalls {
				t.Errorf("tool calls = %d, want %d", len(res.ToolCalls), tt.wantCalls)
			}
			if res.Model != "fake" || res.StopReason != StopEndTurn {
				t.Errorf("model/stop = %q/%q", res.Model, res.StopReason)
			}
		})
	}
}

func TestFakeValidation(t *testing.T) {
	bad := []Request{
		{},
		{Messages: []Message{{Role: RoleAssistant, Content: "hi"}}},
		{Messages: []Message{{Role: RoleUser, Content: "  "}}},
	}
	for i, req := range bad {
		if _, err := NewFake().Run(context.Background(), req); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestFakeStreaming(t *testing.T) {
	shout := Tool{Name: "shout", Handler: func(_ context.Context, in json.RawMessage) (string, error) { return "HI THERE", nil }}
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"echo", "hello  big world", []string{
			"text_delta:echo: ", "text_delta:hello ", "text_delta: ", "text_delta:big ", "text_delta:world"}},
		{"tool", `/tool shout {}`, []string{
			"tool_call:shout", "tool_result:shout",
			"text_delta:tool ", "text_delta:shout ", "text_delta:returned: ", "text_delta:HI ", "text_delta:THERE"}},
		{"invalid json", `/tool shout {bad`, []string{
			"tool_call:shout", "tool_result:shout",
			"text_delta:tool ", "text_delta:shout ", "text_delta:returned: ", "text_delta:invalid ", "text_delta:JSON ", "text_delta:input"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rec recorder
			res, err := NewFake().Run(context.Background(), Request{
				Messages: []Message{{Role: RoleUser, Content: tt.content}},
				Tools:    []Tool{shout},
				OnEvent:  rec.on,
			})
			if err != nil {
				t.Fatal(err)
			}
			got := rec.kinds()
			if len(got) != len(tt.want) {
				t.Fatalf("events = %q, want %q", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("event %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
			var text string
			for _, e := range rec.events {
				if e.Type == EventTextDelta {
					text += e.Text
				}
			}
			if text != res.Text {
				t.Errorf("deltas %q != text %q", text, res.Text)
			}
		})
	}
}
