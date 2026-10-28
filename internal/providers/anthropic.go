package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The Messages API is called directly: the official SDK alone made up about
// a quarter of the binary, and only a handful of fields is needed here.

const anthropicVersion = "2023-06-01"

type claude struct {
	cfg  Config
	http *http.Client
}

func newAnthropic(cfg Config, httpClient *http.Client) *claude {
	return &claude{cfg: cfg, http: httpClient}
}

// claudeMessage is a request message. Content is either a list of blocks we
// build or the blocks of a previous answer, sent back as they came.
type claudeMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type claudeResponse struct {
	StopReason string            `json:"stop_reason"`
	Content    []json.RawMessage `json:"content"`
}

func (c *claude) Complete(ctx context.Context, system string, messages []Message) (string, error) {
	key, err := apiKey(c.cfg)
	if err != nil {
		return "", err
	}
	request := map[string]any{"model": c.cfg.Model, "max_tokens": c.cfg.MaxTokens}
	if system != "" {
		request["system"] = system
	}
	if c.cfg.Effort != "" {
		request["output_config"] = map[string]string{"effort": c.cfg.Effort}
	}
	all := make([]claudeMessage, 0, len(messages))
	for _, m := range messages {
		role := "user"
		if m.Role == "assistant" {
			role = "assistant"
		}
		all = append(all, claudeMessage{Role: role, Content: []claudeBlock{{Type: "text", Text: m.Content}}})
	}
	search := searchFrom(ctx)
	switch {
	case search.Mode == SearchTool:
		request["tools"] = []any{map[string]any{"name": searchToolName, "description": searchToolDescription, "input_schema": searchToolParameters}}
	case search.Mode == SearchNative && NativeSearch(c.cfg) == "anthropic":
		request["tools"] = []any{map[string]any{
			"type": "web_search_20250305", "name": "web_search", "max_uses": maxSearchRounds,
			"user_location": map[string]string{"type": "approximate", "country": "RU"},
		}}
	}
	for round := 0; ; round++ {
		if round >= maxSearchRounds && search.Mode == SearchTool {
			request["tool_choice"] = map[string]string{"type": "none"}
		}
		request["messages"] = all
		resp, err := c.messages(ctx, key, request)
		if err != nil {
			return "", err
		}
		if resp.StopReason == "refusal" {
			return "", errors.New("model declined the request")
		}
		continueTurn := resp.StopReason == "pause_turn"
		var results []claudeBlock
		if resp.StopReason == "tool_use" && search.Mode == SearchTool {
			for _, raw := range resp.Content {
				var use claudeBlock
				if json.Unmarshal(raw, &use) != nil || use.Type != "tool_use" {
					continue
				}
				result := "Неизвестный инструмент."
				if use.Name == searchToolName {
					result = runSearch(ctx, search.Searcher, string(use.Input))
				}
				results = append(results, claudeBlock{Type: "tool_result", ToolUseID: use.ID, Content: result})
			}
		}
		if (continueTurn || len(results) > 0) && round < 2*maxSearchRounds {
			all = append(all, claudeMessage{Role: "assistant", Content: resp.Content})
			if len(results) > 0 {
				all = append(all, claudeMessage{Role: "user", Content: results})
			}
			continue
		}
		return claudeAnswer(resp, search.Mode != "")
	}
}

// claudeAnswer is the text the model wrote after its last search; what it
// said before searching ("сейчас поищу") is not part of the answer.
func claudeAnswer(resp claudeResponse, searched bool) (string, error) {
	var parts []string
	for _, raw := range resp.Content {
		var b claudeBlock
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "server_tool_use", "web_search_tool_result", "tool_use":
			parts = nil
		}
	}
	answer := strings.TrimSpace(strings.Join(parts, ""))
	if searched {
		answer = speakable(answer)
	}
	if answer == "" {
		return "", errors.New("provider returned no answer")
	}
	return answer, nil
}

func (c *claude) messages(ctx context.Context, key string, request map[string]any) (claudeResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return claudeResponse{}, err
	}
	data, err := anthropicDo(ctx, c.http, http.MethodPost, anthropicURL(c.cfg.BaseURL, "messages"), key, body)
	if err != nil {
		return claudeResponse{}, err
	}
	var out claudeResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return claudeResponse{}, errors.New("provider returned no answer")
	}
	return out, nil
}

// anthropicURL accepts the address with or without /v1, the way the panel
// asks for it ("адрес до /v1") and the way the SDK took it (without).
func anthropicURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + "/" + path
}

// anthropicDo sends one request and retries twice on overload and network
// errors, as the SDK did.
func anthropicDo(ctx context.Context, client *http.Client, method, address, key string, body []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, address, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", anthropicVersion)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			lastErr = err
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			return data, nil
		case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusConflict,
			resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
			lastErr = anthropicError(resp.StatusCode, data)
		default:
			return nil, anthropicError(resp.StatusCode, data)
		}
	}
	return nil, lastErr
}

func anthropicError(status int, data []byte) error {
	var out struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &out) == nil && out.Error.Message != "" {
		return fmt.Errorf("provider returned HTTP %d: %s: %s", status, out.Error.Type, out.Error.Message)
	}
	return fmt.Errorf("provider returned HTTP %d", status)
}

// anthropicModels lists every model, page by page.
func anthropicModels(ctx context.Context, cfg Config, key string) ([]string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	var ids []string
	after := ""
	for page := 0; page < 20; page++ {
		q := url.Values{"limit": {"1000"}}
		if after != "" {
			q.Set("after_id", after)
		}
		data, err := anthropicDo(ctx, client, http.MethodGet, anthropicURL(cfg.BaseURL, "models")+"?"+q.Encode(), key, nil)
		if err != nil {
			return nil, err
		}
		var out struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, errors.New("unexpected model list")
		}
		for _, m := range out.Data {
			ids = append(ids, m.ID)
		}
		if !out.HasMore || out.LastID == "" {
			break
		}
		after = out.LastID
	}
	return ids, nil
}
