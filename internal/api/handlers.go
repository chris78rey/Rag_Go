package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/codex/semantic-rag-go/internal/auth"
	"github.com/codex/semantic-rag-go/internal/document"
	"github.com/codex/semantic-rag-go/internal/embeddings"
	"github.com/codex/semantic-rag-go/internal/llm"
	"github.com/codex/semantic-rag-go/internal/task"
	"github.com/codex/semantic-rag-go/internal/vectorstore"
	"github.com/google/uuid"
)

// Server holds all dependencies for HTTP handlers.
type Server struct {
	db        *sql.DB
	authSvc   *auth.Service
	taskMgr   *task.Manager
	chunker   *document.Chunker
	extractor *document.Extractor
	embClient *embeddings.Client
	llmClient *llm.Client
	vsClient  *vectorstore.Client
	uploadDir string
	topK      uint64
}

// NewServer creates the API server with all dependencies.
func NewServer(
	db *sql.DB,
	authSvc *auth.Service,
	taskMgr *task.Manager,
	chunker *document.Chunker,
	extractor *document.Extractor,
	embClient *embeddings.Client,
	llmClient *llm.Client,
	vsClient *vectorstore.Client,
	uploadDir string,
	topK uint64,
) *Server {
	return &Server{
		db:        db,
		authSvc:   authSvc,
		taskMgr:   taskMgr,
		chunker:   chunker,
		extractor: extractor,
		embClient: embClient,
		llmClient: llmClient,
		vsClient:  vsClient,
		uploadDir: uploadDir,
		topK:      topK,
	}
}

// RegisterRoutes registers all public and authenticated routes.
func (s *Server) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler, adminMw func(http.Handler) http.Handler) {
	// Public
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)

	// Authenticated
	mux.Handle("GET /api/me", authMw(http.HandlerFunc(s.handleMe)))
	mux.Handle("POST /api/documents/upload", authMw(http.HandlerFunc(s.handleUpload)))
	mux.Handle("POST /api/documents/text", authMw(http.HandlerFunc(s.handleUploadText)))
	mux.Handle("GET /api/documents", authMw(http.HandlerFunc(s.handleListDocuments)))
	mux.Handle("DELETE /api/documents/{id}", authMw(http.HandlerFunc(s.handleDeleteDocument)))
	mux.Handle("POST /api/chat", authMw(http.HandlerFunc(s.handleChat)))
	mux.Handle("GET /api/usage/today", authMw(http.HandlerFunc(s.handleUsageToday)))
	mux.Handle("GET /api/task/{task_id}", authMw(http.HandlerFunc(s.handleTaskStatus)))
}

// --- Auth ---

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token    string `json:"token"`
	UserID   string `json:"user_id"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	PlanCode string `json:"plan_code"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Solicitud inválida"})
		return
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))

	var userID, hash, role, planCode string
	var active bool
	err := s.db.QueryRowContext(r.Context(),
		`SELECT u.id, u.password_hash, u.role, p.code, u.active
		 FROM users u JOIN plans p ON u.plan_id = p.id
		 WHERE u.email = ?`, email,
	).Scan(&userID, &hash, &role, &planCode, &active)

	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Correo o contraseña incorrectos"})
		return
	}
	if err != nil {
		slog.Error("login_query", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error interno"})
		return
	}

	planCode = auth.NormalizePlanCode(planCode)

	if !active {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Tu cuenta está desactivada"})
		return
	}

	if !s.authSvc.CheckPassword(hash, req.Password) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Correo o contraseña incorrectos"})
		return
	}

	token, err := s.authSvc.GenerateToken(userID, email, role, planCode)
	if err != nil {
		slog.Error("generando_token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error generando token"})
		return
	}

	slog.Info("login_exitoso", "user_id", userID, "email", email)

	writeJSON(w, http.StatusOK, LoginResponse{
		Token:    token,
		UserID:   userID,
		Email:    email,
		Role:     role,
		PlanCode: planCode,
	})
}

