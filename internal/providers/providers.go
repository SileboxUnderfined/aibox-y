// Package providers talks to LLM APIs: OpenAI-compatible Chat Completions
// (OpenAI, DeepSeek, OpenRouter and similar) and the Anthropic Messages API.
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Completer interface {
	Complete(ctx context.Context, system string, messages []Message) (string, error)
}

type Config struct {
	Type      string `json:"type"` // "openai" or "anthropic"
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	APIKeyEnv string `json:"api_key_env"`
	MaxTokens int    `json:"max_tokens"`
	// Effort is passed to Anthropic models as output_config.effort.
	Effort string `json:"effort,omitempty"`
}

var ErrNoKey = errors.New("API key is not set")

func New(cfg Config) (Completer, error) {
	if cfg.Model == "" {
		return nil, errors.New("provider model is empty")
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 1024
	}
	httpClient := &http.Client{Timeout: 90 * time.Second}
	switch cfg.Type {
	case "", "openai":
		if cfg.BaseURL == "" {
			return nil, errors.New("openai provider needs base_url")
		}
		return &openAI{cfg: cfg, http: httpClient}, nil
	case "anthropic":
		return newAnthropic(cfg), nil
	default:
		return nil, fmt.Errorf("unknown provider type %q", cfg.Type)
	}
}

func apiKey(cfg Config) (string, error) {
	key := strings.TrimSpace(os.Getenv(cfg.APIKeyEnv))
	if cfg.APIKeyEnv == "" || key == "" {
		return "", ErrNoKey
	}
	return key, nil
}

type openAI struct {
	cfg  Config
	http *http.Client
}

func (o *openAI) Complete(ctx context.Context, system string, messages []Message) (string, error) {
	key, err := apiKey(o.cfg)
	if err != nil {
		return "", err
	}
	all := make([]Message, 0, len(messages)+1)
	if system != "" {
		all = append(all, Message{Role: "system", Content: system})
	}
	all = append(all, messages...)
	body, _ := json.Marshal(map[string]any{"model": o.cfg.Model, "messages": all, "max_tokens": o.cfg.MaxTokens})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := o.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 {
		return "", errors.New("provider returned no answer")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}
