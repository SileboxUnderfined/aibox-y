package aibox

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aiboxy/internal/characters"
	"aiboxy/internal/chats"
	"aiboxy/internal/providers"
)

// wireCapture records the requests a real provider adapter sends. The system
// message is kept separately, as the OpenAI-compatible adapter prepends it.
type wireCapture struct {
	mu       sync.Mutex
	systems  []string
	messages [][]providers.Message
}

func (c *wireCapture) add(system string, messages []providers.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.systems = append(c.systems, system)
	c.messages = append(c.messages, append([]providers.Message(nil), messages...))
}

// answerRequests drops the background title request so only chat turns remain.
func (c *wireCapture) answerRequests() ([]string, [][]providers.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var systems []string
	var messages [][]providers.Message
	for i, system := range c.systems {
		if strings.HasPrefix(system, "Придумай название") {
			continue
		}
		systems = append(systems, system)
		messages = append(messages, c.messages[i])
	}
	return systems, messages
}

func newWireServer(t *testing.T, capture *wireCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		var body struct {
			Messages []providers.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var system string
		var rest []providers.Message
		for _, m := range body.Messages {
			if m.Role == "system" {
				system = m.Content
				continue
			}
			rest = append(rest, m)
		}
		capture.add(system, rest)
		content := "синтетический ответ"
		if strings.HasPrefix(system, "Придумай название") {
			content = "Тестовый чат"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]string{"role": "assistant", "content": content},
			}},
		})
	}))
}

type wireFixture struct {
	resolver *Resolver
	chats    *chats.Store
	capture  *wireCapture
}

// setupWire builds the real Resolver -> providers.openAI -> httptest.Server
// path with synthetic data. No real API is contacted; the key is a dummy.
func setupWire(t *testing.T, character *characters.Character) wireFixture {
	t.Helper()
	return setupWireState(t, character, "")
}

