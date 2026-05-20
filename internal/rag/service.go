package rag

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/codex/semantic-rag-go/internal/auth"
	"github.com/codex/semantic-rag-go/internal/embeddings"
	"github.com/codex/semantic-rag-go/internal/llm"
	"github.com/codex/semantic-rag-go/internal/vectorstore"
)

var (
	ErrEmptyQuery                 = errors.New("consulta vacia")
	ErrSubscriptionExpired        = errors.New("suscripcion expirada")
	ErrDailyLimitExceeded         = errors.New("limite diario alcanzado")
	ErrTelegramNotLinked          = errors.New("telegram no vinculado")
	ErrInvalidTelegramCredentials = errors.New("credenciales telegram invalidas")
	ErrTelegramAccountInactive    = errors.New("cuenta telegram inactiva")
)

const normalDailyQueryLimit = 50

// Service centraliza el flujo RAG reutilizable entre HTTP y Telegram.
type Service struct {
	db        *sql.DB
	authSvc   *auth.Service
	embClient *embeddings.Client
	llmClient *llm.Client
	vsClient  *vectorstore.Client
	topK      uint64
}

// ChatMetadata resume la calidad del contexto recuperado.
type ChatMetadata struct {
	Fragments      int      `json:"fragments_recuperados"`
	Sources        []string `json:"fuentes"`
	AvgScore       float32  `json:"score_promedio"`
	ContextQuality string   `json:"calidad_contexto"`
}

// PreparedChat contiene el prompt y la metadata listos para generar la respuesta.
type PreparedChat struct {
	UserID       string
	Query        string
	Metadata     ChatMetadata
	SystemPrompt string
	UserPrompt   string
}

// NewService crea el servicio compartido de consultas.
func NewService(
	db *sql.DB,
	authSvc *auth.Service,
	embClient *embeddings.Client,
	llmClient *llm.Client,
	vsClient *vectorstore.Client,
	topK uint64,
) *Service {
	return &Service{
		db:        db,
		authSvc:   authSvc,
		embClient: embClient,
		llmClient: llmClient,
		vsClient:  vsClient,
		topK:      topK,
	}
}

// PrepareChat valida el acceso del usuario, recupera contexto semantico y arma el prompt final.
func (s *Service) PrepareChat(ctx context.Context, userID, query string) (*PreparedChat, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrEmptyQuery
	}

	if s.IsSubscriptionExpired(ctx, userID) {
		return nil, ErrSubscriptionExpired
	}

	planCode, err := s.getUserPlanCode(ctx, userID)
	if err != nil {
		return nil, err
	}

	if auth.NormalizePlanCode(planCode) != auth.PlanCodePremium {
		count := s.TodayQueryCount(ctx, userID)
		if count >= normalDailyQueryLimit {
			return nil, ErrDailyLimitExceeded
		}
	}

	slog.Info("iniciando_busqueda", "user_id", userID, "query", truncate(query, 100))
	queryVector, _, err := s.embClient.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("analizando la consulta: %w", err)
	}

	results, err := s.vsClient.SearchByUser(ctx, queryVector, userID, s.topK)
	if err != nil {
		return nil, fmt.Errorf("buscando coincidencias en tus documentos: %w", err)
	}

	s.incrementUsage(ctx, userID)

	metadata := buildChatMetadata(userID, results)
	systemPrompt, userPrompt := buildChatPrompts(query, results)

	return &PreparedChat{
		UserID:       userID,
		Query:        query,
		Metadata:     metadata,
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
	}, nil
}

// GenerateAnswer llama al LLM y devuelve la respuesta completa.
func (s *Service) GenerateAnswer(ctx context.Context, prepared *PreparedChat, writer io.Writer) (string, error) {
	if prepared == nil {
		return "", errors.New("consulta preparada requerida")
	}
	if writer == nil {
		writer = io.Discard
	}
	return s.llmClient.GenerateStream(ctx, prepared.SystemPrompt, prepared.UserPrompt, writer)
}

// LinkTelegramAccount vincula un chat de Telegram con un usuario valido.
func (s *Service) LinkTelegramAccount(ctx context.Context, chatID int64, email, password string) (string, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	password = strings.TrimSpace(password)
	if email == "" || password == "" {
		return "", ErrInvalidTelegramCredentials
	}

	var (
		userID       string
		passwordHash string
		active       int
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT id, password_hash, active
		 FROM users
		 WHERE email = ?`,
		email,
	).Scan(&userID, &passwordHash, &active)
	if err == sql.ErrNoRows {
		return "", ErrInvalidTelegramCredentials
	}
	if err != nil {
		return "", fmt.Errorf("verificando credenciales de telegram: %w", err)
	}

	if active == 0 {
		return "", ErrTelegramAccountInactive
	}

	if !s.authSvc.CheckPassword(passwordHash, password) {
		return "", ErrInvalidTelegramCredentials
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO user_telegram_links (telegram_chat_id, user_id, created_at)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(telegram_chat_id) DO UPDATE SET
		     user_id = excluded.user_id,
		     created_at = CURRENT_TIMESTAMP`,
		chatID, userID,
	)
	if err != nil {
		return "", fmt.Errorf("guardando enlace telegram: %w", err)
	}

	return userID, nil
}

