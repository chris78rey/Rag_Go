package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/codex/semantic-rag-go/internal/admin"
	"github.com/codex/semantic-rag-go/internal/api"
	"github.com/codex/semantic-rag-go/internal/auth"
	"github.com/codex/semantic-rag-go/internal/database"
	"github.com/codex/semantic-rag-go/internal/document"
	"github.com/codex/semantic-rag-go/internal/embeddings"
	"github.com/codex/semantic-rag-go/internal/llm"
	"github.com/codex/semantic-rag-go/internal/rag"
	"github.com/codex/semantic-rag-go/internal/task"
	"github.com/codex/semantic-rag-go/internal/telegrambot"
	"github.com/codex/semantic-rag-go/internal/vectorstore"
	"github.com/codex/semantic-rag-go/internal/webui"
)

func main() {
	configureLogger()

	port := env("PORT", "8080")
	uploadDir := env("UPLOAD_DIR", "./uploads")
	tmpDir := env("TMP_DIR", os.TempDir())

	mustMkdir(uploadDir)
	mustMkdir(tmpDir)

	openRouterKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if openRouterKey == "" {
		log.Fatal("OPENROUTER_API_KEY requerido")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dbPath := env("DB_PATH", "data/rag.db")
	mustMkdir(filepath.Dir(dbPath))

	db, err := database.New(ctx, dbPath)
	if err != nil {
		log.Fatalf("sqlite no disponible: %v", err)
	}
	defer db.Close()

	authSvc := auth.NewService(env("JWT_SECRET", "dev-change-this-secret"))

	chunker := document.NewChunker(
		envInt("CHUNK_SIZE", 512),
		envInt("CHUNK_OVERLAP", 64),
	)
	extractor := document.NewExtractor(tmpDir)
	embClient := embeddings.NewClient(openRouterKey, env("EMBEDDING_MODEL", "openai/text-embedding-3-large"))
	llmClient := llm.NewClient(openRouterKey, env("OPENROUTER_MODEL", "openai/gpt-4o-mini"))

	vsClient, err := connectQdrantWithRetry(ctx)
	if err != nil {
		log.Fatalf("qdrant no disponible: %v", err)
	}
	defer vsClient.Close()

	taskMgr := task.NewManager()
	topK := envUint64("TOP_K", 10)
	chatSvc := rag.NewService(db.Pool, authSvc, embClient, llmClient, vsClient, topK)

	mux := http.NewServeMux()

	authMw := auth.RequireAuth(authSvc)
	adminMw := func(next http.Handler) http.Handler {
		return authMw(auth.RequireAdmin()(next))
	}

	apiServer := api.NewServer(
		db.Pool,
		authSvc,
		taskMgr,
		chunker,
		extractor,
		embClient,
		llmClient,
		vsClient,
		chatSvc,
		uploadDir,
		topK,
	)
	apiServer.RegisterRoutes(mux, authMw, adminMw)

	adminHandler := admin.NewHandler(db.Pool, authSvc)
	mux.Handle("GET /api/admin/users", adminMw(http.HandlerFunc(adminHandler.ListUsers)))
	mux.Handle("POST /api/admin/users", adminMw(http.HandlerFunc(adminHandler.CreateUser)))
	mux.Handle("PATCH /api/admin/users/{id}", adminMw(http.HandlerFunc(adminHandler.UpdateUser)))
	mux.Handle("PATCH /api/admin/users/{id}/plan", adminMw(http.HandlerFunc(adminHandler.UpdatePlan)))
	mux.Handle("PATCH /api/admin/users/{id}/password", adminMw(http.HandlerFunc(adminHandler.UpdatePassword)))
	mux.Handle("GET /api/admin/usage", adminMw(http.HandlerFunc(adminHandler.GetUsage)))
	mux.Handle("GET /api/admin/users/{id}/documents", adminMw(http.HandlerFunc(adminHandler.GetUserDocuments)))
	mux.Handle("GET /api/admin/settings", adminMw(http.HandlerFunc(adminHandler.GetSettings)))
	mux.Handle("PUT /api/admin/settings", adminMw(http.HandlerFunc(adminHandler.UpdateSetting)))
	mux.Handle("POST /api/admin/users/{id}/payment", adminMw(http.HandlerFunc(adminHandler.RegisterPayment)))

	// Público (autenticado) — título del RAG visible para todos
	mux.Handle("GET /api/settings/{key}", authMw(http.HandlerFunc(adminHandler.GetPublicSetting)))
	// Sin auth — para la pantalla de login
	mux.HandleFunc("GET /api/public/app-title", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var value string
		db.Pool.QueryRowContext(r.Context(), "SELECT value FROM system_settings WHERE key = 'app_title'").Scan(&value)
		fmt.Fprintf(w, `{"key":"app_title","value":"%s"}`, value)
	})

	registerStaticFrontend(mux)

	addr := ":" + port
	slog.Info("servidor_iniciado", "addr", addr, "web", "embedded", "uploads", uploadDir)

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	if telegramBotToken := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")); telegramBotToken != "" {
		go func() {
			telegrambot.New(telegramBotToken, chatSvc).Run(runCtx)
		}()
		slog.Info("telegram_bot_iniciado")
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           withCORS(mux),
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("servidor detenido: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(quit)

	<-quit

	slog.Info("apagando_servidor...")
	cancelRun()

	ctxShutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()

	if err := server.Shutdown(ctxShutdown); err != nil {
		slog.Error("error_apagando_servidor", "error", err)
	}

	slog.Info("servidor_apagado_correctamente")
}

func connectQdrantWithRetry(ctx context.Context) (*vectorstore.Client, error) {
	host := env("QDRANT_HOST", "qdrant")
	port := env("QDRANT_PORT", "6334")
	collection := env("QDRANT_COLLECTION", "semantic_docs")
	vectorSize := embeddingDim(env("EMBEDDING_MODEL", "openai/text-embedding-3-large"))

	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		client, err := vectorstore.NewClient(ctx, host, port, collection, vectorSize)
		if err == nil {
			return client, nil
		}

		lastErr = err
		slog.Warn("esperando_qdrant", "intento", attempt, "error", err)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	return nil, fmt.Errorf("no se pudo conectar a Qdrant: %w", lastErr)
}

func registerStaticFrontend(mux *http.ServeMux) {
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(webui.IndexHTML)
	})
}

func withCORS(next http.Handler) http.Handler {
	allowOrigin := env("CORS_ALLOW_ORIGIN", "*")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func configureLogger() {
	level := new(slog.LevelVar)

	switch strings.ToLower(env("LOG_LEVEL", "info")) {
	case "debug":
		level.Set(slog.LevelDebug)
	case "warn":
		level.Set(slog.LevelWarn)
	case "error":
		level.Set(slog.LevelError)
	default:
		level.Set(slog.LevelInfo)
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}

func env(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		slog.Warn("env_int_invalido", "key", key, "value", value, "fallback", fallback)
		return fallback
	}

	return parsed
}

func envUint64(key string, fallback uint64) uint64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		slog.Warn("env_uint64_invalido", "key", key, "value", value, "fallback", fallback)
		return fallback
	}

	return parsed
}

func embeddingDim(model string) uint64 {
	switch {
	case strings.Contains(model, "text-embedding-3-large"):
		return 3072
	case strings.Contains(model, "text-embedding-ada-002"):
		return 1536
	default:
		return 3072 // valor por defecto para mantener la configuracion grande
	}
}

func mustMkdir(path string) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		log.Fatalf("no se pudo crear directorio %s: %v", path, err)
	}
}
