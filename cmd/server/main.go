// Command server is the HTTP entrypoint. It only wires things together:
// config -> pool -> queries -> ai -> services -> handlers -> router.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"alphahack/internal/ai"
	"alphahack/internal/config"
	"alphahack/internal/handlers"
	httpx "alphahack/internal/http"
	"alphahack/internal/queries"
	"alphahack/internal/services"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config:\n%w", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel(cfg.LogLevel)}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Database.
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	store := queries.New(pool)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = store.Ping(pingCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("database unreachable (is `make up` running?): %w", err)
	}

	// Model provider.
	var client ai.Client
	defaultModel := cfg.AnthropicModel
	switch cfg.AIProvider {
	case "anthropic":
		client = ai.NewAnthropic(ai.Options{
			APIKey:        cfg.AnthropicAPIKey,
			Model:         cfg.AnthropicModel,
			Effort:        cfg.AnthropicEffort,
			MaxTokens:     cfg.AIMaxTokens,
			MaxIterations: cfg.AIMaxIterations,
			Fallbacks:     cfg.AIFallbacks,
		})
	case "ollama":
		defaultModel = cfg.OllamaModel
		client = ai.NewOllama(ai.OllamaOptions{
			BaseURL:       cfg.OllamaBaseURL,
			Model:         cfg.OllamaModel,
			Think:         cfg.OllamaThink,
			NumCtx:        cfg.OllamaNumCtx,
			MaxTokens:     cfg.AIMaxTokens,
			MaxIterations: cfg.AIMaxIterations,
		})
		if cfg.OllamaNumCtx == 0 {
			logger.Warn("OLLAMA_NUM_CTX is 0: Ollama's default context window applies and may truncate long prompts")
		}
	default:
		defaultModel = "fake"
		client = ai.NewFake()
		logger.Warn("AI_PROVIDER=fake: replies are canned echoes; set AI_PROVIDER=ollama or ANTHROPIC_API_KEY for real model calls")
	}

	// Services and handlers.
	tools := services.DefaultTools()
	h := handlers.New(handlers.Deps{
		Agents:        services.NewAgentService(store),
		Conversations: services.NewConversationService(store),
		Chat: services.NewChatService(store, client, tools, services.ChatOptions{
			DefaultModel:  defaultModel,
			HistoryLimit:  cfg.AIHistoryLimit,
			MaxIterations: cfg.AIMaxIterations,
		}),
		Health: services.NewHealthService(store),
		Logger: logger,
	})

	mux := http.NewServeMux()
	h.Register(mux)

	// Outermost first. Logger sits outside Recover so it sees the 500 a panic produces.
	root := httpx.Chain(httpx.NewRouter(mux),
		httpx.WithRequestID(),
		httpx.WithLogger(logger),
		httpx.WithRecover(logger),
		httpx.WithCORS(cfg.CORSOrigins),
		httpx.WithTimeout(cfg.RequestTimeout),
	)
	srv := httpx.NewServer(":"+cfg.Port, root)

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", srv.Addr, "env", cfg.Env,
			"ai_provider", cfg.AIProvider, "model", defaultModel, "tools", len(tools))
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("server: %w", err)
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

func logLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
