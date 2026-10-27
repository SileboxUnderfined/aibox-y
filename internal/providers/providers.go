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
	// Title and Aliases are how the model is named by voice.
	Title   string   `json:"title,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
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
	search := searchFrom(ctx)
	native := ""
	if search.Mode == SearchNative {
		native = NativeSearch(o.cfg)
	}
	if native == "openai" {
		// OpenAI searches only through the Responses API.
		return o.responses(ctx, key, system, messages)
	}
	all := make([]chatMessage, 0, len(messages)+1)
	if system != "" {
		all = append(all, chatMessage{Role: "system", Content: system})
	}
	for _, m := range messages {
		all = append(all, chatMessage{Role: m.Role, Content: m.Content})
	}
	for round := 0; ; round++ {
		request := map[string]any{"model": o.cfg.Model, "messages": all, "max_tokens": o.cfg.MaxTokens}
		if search.Mode == SearchTool {
			request["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": searchToolName, "description": searchToolDescription, "parameters": searchToolParameters}}}
			if round >= maxSearchRounds {
				request["tool_choice"] = "none"
			}
		}
		if native == "openrouter" {
			request["plugins"] = []any{map[string]any{"id": "web", "max_results": 3}}
		}
		answer, err := o.chat(ctx, key, request)
		if err != nil {
			return "", err
		}
		if len(answer.ToolCalls) == 0 || search.Mode != SearchTool || round >= maxSearchRounds {
			text := strings.TrimSpace(answer.Content)
			if search.Mode != "" {
				text = speakable(text)
			}
			if text == "" {
				return "", errors.New("provider returned no answer")
			}
			return text, nil
		}
		all = append(all, chatMessage{Role: "assistant", Content: answer.Content, ReasoningContent: answer.ReasoningContent, ToolCalls: answer.ToolCalls})
		for _, call := range answer.ToolCalls {
			result := "Неизвестный инструмент."
			if call.Function.Name == searchToolName {
				result = runSearch(ctx, search.Searcher, call.Function.Arguments)
			}
			all = append(all, chatMessage{Role: "tool", ToolCallID: call.ID, Content: result})
		}
	}
}

// chatMessage is a Chat Completions message, tool calls included.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// DeepSeek wants its reasoning back while a tool call is in progress.
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (o *openAI) chat(ctx context.Context, key string, request map[string]any) (chatMessage, error) {
	data, err := o.post(ctx, key, "/chat/completions", request)
	if err != nil {
		return chatMessage{}, err
	}
	var out struct {
		Choices []struct {
			Message chatMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 {
		return chatMessage{}, errors.New("provider returned no answer")
	}
	return out.Choices[0].Message, nil
}

func (o *openAI) post(ctx context.Context, key, path string, request any) ([]byte, error) {
	body, _ := json.Marshal(request)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.cfg.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := o.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	return data, nil
}

// responses asks OpenAI through the Responses API with its hosted web search.
func (o *openAI) responses(ctx context.Context, key, system string, messages []Message) (string, error) {
	input := make([]any, 0, len(messages))
	for _, m := range messages {
		input = append(input, map[string]string{"role": m.Role, "content": m.Content})
	}
	// Reasoning and the search itself count against this budget too.
	budget := max(o.cfg.MaxTokens, 4096)
	request := map[string]any{
		"model": o.cfg.Model, "input": input, "store": false, "max_output_tokens": budget,
		"tools": []any{map[string]any{"type": "web_search", "user_location": map[string]string{"type": "approximate", "country": "RU"}}},
	}
	if system != "" {
		request["instructions"] = system
	}
	data, err := o.post(ctx, key, "/responses", request)
	if err != nil {
		return "", err
	}
	var out struct {
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", errors.New("provider returned no answer")
	}
	if out.Status != "completed" {
		return "", fmt.Errorf("provider response %s", out.Status)
	}
	var text strings.Builder
	for _, item := range out.Output {
		if item.Type != "message" || item.Role != "assistant" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" {
				text.WriteString(part.Text)
			}
		}
	}
	answer := speakable(text.String())
	if answer == "" {
		return "", errors.New("provider returned no answer")
	}
	return answer, nil
}
