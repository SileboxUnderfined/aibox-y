package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestAnthropicURL(t *testing.T) {
	cases := map[string]string{
		"":                           "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com":  "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/": "https://api.anthropic.com/v1/messages",
		"https://proxy.example/v1":   "https://proxy.example/v1/messages",
		"https://proxy.example/v1/":  "https://proxy.example/v1/messages",
	}
	for base, want := range cases {
		if got := anthropicURL(base, "messages"); got != want {
			t.Errorf("%q: %q, want %q", base, got, want)
		}
	}
}

func TestAnthropicRequest(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "secret" || r.Header.Get("anthropic-version") != anthropicVersion {
			t.Errorf("request %s %v", r.URL.Path, r.Header)
		}
		var body struct {
			Model        string            `json:"model"`
			MaxTokens    int               `json:"max_tokens"`
			System       string            `json:"system"`
			OutputConfig map[string]string `json:"output_config"`
			Messages     []struct {
				Role    string        `json:"role"`
				Content []claudeBlock `json:"content"`
			} `json:"messages"`
		}
		data, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "claude" || body.MaxTokens != 100 || body.System != "sys" || body.OutputConfig["effort"] != "low" ||
			len(body.Messages) != 2 || body.Messages[1].Role != "assistant" || body.Messages[1].Content[0].Text != "привет" {
			t.Errorf("body = %s", data)
		}
		_, _ = io.WriteString(w, `{"stop_reason":"end_turn","content":[{"type":"text","text":" Ответ. "}]}`)
	}))
	defer srv.Close()
	t.Setenv("K", "secret")
	c, _ := New(Config{Type: "anthropic", BaseURL: srv.URL, Model: "claude", APIKeyEnv: "K", MaxTokens: 100, Effort: "low"})
	answer, err := c.Complete(context.Background(), "sys", []Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "привет"}})
	if err != nil || answer != "Ответ." || calls != 2 {
		t.Fatalf("answer %q, err %v, calls %d", answer, err, calls)
	}
}

func TestAnthropicErrorMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	}))
	defer srv.Close()
	t.Setenv("K", "bad")
	c, _ := New(Config{Type: "anthropic", BaseURL: srv.URL, Model: "claude", APIKeyEnv: "K"})
	_, err := c.Complete(context.Background(), "", []Message{{Role: "user", Content: "q"}})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Fatalf("err %v", err)
	}
}

func TestAnthropicModelsPaging(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.URL.Query().Get("after_id") == "" {
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-b"}],"has_more":true,"last_id":"claude-b"}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-a"}],"has_more":false,"last_id":"claude-a"}`)
	}))
	defer srv.Close()
	t.Setenv("K", "secret")
	ids, err := ListModels(context.Background(), Config{Type: "anthropic", BaseURL: srv.URL + "/v1", APIKeyEnv: "K"})
	if err != nil || !reflect.DeepEqual(ids, []string{"claude-a", "claude-b"}) {
		t.Fatalf("ids %v, err %v", ids, err)
	}
}
