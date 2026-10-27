package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeSearch struct{ queries []string }

func (f *fakeSearch) Search(_ context.Context, q string) (string, error) {
	f.queries = append(f.queries, q)
	return "1. Погода (gismeteo.ru): в Москве плюс пять", nil
}

func TestOpenAIToolLoop(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"думаю","tool_calls":[{"id":"c1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"погода москва\"}"}}]}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"В Москве плюс пять [1], подробнее: https://gismeteo.ru"}}]}`)
	}))
	defer srv.Close()
	t.Setenv("K", "secret")
	c, _ := New(Config{BaseURL: srv.URL, Model: "deepseek-chat", APIKeyEnv: "K"})
	s := &fakeSearch{}
	answer, err := c.Complete(WithSearch(context.Background(), Search{Mode: SearchTool, Searcher: s}), "sys", []Message{{Role: "user", Content: "Какая погода?"}})
	if err != nil {
		t.Fatal(err)
	}
	if answer != "В Москве плюс пять, подробнее:" {
		t.Fatalf("answer = %q", answer)
	}
	if len(s.queries) != 1 || s.queries[0] != "погода москва" {
		t.Fatalf("queries = %v", s.queries)
	}
	if len(bodies) != 2 || bodies[0]["tools"] == nil {
		t.Fatalf("requests = %v", bodies)
	}
	msgs := bodies[1]["messages"].([]any)
	call := msgs[2].(map[string]any)
	result := msgs[3].(map[string]any)
	if call["reasoning_content"] != "думаю" || result["role"] != "tool" || result["tool_call_id"] != "c1" || !strings.Contains(result["content"].(string), "плюс пять") {
		t.Fatalf("second request messages = %v", msgs)
	}
}

func TestOpenAIStopsSearching(t *testing.T) {
	n := 0
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		data, _ := io.ReadAll(r.Body)
		last = nil
		_ = json.Unmarshal(data, &last)
		if last["tool_choice"] == "none" {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Не нашла точных данных."}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"c","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"x\"}"}}]}}]}`)
	}))
	defer srv.Close()
	t.Setenv("K", "secret")
	c, _ := New(Config{BaseURL: srv.URL, Model: "m", APIKeyEnv: "K"})
	answer, err := c.Complete(WithSearch(context.Background(), Search{Mode: SearchTool, Searcher: &fakeSearch{}}), "", []Message{{Role: "user", Content: "q"}})
	if err != nil || answer != "Не нашла точных данных." || n != maxSearchRounds+1 {
		t.Fatalf("answer %q, err %v, requests %d", answer, err, n)
	}
}

func TestOpenAINoSearchWithoutContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if strings.Contains(string(data), "tools") || strings.Contains(string(data), "plugins") {
			t.Errorf("search sent without WithSearch: %s", data)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Привет [1]"}}]}`)
	}))
	defer srv.Close()
	t.Setenv("K", "secret")
	c, _ := New(Config{BaseURL: srv.URL + "/openrouter.ai", Model: "m", APIKeyEnv: "K"})
	answer, _ := c.Complete(context.Background(), "", []Message{{Role: "user", Content: "q"}})
	if answer != "Привет [1]" {
		t.Fatalf("answer changed without search: %q", answer)
	}
}

func TestOpenRouterNativePlugin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(data), `"plugins":[{"id":"web"`) {
			t.Errorf("no web plugin: %s", data)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Курс 80 рублей ([cbr.ru](https://cbr.ru))."}}]}`)
	}))
	defer srv.Close()
	t.Setenv("K", "secret")
	// NativeSearch recognizes OpenRouter by its address.
	c, _ := New(Config{BaseURL: srv.URL + "/openrouter.ai/api/v1", Model: "m", APIKeyEnv: "K"})
	answer, err := c.Complete(WithSearch(context.Background(), Search{Mode: SearchNative}), "", []Message{{Role: "user", Content: "q"}})
	if err != nil || answer != "Курс 80 рублей (cbr.ru)." {
		t.Fatalf("answer %q, err %v", answer, err)
	}
}

func TestOpenAINativeUsesResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.openai.com/v1/responses" {
			t.Errorf("path = %s", r.URL.Path)
		}
		data, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(data), `"type":"web_search"`) || !strings.Contains(string(data), `"instructions":"sys"`) {
			t.Errorf("request = %s", data)
		}
		_, _ = io.WriteString(w, `{"status":"completed","output":[{"type":"web_search_call","status":"completed"},{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Сегодня солнечно."}]}]}`)
	}))
	defer srv.Close()
	t.Setenv("K", "secret")
	c, _ := New(Config{BaseURL: srv.URL + "/api.openai.com/v1", Model: "gpt", APIKeyEnv: "K"})
	answer, err := c.Complete(WithSearch(context.Background(), Search{Mode: SearchNative}), "sys", []Message{{Role: "user", Content: "q"}})
	if err != nil || answer != "Сегодня солнечно." {
		t.Fatalf("answer %q, err %v", answer, err)
	}
}

func TestAnthropicToolLoop(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(data))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"claude","stop_reason":"tool_use","content":[{"type":"text","text":"Сейчас поищу."},{"type":"tool_use","id":"t1","name":"web_search","input":{"query":"погода"}}],"usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"m2","type":"message","role":"assistant","model":"claude","stop_reason":"end_turn","content":[{"type":"text","text":"Плюс пять."}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()
	t.Setenv("K", "secret")
	c, _ := New(Config{Type: "anthropic", BaseURL: srv.URL, Model: "claude", APIKeyEnv: "K", MaxTokens: 100})
	s := &fakeSearch{}
	answer, err := c.Complete(WithSearch(context.Background(), Search{Mode: SearchTool, Searcher: s}), "sys", []Message{{Role: "user", Content: "Погода?"}})
	if err != nil || answer != "Плюс пять." {
		t.Fatalf("answer %q, err %v", answer, err)
	}
	if len(s.queries) != 1 || !strings.Contains(bodies[1], `"tool_use_id":"t1"`) || !strings.Contains(bodies[1], "gismeteo") {
		t.Fatalf("second request = %s", bodies[1])
	}
}

func TestAnthropicNativeSkipsPreamble(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(data), `"type":"web_search_20250305"`) {
			t.Errorf("request = %s", data)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude","stop_reason":"end_turn","content":[
			{"type":"text","text":"Поищу."},
			{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"q"}},
			{"type":"web_search_tool_result","tool_use_id":"s1","content":[]},
			{"type":"text","text":"Матч закончился "},{"type":"text","text":"со счётом два один.","citations":[]}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()
	t.Setenv("K", "secret")
	// NativeSearch accepts only Anthropic's own address for this.
	c, _ := New(Config{Type: "anthropic", BaseURL: srv.URL + "/anthropic.com", Model: "claude", APIKeyEnv: "K", MaxTokens: 100})
	answer, err := c.Complete(WithSearch(context.Background(), Search{Mode: SearchNative}), "", []Message{{Role: "user", Content: "q"}})
	if err != nil || answer != "Матч закончился со счётом два один." {
		t.Fatalf("answer %q, err %v", answer, err)
	}
}

func TestNativeSearch(t *testing.T) {
	cases := map[string]Config{
		"anthropic":  {Type: "anthropic"},
		"openai":     {BaseURL: "https://api.openai.com/v1"},
		"openrouter": {BaseURL: "https://openrouter.ai/api/v1"},
		"":           {BaseURL: "https://api.deepseek.com/v1"},
	}
	for want, cfg := range cases {
		if got := NativeSearch(cfg); got != want {
			t.Errorf("%v: %q, want %q", cfg, got, want)
		}
	}
	if NativeSearch(Config{Type: "anthropic", BaseURL: "https://proxy.example/v1"}) != "" {
		t.Error("an Anthropic-compatible proxy is not known to search")
	}
}

func TestSpeakable(t *testing.T) {
	in := "По данным **ЦБ** [1][2], курс 80 рублей 【3†source】. Подробнее на https://cbr.ru/ ."
	if got := speakable(in); got != "По данным ЦБ, курс 80 рублей. Подробнее на." {
		t.Fatalf("speakable = %q", got)
	}
}
