package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"alphahack/internal/ai"
)

// DefaultTools returns the tools every agent can call. Add new tools here.
func DefaultTools() []ai.Tool {
	return []ai.Tool{
		toolCurrentTime(time.Now),
	}
}

// toolCurrentTime reports the current time in an IANA timezone (default UTC).
// now is injectable so tests are deterministic.
func toolCurrentTime(now func() time.Time) ai.Tool {
	return ai.Tool{
		Name:        "get_current_time",
		Description: "Get the current date and time as an RFC3339 timestamp. Optionally pass an IANA timezone name such as \"Europe/Paris\"; the default is UTC.",
		Properties: map[string]any{
			"timezone": map[string]any{
				"type":        "string",
				"description": "IANA timezone name, e.g. \"America/New_York\". Defaults to UTC.",
			},
		},
		Handler: func(_ context.Context, input json.RawMessage) (string, error) {
			var in struct {
				Timezone string `json:"timezone"`
			}
			if len(input) > 0 && string(input) != "null" {
				if err := json.Unmarshal(input, &in); err != nil {
					return "", fmt.Errorf("invalid input: %w", err)
				}
			}
			tz := strings.TrimSpace(in.Timezone)
			loc := time.UTC
			if tz != "" {
				l, err := time.LoadLocation(tz)
				if err != nil || tz == "Local" {
					return "", fmt.Errorf("unknown timezone %q", tz)
				}
				loc = l
			}
			return now().In(loc).Format(time.RFC3339), nil
		},
	}
}