// --- Me ---

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())

	var email, fullName, role, planCode string
	var active bool
	var createdAt string

	err := s.db.QueryRowContext(r.Context(),
		`SELECT u.email, u.full_name, u.role, p.code, u.active,
		        strftime('%Y-%m-%dT%H:%M:%fZ', u.created_at)
		 FROM users u JOIN plans p ON u.plan_id = p.id
		 WHERE u.id = ?`, userID,
	).Scan(&email, &fullName, &role, &planCode, &active, &createdAt)

	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "usuario no encontrado"})
		return
	}

	planCode = auth.NormalizePlanCode(planCode)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":         userID,
		"email":      email,
		"full_name":  fullName,
		"role":       role,
		"plan_code":  planCode,
		"active":     active,
		"created_at": createdAt,
	})
}

// --- Upload ---

type UploadResponse struct {
	TaskID     string `json:"task_id"`
	DocumentID string `json:"document_id"`
	Filename   string `json:"filename"`
	Message    string `json:"message"`
}

var (
	errRepoQuotaExceeded         = errors.New("repo quota exceeded")
	errStorageQuotaExceeded      = errors.New("storage quota exceeded")
	errConcurrentUploadsExceeded = errors.New("concurrent uploads exceeded")
)

type uploadQuotaInfo struct {
	cfg             planConfig
	documentCount   int
	storageBytes    int64
	processingCount int
}

func (s *Server) resolvePlanConfig(ctx context.Context, planCode string) planConfig {
	cfg, err := s.loadPlanConfig(ctx, planCode)
	if err != nil {
		slog.Warn("cargando_config_plan", "plan", planCode, "error", err)
		return fallbackPlanConfig(planCode)
	}
	return cfg
}

func (s *Server) checkUploadQuota(ctx context.Context, userID, planCode string, additionalBytes int64) (uploadQuotaInfo, error) {
	cfg := s.resolvePlanConfig(ctx, planCode)

	documentCount, err := s.countDocuments(ctx, userID)
	if err != nil {
		return uploadQuotaInfo{}, err
	}
	if documentCount >= cfg.RepositoryLimit {
		return uploadQuotaInfo{}, errRepoQuotaExceeded
	}

	processingCount, err := s.countProcessingDocuments(ctx, userID)
	if err != nil {
		return uploadQuotaInfo{}, err
	}
	if processingCount >= cfg.MaxConcurrentUploads {
		return uploadQuotaInfo{}, errConcurrentUploadsExceeded
	}

	storageBytes, err := s.sumStorageBytes(ctx, userID)
	if err != nil {
		return uploadQuotaInfo{}, err
	}
	if additionalBytes < 0 {
		additionalBytes = 0
	}
	if storageBytes+additionalBytes > cfg.maxStorageBytes() {
		return uploadQuotaInfo{}, errStorageQuotaExceeded
	}

	return uploadQuotaInfo{
		cfg:             cfg,
		documentCount:   documentCount,
		storageBytes:    storageBytes,
		processingCount: processingCount,
	}, nil
}

func isRequestTooLargeError(err error) bool {
	if err == nil {
		return false
	}

	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return true
	}

	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "request body too large") || strings.Contains(lower, "multipart: message too large")
}

func removeUploadFile(path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("limpiando_archivo_temporal", "path", path, "error", err)
	}
}

func writeQuotaError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeRateLimitError(w http.ResponseWriter, limit int, action string) {
	if limit <= 0 {
		return
	}

	writeJSON(w, http.StatusTooManyRequests, map[string]string{
		"error": fmt.Sprintf("límite de %s alcanzado (%d por minuto)", action, limit),
	})
}

