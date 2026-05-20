package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/codex/semantic-rag-go/internal/rag"
)

const telegramAPIBase = "https://api.telegram.org/bot"

// Bot consume updates de Telegram y delega las consultas al servicio RAG.
type Bot struct {
	token  string
	ragSvc *rag.Service
	client *http.Client
}

type updateEnvelope struct {
	OK     bool     `json:"ok"`
	Result []update `json:"result"`
}

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message,omitempty"`
}

type message struct {
	MessageID int64 `json:"message_id"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text string `json:"text"`
}

type sendMessageRequest struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

// New creates a Telegram bot wrapper.
func New(token string, ragSvc *rag.Service) *Bot {
	return &Bot{
		token:  strings.TrimSpace(token),
		ragSvc: ragSvc,
		client: &http.Client{Timeout: 45 * time.Second},
	}
}

// Run ejecuta el long polling hasta que se cancele el contexto.
func (b *Bot) Run(ctx context.Context) {
	if b.token == "" {
		slog.Info("telegram_bot_desactivado", "razon", "TELEGRAM_BOT_TOKEN no configurado")
		return
	}
	if b.ragSvc == nil {
		slog.Warn("telegram_bot_no_iniciado", "razon", "servicio rag no disponible")
		return
	}

	slog.Info("telegram_bot_iniciado")
	offset := int64(0)

	for {
		if ctx.Err() != nil {
			slog.Info("telegram_bot_detenido")
			return
		}

		updates, err := b.poll(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				slog.Info("telegram_bot_detenido")
				return
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				continue
			}
			slog.Warn("telegram_poll_error", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		for _, upd := range updates {
			if upd.UpdateID >= offset {
				offset = upd.UpdateID + 1
			}
			if upd.Message == nil {
				continue
			}
			text := strings.TrimSpace(upd.Message.Text)
			if text == "" {
				continue
			}

			go b.handleUpdate(ctx, upd.Message.Chat.ID, text)
		}
	}
}

func (b *Bot) poll(ctx context.Context, offset int64) ([]update, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()

	url := fmt.Sprintf("%s%s/getUpdates?timeout=30&offset=%d", telegramAPIBase, b.token, offset)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var env updateEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, err
	}
	if !env.OK {
		return nil, fmt.Errorf("telegram getUpdates devolvio ok=false")
	}

	return env.Result, nil
}

func (b *Bot) handleUpdate(ctx context.Context, chatID int64, text string) {
	cmd, rest := splitCommand(text)

	switch cmd {
	case "/start", "/help":
		_ = b.sendMessage(ctx, chatID, helpMessage())
		return
	case "/login":
		email, password, ok := splitLoginArgs(rest)
		if !ok {
			_ = b.sendMessage(ctx, chatID, "Formato incorrecto. Usa:\n/login tu_correo@ejemplo.com tu_contrasena")
			return
		}

		userID, err := b.ragSvc.LinkTelegramAccount(ctx, chatID, email, password)
		switch {
		case err == nil:
			_ = b.sendMessage(ctx, chatID, fmt.Sprintf("Cuenta vinculada con exito. Usuario: %s", userID))
		case errors.Is(err, rag.ErrTelegramAccountInactive):
			_ = b.sendMessage(ctx, chatID, "Tu cuenta esta desactivada.")
		case errors.Is(err, rag.ErrInvalidTelegramCredentials):
			_ = b.sendMessage(ctx, chatID, "Credenciales invalidas.")
		default:
			slog.Warn("telegram_login_error", "chat_id", chatID, "error", err)
			_ = b.sendMessage(ctx, chatID, "Error interno al vincular la cuenta.")
		}
		return
	case "/unlink":
		if err := b.ragSvc.UnlinkTelegramAccount(ctx, chatID); err != nil {
			if errors.Is(err, rag.ErrTelegramNotLinked) {
				_ = b.sendMessage(ctx, chatID, "No tienes una cuenta vinculada.")
				return
			}
			slog.Warn("telegram_unlink_error", "chat_id", chatID, "error", err)
			_ = b.sendMessage(ctx, chatID, "Error interno al desvincular la cuenta.")
			return
		}
		_ = b.sendMessage(ctx, chatID, "Cuenta desvinculada.")
		return
	default:
		if strings.HasPrefix(cmd, "/") {
			_ = b.sendMessage(ctx, chatID, helpMessage())
			return
		}
	}

	userID, err := b.ragSvc.TelegramUserID(ctx, chatID)
	if err != nil {
		if errors.Is(err, rag.ErrTelegramNotLinked) {
			_ = b.sendMessage(ctx, chatID, helpMessage())
			return
		}
		slog.Warn("telegram_link_lookup_error", "chat_id", chatID, "error", err)
		_ = b.sendMessage(ctx, chatID, "Error verificando tu cuenta vinculada.")
		return
	}

	prepared, err := b.ragSvc.PrepareChat(ctx, userID, text)
	switch {
	case err == nil:
	case errors.Is(err, rag.ErrSubscriptionExpired):
		_ = b.sendMessage(ctx, chatID, "Tu suscripcion ha expirado. Renueva para seguir usando el bot.")
		return
	case errors.Is(err, rag.ErrDailyLimitExceeded):
		_ = b.sendMessage(ctx, chatID, "Has alcanzado tu limite diario de consultas.")
		return
	case errors.Is(err, rag.ErrEmptyQuery):
		return
	default:
		slog.Warn("telegram_prepare_error", "chat_id", chatID, "user_id", userID, "error", err)
		_ = b.sendMessage(ctx, chatID, "Error analizando tu consulta.")
		return
	}

	answer, err := b.ragSvc.GenerateAnswer(ctx, prepared, io.Discard)
	if err != nil {
		slog.Warn("telegram_llm_error", "chat_id", chatID, "user_id", userID, "error", err)
		_ = b.sendMessage(ctx, chatID, "Error generando la respuesta.")
		return
	}
	if strings.TrimSpace(answer) == "" {
		answer = "No se encontro una respuesta util con el contexto disponible."
	}

	for _, chunk := range splitTelegramMessage(answer, 3500) {
		if err := b.sendMessage(ctx, chatID, chunk); err != nil {
			slog.Warn("telegram_send_error", "chat_id", chatID, "error", err)
			return
		}
	}
}

func (b *Bot) sendMessage(ctx context.Context, chatID int64, text string) error {
	payload := sendMessageRequest{
		ChatID: chatID,
		Text:   text,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	url := fmt.Sprintf("%s%s/sendMessage", telegramAPIBase, b.token)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, resp.Body)
		return fmt.Errorf("telegram sendMessage status %d: %s", resp.StatusCode, strings.TrimSpace(buf.String()))
	}

	return nil
}

func splitCommand(text string) (string, string) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return "", ""
	}

	cmd := parts[0]
	rest := strings.TrimSpace(strings.TrimPrefix(text, cmd))
	cmd = strings.ToLower(strings.SplitN(cmd, "@", 2)[0])
	return cmd, rest
}

func splitLoginArgs(rest string) (string, string, bool) {
	parts := strings.SplitN(strings.TrimSpace(rest), " ", 2)
	if len(parts) != 2 {
		return "", "", false
	}

	email := strings.TrimSpace(parts[0])
	password := strings.TrimSpace(parts[1])
	if email == "" || password == "" {
		return "", "", false
	}
	return email, password, true
}

func splitTelegramMessage(text string, maxLen int) []string {
	if maxLen <= 0 {
		return []string{text}
	}

	runes := []rune(text)
	if len(runes) <= maxLen {
		return []string{text}
	}

	chunks := make([]string, 0, (len(runes)/maxLen)+1)
	for start := 0; start < len(runes); start += maxLen {
		end := start + maxLen
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}

func helpMessage() string {
	return "Comandos disponibles:\n/start o /help\n/login tu_correo@ejemplo.com tu_contrasena\n/unlink\n\nTambien puedes escribir una pregunta normal despues de vincular tu cuenta."
}