// UnlinkTelegramAccount elimina el enlace entre un chat y un usuario.
func (s *Service) UnlinkTelegramAccount(ctx context.Context, chatID int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM user_telegram_links WHERE telegram_chat_id = ?`,
		chatID,
	)
	if err != nil {
		return fmt.Errorf("eliminando enlace telegram: %w", err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("verificando enlace telegram: %w", err)
	}
	if rows == 0 {
		return ErrTelegramNotLinked
	}

	return nil
}

// TelegramUserID resuelve el usuario vinculado a un chat de Telegram.
func (s *Service) TelegramUserID(ctx context.Context, chatID int64) (string, error) {
	var userID string
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id
		 FROM user_telegram_links
		 WHERE telegram_chat_id = ?`,
		chatID,
	).Scan(&userID)
	if err == sql.ErrNoRows {
		return "", ErrTelegramNotLinked
	}
	if err != nil {
		return "", fmt.Errorf("consultando enlace telegram: %w", err)
	}

	return userID, nil
}

// TodayQueryCount devuelve cuantas consultas llevo hoy ese usuario.
func (s *Service) TodayQueryCount(ctx context.Context, userID string) int {
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

// IsSubscriptionExpired verifica si la suscripcion del usuario ya vencio.
func (s *Service) IsSubscriptionExpired(ctx context.Context, userID string) bool {
	var expired bool
	err := s.db.QueryRowContext(ctx,
		`SELECT CASE
			WHEN COALESCE(subscription_expires_at, '') = '' THEN 1
			ELSE datetime(subscription_expires_at) < datetime('now')
		END
		 FROM users
		 WHERE id = ?`,
		userID,
	).Scan(&expired)
	if err != nil {
		slog.Warn("verificando_suscripcion", "user_id", userID, "error", err)
		return true
	}
	return expired
}

func (s *Service) getUserPlanCode(ctx context.Context, userID string) (string, error) {
	var planCode string
	err := s.db.QueryRowContext(ctx,
		`SELECT p.code
		 FROM users u
		 JOIN plans p ON u.plan_id = p.id
		 WHERE u.id = ?`,
		userID,
	).Scan(&planCode)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("usuario no encontrado")
	}
	if err != nil {
		return "", fmt.Errorf("consultando plan de usuario: %w", err)
	}

	return planCode, nil
}

func (s *Service) incrementUsage(ctx context.Context, userID string) {
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

func buildChatMetadata(userID string, results []vectorstore.SearchResult) ChatMetadata {
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

	return ChatMetadata{
		Fragments:      len(results),
		Sources:        sources,
		AvgScore:       avgScore,
		ContextQuality: quality,
	}
}

func buildChatPrompts(query string, results []vectorstore.SearchResult) (string, string) {
	var contextBuilder strings.Builder
	for i, res := range results {
		sectionInfo := ""
		if res.Section != "" {
			sectionInfo = " | Seccion: " + res.Section
		}
		fmt.Fprintf(&contextBuilder, "[Fuente %d | %s%s]: %s\n\n", i+1, res.Filename, sectionInfo, res.Text)
	}

	systemPrompt := `Eres un auditor experto. Responde unicamente con base en la informacion proporcionada.
Presta atencion a la seccion de cada fuente: reglas de diferentes secciones aplican a contextos distintos.
No mezcles reglas de una seccion con otra. Si una fuente pertenece a una seccion y otra a otra, usa la correcta segun el caso.
Si la respuesta no esta en la informacion proporcionada, indicarlo sin inventar.
Responde en espanol con una estructura ordenada y facil de copiar:
1. Respuesta directa.
2. Desarrollo con los puntos relevantes.
3. Documentos consultados.
4. Observaciones o limitaciones, si aplica.
Usa viñetas cuando haya varios elementos y evita mezclar ideas en un solo párrafo.
No uses la palabra "fragmento" en la respuesta al usuario; prefiere "fuente", "documento" o "seccion" segun corresponda.`

	userPrompt := fmt.Sprintf(`Contexto de documentos:
%s

Pregunta: %s

Responde basandote exclusivamente en el contexto proporcionado.`, contextBuilder.String(), query)

	return systemPrompt, userPrompt
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
