// Package config loads runtime configuration from the environment.
//
// Contract (owned by the orchestrator): the Config struct below and
//
//	func Load() (Config, error)
//
// Load reads an optional .env file in the working directory (never overriding
// variables already set in the real environment), applies the defaults listed
// on each field, validates, and returns every problem in a single error.
package config

import (
	"os"
	"time"
)

type Config struct {
	Env  string // APP_ENV, default "development"
	Port string // PORT, default "8080"

	DatabaseURL string // DATABASE_URL, required

	// AI_PROVIDER: "anthropic", "ollama" or "fake". Default: "anthropic" when
	// ANTHROPIC_API_KEY is set, otherwise "fake". "ollama" is never a default.
	AIProvider      string
	AnthropicAPIKey string // ANTHROPIC_API_KEY, required when AIProvider == "anthropic"
	AnthropicModel  string // ANTHROPIC_MODEL, default "claude-opus-5-5"
	AnthropicEffort string // ANTHROPIC_EFFORT, default "" (one of "", low, medium, high, xhigh, max)
	AIFallbacks     bool   // AI_FALLBACKS, default true

	OllamaBaseURL string // OLLAMA_BASE_URL, default "http://localhost:11434"; must be an http(s) URL
	OllamaModel   string // OLLAMA_MODEL, default "qwen3.5:35b-a3b"
	OllamaThink   string // OLLAMA_THINK, default "" (one of "", true, false); "" = model default
	OllamaNumCtx  int    // OLLAMA_NUM_CTX, default 0 (= Ollama's default context window), must be >= 0

	AIMaxTokens     int64 // AI_MAX_TOKENS, default 16000, must be > 0
	AIMaxIterations int   // AI_MAX_ITERATIONS, default 10, must be > 0
	AIHistoryLimit  int   // AI_HISTORY_LIMIT, default 40 (messages of history sent to the model), must be > 0

	RequestTimeout time.Duration // REQUEST_TIMEOUT, default "300s" (Go duration syntax)
	CORSOrigins    []string      // CORS_ALLOWED_ORIGINS, comma-separated, default "*"
	LogLevel       string        // LOG_LEVEL, default "info" (debug, info, warn, error)
}

// Load builds the Config from the process environment and ./.env.
// Real environment variables win over .env; an empty value counts as unset.
func Load() (Config, error) {
	return load(os.Getenv, ".env")
}