func writeUploadQuotaError(w http.ResponseWriter, cfg planConfig, err error) {
	switch {
	case errors.Is(err, errRepoQuotaExceeded):
		writeQuotaError(w, http.StatusForbidden, fmt.Sprintf("tu plan permite hasta %d repositorios activos", cfg.RepositoryLimit))
	case errors.Is(err, errStorageQuotaExceeded):
		writeQuotaError(w, http.StatusForbidden, fmt.Sprintf("tu plan permite hasta %d MB totales de almacenamiento", cfg.MaxTotalStorageMB))
	case errors.Is(err, errConcurrentUploadsExceeded):
		writeQuotaError(w, http.StatusTooManyRequests, fmt.Sprintf("solo puedes tener %d cargas en proceso al mismo tiempo", cfg.MaxConcurrentUploads))
	case errors.Is(err, errRateLimitExceeded):
		writeRateLimitError(w, cfg.UploadsPerMinute, "subidas")
	default:
		writeQuotaError(w, http.StatusForbidden, "cuota de carga excedida")
	}
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	planCode := auth.NormalizePlanCode(auth.GetPlanCode(r.Context()))
	cfg := s.resolvePlanConfig(r.Context(), planCode)

	if err := s.enforceRateLimit(r.Context(), userID, "upload", cfg.UploadsPerMinute); err != nil {
		if errors.Is(err, errRateLimitExceeded) {
			writeRateLimitError(w, cfg.UploadsPerMinute, "subidas")
			return
		}
		slog.Warn("rate_limit_upload", "user_id", userID, "error", err)
	}

	if _, err := s.checkUploadQuota(r.Context(), userID, planCode, 0); err != nil {
		writeUploadQuotaError(w, cfg, err)
		return
	}

	maxBytes := cfg.maxUploadBytes()
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	if err := r.ParseMultipartForm(maxBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("archivo demasiado grande (máx %d MB)", maxBytes>>20),
		})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "campo 'file' requerido"})
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".pdf" && ext != ".txt" && ext != ".md" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "solo se aceptan PDF, TXT o MD"})
		return
	}

	taskID := uuid.New().String()
	documentID := uuid.New().String()

	// Save uploaded file
	savePath := filepath.Join(s.uploadDir, taskID+ext)
	dst, err := os.Create(savePath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "guardando archivo"})
		return
	}
	size, err := io.Copy(dst, file)
	dst.Close()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "copiando archivo"})
		return
	}

	// Register document in DB
	_, err = s.db.ExecContext(r.Context(),
		`INSERT INTO documents (id, user_id, filename, original_filename, size_bytes, status)
		 VALUES (?, ?, ?, ?, ?, 'processing')`,
		documentID, userID, savePath, header.Filename, size,
	)
	if err != nil {
		slog.Error("registrando_documento", "error", err)
		os.Remove(savePath)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "registrando documento"})
		return
	}

	// Register task
	s.taskMgr.Create(taskID, header.Filename)
	slog.Info("subida_recibida", "task_id", taskID, "doc_id", documentID, "user_id", userID, "archivo", header.Filename)

	// Process asynchronously
	go s.processDocument(taskID, documentID, userID, savePath, header.Filename)

	writeJSON(w, http.StatusAccepted, UploadResponse{
		TaskID:     taskID,
		DocumentID: documentID,
		Filename:   header.Filename,
		Message:    "Documento recibido. Analizando contenido...",
	})
}

// --- Upload Text ---

type UploadTextRequest struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

