package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

const openRouterEmbedURL = "https://openrouter.ai/api/v1/embeddings"

// Client wraps the HTTP client for generating embeddings via OpenRouter.
type Client struct {
	apiKey  string
	model   string
	http    *http.Client
}

// EmbeddingResponse matches the OpenAI-compatible embeddings API response.
type EmbeddingResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// NewClient creates a new embeddings client.
func NewClient(apiKey, model string) *Client {
	return &Client{
		apiKey: apiKey,
		model:  model,
		http: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Embed generates embeddings for a batch of texts.
func (c *Client) Embed(ctx context.Context, texts []string) ([]float64, int, error) {
	if len(texts) == 0 || len(texts) > 1 {
		return nil, 0, fmt.Errorf("embedding: solo se acepta 1 texto por llamada (batch interno)")
	}
	// For single-text embeddings (query mode), use a single string
	return c.embedSingle(ctx, texts[0])
}

// EmbedBatch generates embeddings for multiple texts in a single API call.
func (c *Client) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	payload := map[string]interface{}{
		"model": c.model,
		"input": texts,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("serializando payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openRouterEmbedURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creando request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "http://localhost:8080")
	req.Header.Set("X-Title", "SemanticCoreRAG")

	start := time.Now()
	resp, err := c.http.Do(req)
	elapsed := time.Since(start)

	if err != nil {
		return nil, fmt.Errorf("llamando API embeddings: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody bytes.Buffer
		errBody.ReadFrom(resp.Body)
		slog.Error("fallo_embedding", "status", resp.StatusCode, "body", errBody.String())
		return nil, fmt.Errorf("API embeddings devolvió status %d", resp.StatusCode)
	}

	var embResp EmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("decodificando respuesta embeddings: %w", err)
	}

	slog.Info("embeddings_generados", "textos", len(texts), "tokens", embResp.Usage.TotalTokens, "tiempo_ms", elapsed.Milliseconds())

	vectors := make([][]float64, len(embResp.Data))
	for _, d := range embResp.Data {
		vectors[d.Index] = d.Embedding
	}
	return vectors, nil
}

func (c *Client) embedSingle(ctx context.Context, text string) ([]float64, int, error) {
	vectors, err := c.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, 0, err
	}
	if len(vectors) == 0 {
		return nil, 0, fmt.Errorf("sin vectores devueltos")
	}
	return vectors[0], len(vectors[0]), nil
}
