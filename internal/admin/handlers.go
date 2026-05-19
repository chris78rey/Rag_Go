package admin

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/codex/semantic-rag-go/internal/auth"
	"github.com/google/uuid"
)

// Handler manages admin API endpoints.
type Handler struct {
	db      *sql.DB
	authSvc *auth.Service
}

// NewHandler creates a new admin handler.
func NewHandler(db *sql.DB, authSvc *auth.Service) *Handler {
	return &Handler{db: db, authSvc: authSvc}
}

// --- Request/Response types ---

type CreateUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	FullName string `json:"full_name"`
	Role     string `json:"role"`
	PlanCode string `json:"plan_code"`
}

type UserResponse struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	FullName  string `json:"full_name"`
	Role      string `json:"role"`
	PlanCode  string `json:"plan_code"`
	Active    bool   `json:"active"`
	CreatedAt string `json:"created_at"`
}

type UpdateUserRequest struct {
	FullName *string `json:"full_name,omitempty"`
	Active   *bool   `json:"active,omitempty"`
}

type UpdatePlanRequest struct {
	PlanCode string `json:"plan_code"`
}

type UserUsageResponse struct {
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	UsageDate string `json:"usage_date"`
	Count     int    `json:"query_count"`
}

// --- Handlers ---

// CreateUser creates a new user (admin-only).
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body inválido"})
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email y password requeridos"})
		return
	}
	if len(req.Password) < 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password mínimo 6 caracteres"})
		return
	}

	role := "user"
	if req.Role == "admin" {
		role = "admin"
	}

	planCode := req.PlanCode
	if planCode != "basic" && planCode != "unlimited" {
		planCode = "basic"
	}

	hash, err := h.authSvc.HashPassword(req.Password)
	if err != nil {
		slog.Error("hash_password", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error interno"})
		return
	}

	var planID int
	err = h.db.QueryRowContext(r.Context(), "SELECT id FROM plans WHERE code = ?", planCode).Scan(&planID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plan no encontrado"})
		return
	}

	userID := uuid.New().String()
	_, err = h.db.ExecContext(r.Context(),
		`INSERT INTO users (id, email, password_hash, full_name, role, plan_id)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		userID, req.Email, hash, req.FullName, role, planID,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "email ya registrado"})
			return
		}
		slog.Error("creando_usuario", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error creando usuario"})
		return
	}

	slog.Info("usuario_creado", "user_id", userID, "email", req.Email, "role", role)

	writeJSON(w, http.StatusCreated, UserResponse{
		ID:       userID,
		Email:    req.Email,
		FullName: req.FullName,
		Role:     role,
		PlanCode: planCode,
		Active:   true,
	})
}

// ListUsers returns all users (admin-only).
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT u.id, u.email, u.full_name, u.role, p.code, u.active,
		        strftime('%Y-%m-%dT%H:%M:%fZ', u.created_at)
		 FROM users u JOIN plans p ON u.plan_id = p.id
		 ORDER BY u.created_at DESC`,
	)
	if err != nil {
		slog.Error("listando_usuarios", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error listando usuarios"})
		return
	}
	defer rows.Close()

	var users []UserResponse
	for rows.Next() {
		var u UserResponse
		if err := rows.Scan(&u.ID, &u.Email, &u.FullName, &u.Role, &u.PlanCode, &u.Active, &u.CreatedAt); err != nil {
			slog.Error("escaneando_usuario", "error", err)
			continue
		}
		users = append(users, u)
	}

	writeJSON(w, http.StatusOK, users)
}

// UpdateUser modifies a user's name or active status (admin-only).
func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	var req UpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body inválido"})
		return
	}

	if req.FullName != nil {
		_, err := h.db.ExecContext(r.Context(),
			"UPDATE users SET full_name = ? WHERE id = ?", *req.FullName, userID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "actualizando usuario"})
			return
		}
	}

	if req.Active != nil {
		_, err := h.db.ExecContext(r.Context(),
			"UPDATE users SET active = ? WHERE id = ?", *req.Active, userID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "actualizando usuario"})
			return
		}
	}

	slog.Info("usuario_actualizado", "user_id", userID)
	writeJSON(w, http.StatusOK, map[string]string{"message": "actualizado"})
}