func (s *Server) handleUploadText(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	planCode := auth.NormalizePlanCode(auth.GetPlanCode(r.Context()))
	cfg := s.resolvePlanConfig(r.Context(), planCode)

	if err := s.enforceRateLimit(r.Context(), userID, "upload", cfg.UploadsPerMinute); err != nil {
		if errors.Is(err, errRateLimitExceeded) {
			writeRateLimitError(w, cfg.UploadsPerMinute, "subidas")
			return
		}
		slog.Warn("rate_limit_upload", "user_id", userID, "error", err)
	}

	if _, err := s.checkUploadQuota(r.Context(), userID, planCode, 0); err != nil {
		writeUploadQuotaError(w, cfg, err)
		return
	}

	var req UploadTextRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Solicitud inválida"})
		return
	}

	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "texto requerido"})
		return
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "texto-" + time.Now().Format("20060102-150405")
	}
	title = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' {
			return '-'
		}
		return r
	}, title)

	taskID := uuid.New().String()
	documentID := uuid.New().String()

	// Save as temp .txt file
	savePath := filepath.Join(s.uploadDir, taskID+".txt")
	if err := os.WriteFile(savePath, []byte(text), 0644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "guardando texto"})
		return
	}

	// Register in DB
	_, err := s.db.ExecContext(r.Context(),
		`INSERT INTO documents (id, user_id, filename, original_filename, size_bytes, status)
		 VALUES (?, ?, ?, ?, ?, 'processing')`,
		documentID, userID, savePath, title+".txt", int64(len(text)),
	)
	if err != nil {
		slog.Error("registrando_texto", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "registrando texto"})
		return
	}

	s.taskMgr.Create(taskID, title+".txt")
	slog.Info("texto_recibido", "task_id", taskID, "doc_id", documentID, "user_id", userID, "title", title, "chars", len(text))

	go s.processDocument(taskID, documentID, userID, savePath, title+".txt")

	writeJSON(w, http.StatusAccepted, UploadResponse{
		TaskID:     taskID,
		DocumentID: documentID,
		Filename:   title + ".txt",
		Message:    "Texto recibido. Analizando contenido...",
	})
}

func (s *Server) processDocument(taskID, documentID, userID, filePath, filename string) {
	ctx := context.Background()

	s.taskMgr.UpdateStatus(taskID, task.StatusExtracting, 0, "")
	slog.Info("iniciando_extraccion", "task_id", taskID, "doc_id", documentID, "user_id", userID)

	text, err := s.extractor.ExtractText(filePath)
	if err != nil {
		slog.Error("error_extraccion", "task_id", taskID, "error", err)
		s.taskMgr.UpdateStatus(taskID, task.StatusError, 0, err.Error())
		s.updateDocumentStatus(documentID, "error")
		return
	}

	s.taskMgr.UpdateStatus(taskID, task.StatusEmbedding, 0, "")
	chunks := s.chunker.Split(text, filename)
	slog.Info("chunking_completado", "task_id", taskID, "chunks", len(chunks))

	if len(chunks) == 0 {
		s.taskMgr.UpdateStatus(taskID, task.StatusError, 0, "sin texto extraible")
		s.updateDocumentStatus(documentID, "error")
		return
	}

	s.taskMgr.UpdateProgress(taskID, 0, len(chunks))
	s.updateDocumentProgress(documentID, "processing", 0, len(chunks))

	batchSize := 8
	maxRetries := 3
	var allVectors [][]float64
	var allPayloads []map[string]interface{}

	for i := 0; i < len(chunks); i += batchSize {
		end := i + batchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := chunks[i:end]

		texts := make([]string, len(batch))
		for j, ch := range batch {
			texts[j] = ch.Text
		}

		var vectors [][]float64
		var err error
		for attempt := 0; attempt < maxRetries; attempt++ {
			vectors, err = s.embClient.EmbedBatch(ctx, texts)
			if err == nil {
				break
			}
			slog.Warn("reintentando_embedding", "task_id", taskID, "lote", i/batchSize, "intento", attempt+1, "error", err)
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
		}
		if err != nil {
			slog.Error("fallo_embedding", "task_id", taskID, "lote", i/batchSize, "error", err)
			s.taskMgr.UpdateStatus(taskID, task.StatusError, 0, fmt.Sprintf("error embeddings: %v", err))
			s.updateDocumentStatus(documentID, "error")
			return
		}

		for j, ch := range batch {
			payload := map[string]interface{}{
				"text":        ch.Text,
				"filename":    ch.Filename,
				"section":     ch.Section,
				"chunk_index": ch.Index,
				"user_id":     userID,
				"document_id": documentID,
			}
			allVectors = append(allVectors, vectors[j])
			allPayloads = append(allPayloads, payload)
		}

		slog.Info("lote_embeddings_completado", "task_id", taskID, "lote", i/batchSize, "tamaño", len(batch))
		s.taskMgr.UpdateProgress(taskID, end, len(chunks))
		s.updateDocumentProgress(documentID, "processing", end, len(chunks))
	}

	s.taskMgr.UpdateProgress(taskID, len(chunks), len(chunks))
	s.updateDocumentProgress(documentID, "processing", len(chunks), len(chunks))
	s.taskMgr.UpdateStatus(taskID, task.StatusStoring, len(chunks), "")
	if err := s.vsClient.Upsert(ctx, allVectors, allPayloads); err != nil {
		slog.Error("error_almacenamiento", "task_id", taskID, "error", err)
		s.taskMgr.UpdateStatus(taskID, task.StatusError, 0, fmt.Sprintf("error Qdrant: %v", err))
		s.updateDocumentStatus(documentID, "error")
		return
	}

	s.taskMgr.UpdateStatus(taskID, task.StatusCompleted, len(chunks), "")
	s.taskMgr.UpdateProgress(taskID, len(chunks), len(chunks))
	s.updateDocumentProgress(documentID, "completed", len(chunks), len(chunks))
	slog.Info("ingesta_completada", "task_id", taskID, "doc_id", documentID, "chunks", len(chunks))

	os.Remove(filePath)
}

