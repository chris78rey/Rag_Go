package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const openRouterChatURL = "https://openrouter.ai/api/v1/chat/completions"

// Client wraps the HTTP client for LLM generation via OpenRouter.
type Client struct {
	apiKey string
	model  string
	http   *http.Client
}

// Message represents a chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// StreamEvent represents a single token in the SSE stream.
type StreamEvent struct {
	Content string `json:"content"`
	Done    bool   `json:"done"`
	Error   string `json:"error,omitempty"`
}

// NewClient creates a new LLM client.
func NewClient(apiKey, model string) *Client {
	return &Client{
		apiKey: apiKey,
		model:  model,
		http: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// GenerateStream sends a streaming chat completion request and writes SSE events to the writer.
// It returns the full accumulated response text and any error.
func (c *Client) GenerateStream(ctx context.Context, systemPrompt, userPrompt string, writer io.Writer) (string, error) {
	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	// Build HTTP-only messages (no extra fields)
	apiMessages := make([]map[string]string, len(messages))
	for i, m := range messages {
		apiMessages[i] = map[string]string{
			"role":    m.Role,
			"content": m.Content,
		}
	}

	payload := map[string]interface{}{
		"model":    c.model,
		"messages": apiMessages,
		"stream":   true,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("serializando payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openRouterChatURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creando request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "http://localhost:8080")
	req.Header.Set("X-Title", "SemanticCoreRAG")

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llamando API LLM: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody bytes.Buffer
		errBody.ReadFrom(resp.Body)
		slog.Error("error_llm", "status", resp.StatusCode, "body", errBody.String())
		return "", fmt.Errorf("API LLM devolvió status %d: %s", resp.StatusCode, errBody.String())
	}

	flusher, _ := writer.(http.Flusher)
	var fullResponse strings.Builder

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "data: [DONE]" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			slog.Warn("parse_error_sse", "data", data, "error", err)
			continue
		}

		done := false
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				fullResponse.WriteString(choice.Delta.Content)
				// Write SSE event
				event, _ := json.Marshal(StreamEvent{Content: choice.Delta.Content})
				fmt.Fprintf(writer, "data: %s\n\n", event)
				if flusher != nil {
					flusher.Flush()
				}
			}
			if choice.FinishReason != nil {
				done = true
			}
		}

		if done {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Error("error_leyendo_sse", "error", err)
		return fullResponse.String(), fmt.Errorf("leyendo stream SSE: %w", err)
	}

	// Send final done event
	doneEvent, _ := json.Marshal(StreamEvent{Done: true})
	fmt.Fprintf(writer, "data: %s\n\n", doneEvent)
	if flusher != nil {
		flusher.Flush()
	}

	slog.Info("generacion_completada", "tokens_respuesta", len(fullResponse.String()), "tiempo_ms", time.Since(start).Milliseconds())
	return fullResponse.String(), nil
}
