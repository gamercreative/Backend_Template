package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mapEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// baseEnv is the smallest valid environment.
func baseEnv(extra map[string]string) map[string]string {
	m := map[string]string{"DATABASE_URL": "postgres://u:p@localhost:5432/db?sslmode=disable"}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// noFile is a dotenv path that does not exist.
func noFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), ".env")
}

func writeDotenv(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want Config
	}{
		{
			name: "fake provider when no key",
			env:  baseEnv(nil),
			want: Config{
				Env:             "development",
				Port:            "8080",
				DatabaseURL:     "postgres://u:p@localhost:5432/db?sslmode=disable",
				AIProvider:      "fake",
				AnthropicModel:  "claude-opus-5-5",
				AIFallbacks:     true,
				AIMaxTokens:     16000,
				AIMaxIterations: 10,
				AIHistoryLimit:  40,
				RequestTimeout:  300 * time.Second,
				CORSOrigins:     []string{"*"},
				LogLevel:        "info",
				OllamaBaseURL:   "http://localhost:11434",
				OllamaModel:     "qwen3.5:35b-a3b",
				OllamaThink:     "",
				OllamaNumCtx:    0,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := load(mapEnv(tt.env), noFile(t))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestProviderDefault(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantProvider string
		wantErr      string
	}{
		{"key set defaults to anthropic", baseEnv(map[string]string{"ANTHROPIC_API_KEY": "k"}), "anthropic", ""},
		{"no key defaults to fake", baseEnv(nil), "fake", ""},
		{"explicit fake wins over key", baseEnv(map[string]string{"ANTHROPIC_API_KEY": "k", "AI_PROVIDER": "fake"}), "fake", ""},
		{"explicit anthropic without key", baseEnv(map[string]string{"AI_PROVIDER": "anthropic"}), "", "ANTHROPIC_API_KEY is required"},
		{"explicit ollama without key", baseEnv(map[string]string{"AI_PROVIDER": "ollama"}), "ollama", ""},
		{"ollama never chosen by default", baseEnv(map[string]string{"OLLAMA_MODEL": "m"}), "fake", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := load(mapEnv(tt.env), noFile(t))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.AIProvider != tt.wantProvider {
				t.Errorf("AIProvider = %q, want %q", got.AIProvider, tt.wantProvider)
			}
		})
	}
}

func TestValidationFailures(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"missing database url", map[string]string{}, "DATABASE_URL is required"},
		{"unknown provider", baseEnv(map[string]string{"AI_PROVIDER": "openai"}), "AI_PROVIDER must be"},
		{"unknown effort", baseEnv(map[string]string{"ANTHROPIC_EFFORT": "ultra"}), "ANTHROPIC_EFFORT must be"},
		{"bad fallbacks bool", baseEnv(map[string]string{"AI_FALLBACKS": "maybe"}), "AI_FALLBACKS must be a boolean"},
		{"zero max tokens", baseEnv(map[string]string{"AI_MAX_TOKENS": "0"}), "AI_MAX_TOKENS must be a positive integer"},
		{"non-numeric max tokens", baseEnv(map[string]string{"AI_MAX_TOKENS": "lots"}), "AI_MAX_TOKENS must be a positive integer"},
		{"negative iterations", baseEnv(map[string]string{"AI_MAX_ITERATIONS": "-1"}), "AI_MAX_ITERATIONS must be a positive integer"},
		{"non-numeric history", baseEnv(map[string]string{"AI_HISTORY_LIMIT": "x"}), "AI_HISTORY_LIMIT must be a positive integer"},
		{"bad timeout", baseEnv(map[string]string{"REQUEST_TIMEOUT": "soon"}), "REQUEST_TIMEOUT must be a Go duration"},
		{"bad log level", baseEnv(map[string]string{"LOG_LEVEL": "trace"}), "LOG_LEVEL must be one of"},
		{"ollama url without scheme", baseEnv(map[string]string{"OLLAMA_BASE_URL": "localhost:11434"}), "OLLAMA_BASE_URL must be an http or https URL"},
		{"ollama url bad scheme", baseEnv(map[string]string{"OLLAMA_BASE_URL": "ftp://host:11434"}), "OLLAMA_BASE_URL must be an http or https URL"},
		{"ollama url without host", baseEnv(map[string]string{"OLLAMA_BASE_URL": "http://"}), "OLLAMA_BASE_URL must be an http or https URL"},
		{"ollama url unparsable", baseEnv(map[string]string{"OLLAMA_BASE_URL": "http://[::1"}), "OLLAMA_BASE_URL must be an http or https URL"},
		{"bad ollama think", baseEnv(map[string]string{"OLLAMA_THINK": "yes"}), "OLLAMA_THINK must be one of"},
		{"negative ollama num ctx", baseEnv(map[string]string{"OLLAMA_NUM_CTX": "-1"}), "OLLAMA_NUM_CTX must be an integer of 0 or more"},
		{"non-numeric ollama num ctx", baseEnv(map[string]string{"OLLAMA_NUM_CTX": "big"}), "OLLAMA_NUM_CTX must be an integer of 0 or more"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(mapEnv(tt.env), noFile(t))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidAdvancedValues(t *testing.T) {
	env := baseEnv(map[string]string{
		"ANTHROPIC_API_KEY": "k",
		"ANTHROPIC_EFFORT":  "xhigh",
		"AI_FALLBACKS":      "false",
		"AI_MAX_TOKENS":     "512",
		"AI_MAX_ITERATIONS": "3",
		"AI_HISTORY_LIMIT":  "7",
		"REQUEST_TIMEOUT":   "45s",
		"LOG_LEVEL":         "debug",
		"APP_ENV":           "production",
		"PORT":              "9000",
	})
	got, err := load(mapEnv(env), noFile(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.AnthropicEffort != "xhigh" || got.AIFallbacks || got.AIMaxTokens != 512 ||
		got.AIMaxIterations != 3 || got.AIHistoryLimit != 7 ||
		got.RequestTimeout != 45*time.Second || got.LogLevel != "debug" ||
		got.Env != "production" || got.Port != "9000" || got.AnthropicAPIKey != "k" {
		t.Errorf("unexpected config: %+v", got)
	}
}

func TestOllamaValues(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantURL string
		wantMdl string
		think   string
		numCtx  int
	}{
		{
			name:    "explicit values",
			env:     map[string]string{"AI_PROVIDER": "ollama", "OLLAMA_BASE_URL": "https://ollama.internal:443", "OLLAMA_MODEL": "llama3:8b", "OLLAMA_THINK": "false", "OLLAMA_NUM_CTX": "16384"},
			wantURL: "https://ollama.internal:443",
			wantMdl: "llama3:8b",
			think:   "false",
			numCtx:  16384,
		},
		{
			name:    "trailing slash stripped",
			env:     map[string]string{"OLLAMA_BASE_URL": "http://gpu-box:11434/"},
			wantURL: "http://gpu-box:11434",
			wantMdl: "qwen3.5:35b-a3b",
		},
		{
			name:    "several trailing slashes stripped",
			env:     map[string]string{"OLLAMA_BASE_URL": "http://gpu-box:11434//"},
			wantURL: "http://gpu-box:11434",
			wantMdl: "qwen3.5:35b-a3b",
		},
		{
			name:    "think true and num ctx zero",
			env:     map[string]string{"OLLAMA_THINK": "true", "OLLAMA_NUM_CTX": "0"},
			wantURL: "http://localhost:11434",
			wantMdl: "qwen3.5:35b-a3b",
			think:   "true",
			numCtx:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := load(mapEnv(baseEnv(tt.env)), noFile(t))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.OllamaBaseURL != tt.wantURL || got.OllamaModel != tt.wantMdl ||
				got.OllamaThink != tt.think || got.OllamaNumCtx != tt.numCtx {
				t.Errorf("got URL=%q model=%q think=%q numCtx=%d; want URL=%q model=%q think=%q numCtx=%d",
					got.OllamaBaseURL, got.OllamaModel, got.OllamaThink, got.OllamaNumCtx,
					tt.wantURL, tt.wantMdl, tt.think, tt.numCtx)
			}
		})
	}
}

func TestOllamaValuesFromDotenv(t *testing.T) {
	path := writeDotenv(t, "AI_PROVIDER=ollama\nOLLAMA_BASE_URL=http://127.0.0.1:11500/\nOLLAMA_MODEL=\"gemma3:12b\"\nOLLAMA_THINK=true\nOLLAMA_NUM_CTX=32768\n")
	got, err := load(mapEnv(baseEnv(nil)), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.AIProvider != "ollama" || got.OllamaBaseURL != "http://127.0.0.1:11500" ||
		got.OllamaModel != "gemma3:12b" || got.OllamaThink != "true" || got.OllamaNumCtx != 32768 {
		t.Errorf("unexpected config: %+v", got)
	}
}

func TestMultipleFailuresReportedTogether(t *testing.T) {
	env := map[string]string{
		"AI_PROVIDER":      "anthropic",
		"AI_MAX_TOKENS":    "0",
		"REQUEST_TIMEOUT":  "soon",
		"LOG_LEVEL":        "trace",
		"ANTHROPIC_EFFORT": "ultra",
	}
	_, err := load(mapEnv(env), noFile(t))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{
		"DATABASE_URL is required",
		"ANTHROPIC_API_KEY is required",
		"ANTHROPIC_EFFORT must be",
		"AI_MAX_TOKENS must be",
		"REQUEST_TIMEOUT must be",
		"LOG_LEVEL must be",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestErrorsNeverContainSecretValues(t *testing.T) {
	const secret = "sk-test-SECRET-123"
	env := map[string]string{
		"ANTHROPIC_API_KEY": secret,
		"AI_FALLBACKS":      "bogus",
		"AI_PROVIDER":       "nope",
		"DATABASE_URL":      "",
	}
	_, err := load(mapEnv(env), noFile(t))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaks secret: %q", err.Error())
	}
}

func TestRealEnvOverridesDotenv(t *testing.T) {
	path := writeDotenv(t, "PORT=9000\nDATABASE_URL=postgres://from-file\n")
	env := map[string]string{"PORT": "7000"}
	got, err := load(mapEnv(env), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Port != "7000" {
		t.Errorf("Port = %q, want 7000 (real env must win)", got.Port)
	}
	if got.DatabaseURL != "postgres://from-file" {
		t.Errorf("DatabaseURL = %q, want value from .env", got.DatabaseURL)
	}
}

func TestDotenvMissingFileIsNotAnError(t *testing.T) {
	got, err := load(mapEnv(baseEnv(nil)), noFile(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Port != "8080" {
		t.Errorf("Port = %q, want default", got.Port)
	}
}

func TestDotenvQuotedAndExportValues(t *testing.T) {
	path := writeDotenv(t, `# a comment

export APP_ENV="staging"
PORT='9100'
DATABASE_URL=postgres://db/x # trailing comment
ANTHROPIC_MODEL = "claude-test"
CORS_ALLOWED_ORIGINS="https://a.example, https://b.example"
LOG_LEVEL=debug
`)
	got, err := load(mapEnv(map[string]string{}), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Env != "staging" {
		t.Errorf("Env = %q, want staging", got.Env)
	}
	if got.Port != "9100" {
		t.Errorf("Port = %q, want 9100", got.Port)
	}
	if got.DatabaseURL != "postgres://db/x" {
		t.Errorf("DatabaseURL = %q", got.DatabaseURL)
	}
	if got.AnthropicModel != "claude-test" {
		t.Errorf("AnthropicModel = %q", got.AnthropicModel)
	}
	if want := []string{"https://a.example", "https://b.example"}; !reflect.DeepEqual(got.CORSOrigins, want) {
		t.Errorf("CORSOrigins = %v, want %v", got.CORSOrigins, want)
	}
	if got.LogLevel != "debug" {
		t.Errorf("LogLevel = %q", got.LogLevel)
	}
}

func TestDotenvParseErrorsAreCollected(t *testing.T) {
	path := writeDotenv(t, "NOT_A_PAIR\nPORT=\"unterminated\nDATABASE_URL=postgres://ok\n")
	got, err := load(mapEnv(map[string]string{}), path)
	if err == nil {
		t.Fatal("expected parse errors")
	}
	for _, want := range []string{"line 1", "line 2", "unterminated"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
	if got.DatabaseURL != "" {
		t.Errorf("Load must not return a config on error")
	}
}

func TestParseDotenv(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    map[string]string
		wantErr bool
	}{
		{"plain", "A=1\nB=two\n", map[string]string{"A": "1", "B": "two"}, false},
		{"comments and blanks", "\n# c\n  # indented c\nA=1\n", map[string]string{"A": "1"}, false},
		{"export prefix", "export A=1\n\texport\tB=2\n", map[string]string{"A": "1", "B": "2"}, false},
		{"double quotes", `A="x y # not comment"`, map[string]string{"A": "x y # not comment"}, false},
		{"single quotes", `A='x=y'`, map[string]string{"A": "x=y"}, false},
		{"inline comment", "A=val # note\nB=val#kept\n", map[string]string{"A": "val", "B": "val#kept"}, false},
		{"empty value", "A=\n", map[string]string{"A": ""}, false},
		{"value with equals", "A=a=b\n", map[string]string{"A": "a=b"}, false},
		{"no equals", "A\n", nil, true},
		{"empty key", "=x\n", nil, true},
		{"unterminated quote", "A=\"x\n", nil, true},
		{"text after quote", "A=\"x\" y\n", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDotenv(strings.NewReader(tt.in))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCORSOrigins(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"default", "", []string{"*"}},
		{"trim and drop empties", " https://a.com , ,https://b.com,", []string{"https://a.com", "https://b.com"}},
		{"single", "http://localhost:3000", []string{"http://localhost:3000"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := baseEnv(map[string]string{"CORS_ALLOWED_ORIGINS": tt.raw})
			got, err := load(mapEnv(env), noFile(t))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got.CORSOrigins, tt.want) {
				t.Errorf("CORSOrigins = %v, want %v", got.CORSOrigins, tt.want)
			}
		})
	}
}