func (s *Server) updateDocumentStatus(docID, status string) {
	_, err := s.db.ExecContext(context.Background(),
		"UPDATE documents SET status = ? WHERE id = ?", status, docID)
	if err != nil {
		slog.Error("actualizando_estado_documento", "doc_id", docID, "error", err)
	}
}

func (s *Server) updateDocumentProgress(docID, status string, processedChunks, totalChunks int) {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE documents
		 SET status = ?, chunks = ?, processed_chunks = ?, total_chunks = ?
		 WHERE id = ?`,
		status, totalChunks, processedChunks, totalChunks, docID,
	)
	if err != nil {
		slog.Error("actualizando_estado_documento_progress", "doc_id", docID, "error", err)
	}
}

// --- List Documents ---

func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, original_filename, size_bytes, status, chunks, processed_chunks, total_chunks,
		        strftime('%Y-%m-%dT%H:%M:%fZ', created_at)
		 FROM documents WHERE user_id = ?
		 ORDER BY created_at DESC`, userID,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error consultando documentos"})
		return
	}
	defer rows.Close()

	type DocResponse struct {
		ID               string `json:"id"`
		OriginalFilename string `json:"original_filename"`
		SizeBytes        int64  `json:"size_bytes"`
		Status           string `json:"status"`
		Chunks           int    `json:"chunks"`
		ProcessedChunks  int    `json:"processed_chunks"`
		TotalChunks      int    `json:"total_chunks"`
		CreatedAt        string `json:"created_at"`
	}

	var docs []DocResponse
	for rows.Next() {
		var d DocResponse
		if err := rows.Scan(&d.ID, &d.OriginalFilename, &d.SizeBytes, &d.Status, &d.Chunks, &d.ProcessedChunks, &d.TotalChunks, &d.CreatedAt); err != nil {
			continue
		}
		docs = append(docs, d)
	}

	if docs == nil {
		docs = []DocResponse{}
	}

	writeJSON(w, http.StatusOK, docs)
}

// --- Delete Document ---