// setupWireState is setupWire with a pre-seeded state.json, so a test can set
// the global base prompt and user prompt before the resolver starts.
func setupWireState(t *testing.T, character *characters.Character, stateJSON string) wireFixture {
	t.Helper()
	dir := t.TempDir()
	chatStore, err := chats.Open(filepath.Join(dir, "chats"))
	if err != nil {
		t.Fatal(err)
	}
	characterStore, err := characters.Open(filepath.Join(dir, "characters.json"))
	if err != nil {
		t.Fatal(err)
	}
	capture := &wireCapture{}
	server := newWireServer(t, capture)
	t.Cleanup(server.Close)

	t.Setenv("AIBOX_TEST_LLM_KEY", "test-key")
	completer, err := providers.New(providers.Config{
		Type:      "openai",
		BaseURL:   server.URL,
		Model:     "synthetic-model",
		APIKeyEnv: "AIBOX_TEST_LLM_KEY",
		MaxTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "state.json")
	if stateJSON != "" {
		if err := os.WriteFile(statePath, []byte(stateJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	resolver := New(Options{
		Models:       map[string]*Model{"synthetic": {Key: "synthetic", Title: "Синтетическая", Completer: completer}},
		DefaultModel: "synthetic",
		StatePath:    statePath,
		Store:        chatStore,
		Characters:   characterStore,
	})
	if character != nil {
		created, err := characterStore.Create(*character)
		if err != nil {
			t.Fatal(err)
		}
		if err := characterStore.SetActive(created.ID); err != nil {
			t.Fatal(err)
		}
	}
	return wireFixture{resolver: resolver, chats: chatStore, capture: capture}
}

func assertCharacterSystem(t *testing.T, system, role string) {
	t.Helper()
	if !strings.HasPrefix(system, characters.BuiltinPrompt) {
		t.Fatalf("mandatory prompt is not intact: %q", system)
	}
	if strings.Contains(system, characters.DefaultUserPrompt) {
		t.Fatalf("default global identity was not replaced: %q", system)
	}
	for _, want := range []string{
		role,
		"Правила персонажа",
		"разговорную роль",
		"самопредставление",
		"«кто ты»",
		"важнее общего описания ассистента",
		"Технические инструкции",
		"безопасность",
		"памяти и поиска",
		"не притворяйся человеком",
		"что ты ИИ",
	} {
		if !strings.Contains(system, want) {
			t.Fatalf("system message lacks %q: %q", want, system)
		}
	}
	if strings.Count(system, role) != 1 {
		t.Fatalf("character role appears %d times: %q", strings.Count(system, role), system)
	}
}

func assertNoPromptInHistory(t *testing.T, messages []providers.Message) {
	t.Helper()
	for _, m := range messages {
		if m.Role == "system" {
			t.Fatalf("system message leaked into history: %+v", m)
		}
		if strings.Contains(m.Content, "Ты Шерлок Холмс.") || strings.Contains(m.Content, "Технические инструкции") || strings.Contains(m.Content, "разговорную роль") {
			t.Fatalf("prompt leaked into history: %+v", m)
		}
	}
}

func TestCharacterPromptReachesProviderWire(t *testing.T) {
	role := "Ты Шерлок Холмс."
	character := characters.Character{Name: "Шерлок", Prompt: role, Rules: []string{"Говори кратко."}}

	t.Run("new chat with empty history", func(t *testing.T) {
		f := setupWire(t, &character)
		if route, _ := say(t, f.resolver, "новый чат разговор"); route != "llm" {
			t.Fatalf("new chat route = %s", route)
		}
		if route, _ := say(t, f.resolver, "кто ты"); route != "llm" {
			t.Fatalf("question route = %s", route)
		}
		systems, messages := f.capture.answerRequests()
		if len(systems) != 1 {
			t.Fatalf("answer requests = %d", len(systems))
		}
		assertCharacterSystem(t, systems[0], role)
		if len(messages[0]) != 1 || messages[0][0] != (providers.Message{Role: "user", Content: "кто ты"}) {
			t.Fatalf("first request should have only the empty-history user turn: %+v", messages[0])
		}
		assertNoPromptInHistory(t, messages[0])
	})

	t.Run("current chat does not accumulate the prompt", func(t *testing.T) {
		f := setupWire(t, &character)
		say(t, f.resolver, "новый чат разговор")
		say(t, f.resolver, "кто ты")
		say(t, f.resolver, "а теперь")
		systems, messages := f.capture.answerRequests()
		if len(systems) != 2 {
			t.Fatalf("answer requests = %d", len(systems))
		}
		if systems[0] != systems[1] {
			t.Fatalf("system message accumulated: first=%q second=%q", systems[0], systems[1])
		}
		assertCharacterSystem(t, systems[1], role)
		if len(messages[1]) != 3 {
			t.Fatalf("second request = %+v", messages[1])
		}
		if messages[1][0].Content != "кто ты" || messages[1][1].Content != "синтетический ответ" || messages[1][2].Content != "а теперь" {
			t.Fatalf("second request history = %+v", messages[1])
		}
		assertNoPromptInHistory(t, messages[1])
	})

	t.Run("continued chat with empty history", func(t *testing.T) {
		f := setupWire(t, &character)
		chat := &chats.Chat{ID: chats.NewID(), Name: "Старый"}
		if err := f.chats.Save(chat); err != nil {
			t.Fatal(err)
		}
		say(t, f.resolver, "продолжи чат старый")
		say(t, f.resolver, "кто ты")
		systems, messages := f.capture.answerRequests()
		if len(systems) != 1 {
			t.Fatalf("answer requests = %d", len(systems))
		}
		assertCharacterSystem(t, systems[0], role)
		if len(messages[0]) != 1 {
			t.Fatalf("continued empty chat request = %+v", messages[0])
		}
	})

	t.Run("temporary chat with empty history", func(t *testing.T) {
		f := setupWire(t, &character)
		say(t, f.resolver, "поговорим")
		say(t, f.resolver, "кто ты")
		systems, messages := f.capture.answerRequests()
		if len(systems) != 1 {
			t.Fatalf("answer requests = %d", len(systems))
		}
		assertCharacterSystem(t, systems[0], role)
		if len(messages[0]) != 1 {
			t.Fatalf("temporary chat request = %+v", messages[0])
		}
	})

	t.Run("quick question with empty history", func(t *testing.T) {
		f := setupWire(t, &character)
		if route, _ := say(t, f.resolver, "спроси, кто ты"); route != "llm" {
			t.Fatalf("quick question route = %s", route)
		}
		systems, messages := f.capture.answerRequests()
		if len(systems) != 1 {
			t.Fatalf("answer requests = %d", len(systems))
		}
		assertCharacterSystem(t, systems[0], role)
		if len(messages[0]) != 1 || messages[0][0] != (providers.Message{Role: "user", Content: "Кто ты"}) {
			t.Fatalf("quick question request = %+v", messages[0])
		}
	})
}

func TestCharacterReplacesGlobalUserLayerOnProviderWire(t *testing.T) {
	role := "Ты Шерлок Холмс."
	character := characters.Character{Name: "Шерлок", Prompt: role, Rules: []string{"Говори кратко."}}
	const (
		base = "ГЛОБАЛЬНЫЙ_БАЗОВЫЙ_МАРКЕР"
		user = "ГЛОБАЛЬНЫЙ_ПОЛЬЗОВАТЕЛЬСКИЙ_МАРКЕР"
	)
	f := setupWireState(t, &character, `{"base_prompt":"`+base+`","system_prompt":"`+user+`"}`)
	say(t, f.resolver, "новый чат разговор")
	say(t, f.resolver, "кто ты")
	systems, _ := f.capture.answerRequests()
	if len(systems) != 1 {
		t.Fatalf("answer requests = %d", len(systems))
	}
	assertCharacterSystem(t, systems[0], role)
	if strings.Contains(systems[0], base) || strings.Contains(systems[0], user) {
		t.Fatalf("global user layer reached the provider with a character: %q", systems[0])
	}

	say(t, f.resolver, "персонаж по умолчанию")
	say(t, f.resolver, "ещё вопрос")
	systems, _ = f.capture.answerRequests()
	if len(systems) != 2 {
		t.Fatalf("answer requests after reset = %d", len(systems))
	}
	if !strings.Contains(systems[1], base) || !strings.Contains(systems[1], user) {
		t.Fatalf("reset did not restore the global prompt on the wire: %q", systems[1])
	}
	if strings.Contains(systems[1], role) || strings.Contains(systems[1], "Правила персонажа") {
		t.Fatalf("character layer survived the reset: %q", systems[1])
	}
}

func TestDefaultCharacterPromptReachesProviderUnchanged(t *testing.T) {
	f := setupWire(t, nil)
	say(t, f.resolver, "новый чат разговор")
	if route, _ := say(t, f.resolver, "кто ты"); route != "llm" {
		t.Fatalf("question route = %s", route)
	}
	systems, messages := f.capture.answerRequests()
	if len(systems) != 1 {
		t.Fatalf("answer requests = %d", len(systems))
	}
	if systems[0] != DefaultSystemPrompt {
		t.Fatalf("default system prompt changed: %q", systems[0])
	}
	if strings.Contains(systems[0], "Роль персонажа") || strings.Contains(systems[0], "разговорную роль") {
		t.Fatalf("default prompt gained a character layer: %q", systems[0])
	}
	if len(messages[0]) != 1 || messages[0][0] != (providers.Message{Role: "user", Content: "кто ты"}) {
		t.Fatalf("default request = %+v", messages[0])
	}
}