// UpdatePlan changes a user's plan (admin-only).
func (h *Handler) UpdatePlan(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	var req UpdatePlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body inválido"})
		return
	}

	if req.PlanCode != "basic" && req.PlanCode != "unlimited" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plan inválido (basic | unlimited)"})
		return
	}

	var planID int
	err := h.db.QueryRowContext(r.Context(), "SELECT id FROM plans WHERE code = ?", req.PlanCode).Scan(&planID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plan no encontrado"})
		return
	}

	_, err = h.db.ExecContext(r.Context(),
		"UPDATE users SET plan_id = ? WHERE id = ?", planID, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "actualizando plan"})
		return
	}

	slog.Info("plan_actualizado", "user_id", userID, "plan", req.PlanCode)
	writeJSON(w, http.StatusOK, map[string]string{"message": "plan actualizado"})
}

// GetUsage returns daily usage for all users (admin-only).
func (h *Handler) GetUsage(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT du.user_id, u.email, du.usage_date, du.query_count
		 FROM daily_usage du
		 JOIN users u ON u.id = du.user_id
		 WHERE du.usage_date = DATE('now')
		 ORDER BY du.query_count DESC`,
	)
	if err != nil {
		slog.Error("consultando_uso", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error consultando uso"})
		return
	}
	defer rows.Close()

	var usages []UserUsageResponse
	for rows.Next() {
		var u UserUsageResponse
		if err := rows.Scan(&u.UserID, &u.Email, &u.UsageDate, &u.Count); err != nil {
			continue
		}
		usages = append(usages, u)
	}

	writeJSON(w, http.StatusOK, usages)
}

// GetUserDocuments returns documents for a specific user (admin-only).
func (h *Handler) GetUserDocuments(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, original_filename, size_bytes, status, chunks,
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
		CreatedAt        string `json:"created_at"`
	}

	var docs []DocResponse
	for rows.Next() {
		var d DocResponse
		if err := rows.Scan(&d.ID, &d.OriginalFilename, &d.SizeBytes, &d.Status, &d.Chunks, &d.CreatedAt); err != nil {
			continue
		}
		docs = append(docs, d)
	}

	writeJSON(w, http.StatusOK, docs)
}

// --- System Settings ---

type SettingsRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// UpdateSetting creates or updates a system setting (admin-only).
func (h *Handler) UpdateSetting(w http.ResponseWriter, r *http.Request) {
	var req SettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body inválido"})
		return
	}

	if req.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key requerido"})
		return
	}

	_, err := h.db.ExecContext(r.Context(),
		`INSERT INTO system_settings (key, value, updated_at)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		req.Key, req.Value,
	)
	if err != nil {
		slog.Error("actualizando_setting", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error guardando"})
		return
	}

	slog.Info("setting_actualizado", "key", req.Key, "value", req.Value)
	writeJSON(w, http.StatusOK, map[string]string{"message": "actualizado"})
}

// GetSettings returns all system settings (admin-only).
func (h *Handler) GetSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT key, value, updated_at FROM system_settings ORDER BY key`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error consultando"})
		return
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var key, value string
		var updated string
		if err := rows.Scan(&key, &value, &updated); err != nil {
			continue
		}
		settings[key] = value
	}

	writeJSON(w, http.StatusOK, settings)
}

// GetPublicSetting returns a specific setting for authenticated users.
func (h *Handler) GetPublicSetting(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	var value string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT value FROM system_settings WHERE key = ?", key,
	).Scan(&value)

	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no encontrado"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"key": key, "value": value})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