func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	docID := r.PathValue("id")

	// Verify ownership
	var ownerID string
	err := s.db.QueryRowContext(r.Context(),
		"SELECT user_id FROM documents WHERE id = ?", docID,
	).Scan(&ownerID)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "documento no encontrado"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error interno"})
		return
	}

	role := auth.GetRole(r.Context())
	if ownerID != userID && role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no autorizado"})
		return
	}

	// Delete from Qdrant (using payload filter)
	if err := s.vsClient.DeleteByDocumentID(r.Context(), docID); err != nil {
		slog.Warn("eliminando_vectores_qdrant", "doc_id", docID, "error", err)
	}

	// Delete from PostgreSQL
	_, err = s.db.ExecContext(r.Context(), "DELETE FROM documents WHERE id = ?", docID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "eliminando documento"})
		return
	}

	slog.Info("documento_eliminado", "doc_id", docID, "user_id", userID)
	writeJSON(w, http.StatusOK, map[string]string{"message": "documento eliminado"})
}

// --- Task Status ---

type TaskResponse struct {
	ID              string      `json:"id"`
	Filename        string      `json:"filename"`
	Status          task.Status `json:"status"`
	Chunks          int         `json:"chunks"`
	CompletedChunks int         `json:"completed_chunks"`
	Progress        int         `json:"progress"`
	Error           string      `json:"error,omitempty"`
	CreatedAt       string      `json:"created_at"`
	UpdatedAt       string      `json:"updated_at"`
}

