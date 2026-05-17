package auth

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
)

type contextKey string

const (
	ctxUserID   contextKey = "user_id"
	ctxEmail    contextKey = "email"
	ctxRole     contextKey = "role"
	ctxPlanCode contextKey = "plan_code"
)

// RequireAuth returns middleware that validates JWT and injects user info into context.
func RequireAuth(service *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractToken(r)
			if token == "" {
				http.Error(w, `{"error":"token requerido"}`, http.StatusUnauthorized)
				return
			}

			claims, err := service.ValidateToken(token)
			if err != nil {
				slog.Warn("token_invalido", "error", err)
				http.Error(w, `{"error":"token inválido o expirado"}`, http.StatusUnauthorized)
				return
			}

			ctx := r.Context()
			ctx = context.WithValue(ctx, ctxUserID, claims.UserID)
			ctx = context.WithValue(ctx, ctxEmail, claims.Email)
			ctx = context.WithValue(ctx, ctxRole, claims.Role)
			ctx = context.WithValue(ctx, ctxPlanCode, claims.PlanCode)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdmin returns middleware that only allows admin role.
func RequireAdmin() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := GetRole(r.Context())
			if role != "admin" {
				http.Error(w, `{"error":"acceso denegado: solo administradores"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Context helpers
func GetUserID(ctx context.Context) string   { v, _ := ctx.Value(ctxUserID).(string); return v }
func GetEmail(ctx context.Context) string     { v, _ := ctx.Value(ctxEmail).(string); return v }
func GetRole(ctx context.Context) string      { v, _ := ctx.Value(ctxRole).(string); return v }
func GetPlanCode(ctx context.Context) string  { v, _ := ctx.Value(ctxPlanCode).(string); return v }

func extractToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	// Also check query param for SSE (EventSource doesn't support custom headers)
	if tok := r.URL.Query().Get("token"); tok != "" {
		return tok
	}
	return ""
}
