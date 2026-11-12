package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// load is Load with the environment and .env path injected, so tests never
// touch the process environment. getenv returns "" for unset variables.
// Values from getenv take precedence over values from the dotenv file.
func load(getenv func(string) string, dotenvPath string) (Config, error) {
	dotenv, dotenvErr := readDotenv(dotenvPath)

	l := &loader{errs: nil}
	if dotenvErr != nil {
		l.errs = append(l.errs, dotenvErr)
	}
	get := func(key string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return dotenv[key]
	}
	l.get = get

	var c Config
	c.Env = withDefault(get("APP_ENV"), "development")
	c.Port = withDefault(get("PORT"), "8080")

	c.DatabaseURL = get("DATABASE_URL")
	if c.DatabaseURL == "" {
		l.fail("DATABASE_URL is required")
	}

	c.AnthropicAPIKey = get("ANTHROPIC_API_KEY")
	c.AIProvider = get("AI_PROVIDER")
	if c.AIProvider == "" {
		if c.AnthropicAPIKey != "" {
			c.AIProvider = "anthropic"
		} else {
			c.AIProvider = "fake"
		}
	}
	switch c.AIProvider {
	case "anthropic":
		if c.AnthropicAPIKey == "" {
			l.fail("ANTHROPIC_API_KEY is required when AI_PROVIDER is anthropic")
		}
	case "ollama", "fake":
	default:
		l.fail(`AI_PROVIDER must be "anthropic", "ollama" or "fake"`)
	}

	c.AnthropicModel = withDefault(get("ANTHROPIC_MODEL"), "claude-opus-5-5")
	c.AnthropicEffort = get("ANTHROPIC_EFFORT")
	switch c.AnthropicEffort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		l.fail(`ANTHROPIC_EFFORT must be one of "", low, medium, high, xhigh, max`)
	}

	c.AIFallbacks = l.boolOr("AI_FALLBACKS", true)
	c.AIMaxTokens = l.positiveInt("AI_MAX_TOKENS", 16000)
	c.AIMaxIterations = int(l.positiveInt("AI_MAX_ITERATIONS", 10))
	c.AIHistoryLimit = int(l.positiveInt("AI_HISTORY_LIMIT", 40))

	c.OllamaBaseURL = l.ollamaBaseURL("OLLAMA_BASE_URL", "http://localhost:11434")
	c.OllamaModel = withDefault(get("OLLAMA_MODEL"), "qwen3.5:35b-a3b")
	c.OllamaThink = get("OLLAMA_THINK")
	switch c.OllamaThink {
	case "", "true", "false":
	default:
		l.fail(`OLLAMA_THINK must be one of "", true, false`)
	}
	c.OllamaNumCtx = l.nonNegativeInt("OLLAMA_NUM_CTX", 0)

	c.RequestTimeout = l.duration("REQUEST_TIMEOUT", 300*time.Second)
	c.CORSOrigins = splitList(withDefault(get("CORS_ALLOWED_ORIGINS"), "*"))

	c.LogLevel = withDefault(get("LOG_LEVEL"), "info")
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		l.fail("LOG_LEVEL must be one of debug, info, warn, error")
	}

	if len(l.errs) > 0 {
		return Config{}, errors.Join(l.errs...)
	}
	return c, nil
}

// loader accumulates problems so every invalid variable is reported at once.
// Messages name the variable but never echo its value, so secrets stay out
// of errors.
type loader struct {
	get  func(string) string
	errs []error
}

func (l *loader) fail(msg string) {
	l.errs = append(l.errs, errors.New(msg))
}

func (l *loader) boolOr(key string, def bool) bool {
	raw := l.get(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		l.fail(key + " must be a boolean (true or false)")
		return def
	}
	return v
}

func (l *loader) positiveInt(key string, def int64) int64 {
	raw := l.get(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		l.fail(key + " must be a positive integer")
		return def
	}
	return v
}

func (l *loader) nonNegativeInt(key string, def int) int {
	raw := l.get(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		l.fail(key + " must be an integer of 0 or more")
		return def
	}
	return v
}

// ollamaBaseURL checks that the value is an absolute http or https URL with a
// host, and returns it without trailing slashes. The url.Parse error is not
// used in the message because it would echo the value.
func (l *loader) ollamaBaseURL(key, def string) string {
	raw := l.get(key)
	if raw == "" {
		return def
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		l.fail(key + " must be an http or https URL with a host")
		return def
	}
	return strings.TrimRight(raw, "/")
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	raw := l.get(key)
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		l.fail(fmt.Sprintf("%s must be a Go duration such as %q", key, "300s"))
		return def
	}
	return v
}

func withDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// splitList splits on commas, trims whitespace and drops empty entries.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