func (s *Server) handleTaskStatus(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	info, ok := s.taskMgr.Get(taskID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "tarea no encontrada"})
		return
	}

	writeJSON(w, http.StatusOK, TaskResponse{
		ID:              info.ID,
		Filename:        info.Filename,
		Status:          info.Status,
		Chunks:          info.Chunks,
		CompletedChunks: info.CompletedChunks,
		Progress:        info.Progress,
		Error:           info.Error,
		CreatedAt:       info.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:       info.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

// --- Usage Today ---

type UsageResponse struct {
	QueryCount  int  `json:"query_count"`
	DailyLimit  *int `json:"daily_limit"`
	Remaining   *int `json:"remaining"`
	IsUnlimited bool `json:"is_unlimited"`
}

func (s *Server) handleUsageToday(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	planCode := auth.NormalizePlanCode(auth.GetPlanCode(r.Context()))

	count := s.getTodayQueryCount(r.Context(), userID)

	resp := UsageResponse{
		QueryCount:  count,
		IsUnlimited: planCode == auth.PlanCodePremium,
	}

	if planCode != auth.PlanCodePremium {
		limit := 50
		remaining := limit - count
		if remaining < 0 {
			remaining = 0
		}
		resp.DailyLimit = &limit
		resp.Remaining = &remaining
	}

	writeJSON(w, http.StatusOK, resp)
}

// --- Chat ---

type ChatRequest struct {
	Query string `json:"query"`
}

type ChatMetadata struct {
	Fragments      int      `json:"fragments_recuperados"`
	Sources        []string `json:"fuentes"`
	AvgScore       float32  `json:"score_promedio"`
	ContextQuality string   `json:"calidad_contexto"`
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	planCode := auth.NormalizePlanCode(auth.GetPlanCode(r.Context()))

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "La consulta debe incluir un texto"})
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "La consulta no puede estar vacía"})
		return
	}

	ctx := r.Context()

	// Check daily quota
	if planCode != auth.PlanCodePremium {
		const limit = 50
		count := s.getTodayQueryCount(ctx, userID)
		if count >= limit {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{
				"error": "Límite diario de consultas alcanzado. Espera hasta mañana o actualiza tu plan.",
			})
			return
		}
	}

	// Step 1: Embed query
	slog.Info("iniciando_busqueda", "user_id", userID, "query", truncate(req.Query, 100))
	queryVector, _, err := s.embClient.Embed(ctx, []string{req.Query})
	if err != nil {
		slog.Error("error_embedding_query", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Analizando la consulta"})
		return
	}

	// Step 2: Semantic search filtered by user_id
	results, err := s.vsClient.SearchByUser(ctx, queryVector, userID, s.topK)
	if err != nil {
		slog.Error("error_busqueda", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Buscando coincidencias en tus documentos"})
		return
	}

	// Increment usage count
	s.incrementUsage(ctx, userID)

	// Step 3: Build metadata
	var sources []string
	var totalScore float32
	seen := make(map[string]bool)

	for _, res := range results {
		totalScore += res.Score
		if !seen[res.Filename] {
			seen[res.Filename] = true
			sources = append(sources, res.Filename)
		}
	}

	avgScore := float32(0)
	if len(results) > 0 {
		avgScore = totalScore / float32(len(results))
	}

	quality := "alta"
	if avgScore < 0.5 {
		quality = "baja - posible falta de contexto"
		slog.Warn("posible_falta_contexto", "user_id", userID, "score_promedio", avgScore)
	} else if avgScore < 0.7 {
		quality = "media"
	}

	metadata := ChatMetadata{
		Fragments:      len(results),
		Sources:        sources,
		AvgScore:       avgScore,
		ContextQuality: quality,
	}

	// Step 4: Build context for LLM
	var contextBuilder strings.Builder
	for i, res := range results {
		sectionInfo := ""
		if res.Section != "" {
			sectionInfo = " | Sección: " + res.Section
		}
		fmt.Fprintf(&contextBuilder, "[Fragmento %d | %s%s]: %s\n\n", i+1, res.Filename, sectionInfo, res.Text)
	}

	systemPrompt := `Eres un auditor experto. Responde ÚNICAMENTE basándote en los fragmentos proporcionados.
PRESTA ATENCIÓN a la SECCIÓN de cada fragmento: reglas de diferentes secciones aplican a contextos distintos.
No mezcles reglas de una sección con otra. Si un fragmento pertenece a "Sala de Alta Complejidad" 
y otro a "Quirófano General", son normas diferentes y debes usar la correcta según el caso.
Si la respuesta no está en los fragmentos, indícalo sin inventar.
Responde en español, de forma clara y concisa, citando la sección del tarifario que aplica.`

	userPrompt := fmt.Sprintf(`Contexto de documentos:
%s

Pregunta: %s

Responde basándote exclusivamente en el contexto proporcionado.`, contextBuilder.String(), req.Query)

	// Step 5: Stream LLM response with SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming no soportado"})
		return
	}

	// Send metadata as first event
	metaJSON, _ := json.Marshal(metadata)
	fmt.Fprintf(w, "event: metadata\ndata: %s\n\n", metaJSON)
	flusher.Flush()

	fullResponse, err := s.llmClient.GenerateStream(ctx, systemPrompt, userPrompt, w)
	if err != nil {
		slog.Error("error_generacion_llm", "error", err)
		errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", errJSON)
		flusher.Flush()
		return
	}

	slog.Info("chat_completado", "user_id", userID, "query_len", len(req.Query), "response_len", len(fullResponse))
}

// --- Usage helpers ---

func (s *Server) getTodayQueryCount(ctx context.Context, userID string) int {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT query_count FROM daily_usage
		 WHERE user_id = ? AND usage_date = DATE('now')`,
		userID,
	).Scan(&count)
	if err == sql.ErrNoRows {
		return 0
	}
	if err != nil {
		slog.Warn("consultando_uso_diario", "error", err)
		return 0
	}
	return count
}

func (s *Server) incrementUsage(ctx context.Context, userID string) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO daily_usage (user_id, usage_date, query_count)
		 VALUES (?, DATE('now'), 1)
		 ON CONFLICT(user_id, usage_date)
		 DO UPDATE SET query_count = query_count + 1`,
		userID,
	)
	if err != nil {
		slog.Error("incrementando_uso", "user_id", userID, "error", err)
	}
}

// --- Plan helpers ---

func maxUploadBytesByPlan(planCode string) int64 {
	switch auth.NormalizePlanCode(planCode) {
	case auth.PlanCodePremium:
		return 1024 << 20 // 1 GB
	default:
		return 100 << 20 // 100 MB
	}
}

// --- Util ---

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
