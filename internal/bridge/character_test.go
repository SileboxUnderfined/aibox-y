package bridge

import (
	"context"
	"strings"
	"sync"
	"testing"

	"aiboxy/internal/characters"
	"aiboxy/internal/llm"
)

type fixedCharacters struct{ active characters.Character }

func (f fixedCharacters) Active() characters.Character { return f.active }

type sequenceCharacters struct {
	mu    sync.Mutex
	calls int
	list  []characters.Character
}

func (s *sequenceCharacters) Active() characters.Character {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls > len(s.list) {
		return s.list[len(s.list)-1]
	}
	return s.list[s.calls-1]
}

func (s *sequenceCharacters) CallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func llmRoute(t *testing.T, e *Engine) {
	t.Helper()
	cfg := DefaultRoutingConfig()
	cfg.Rules = nil
	cfg.DefaultRoute = "llm"
	if err := e.SetRouting(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestEngineAnswerPromptUsesCharacterLayer(t *testing.T) {
	var got []llm.Message
	e := &Engine{
		Characters: fixedCharacters{active: characters.Character{ID: "sherlock", Name: "Шерлок", Prompt: "Ты Шерлок Холмс.", Rules: []string{"Говори кратко."}}},
		LLM: completionFunc(func(_ context.Context, m []llm.Message, structured bool) (string, error) {
			got = m
			return "Ответ", nil
		}),
	}
	llmRoute(t, e)
	if _, err := e.Resolve(t.Context(), "кто ты"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Role != "system" || got[1].Role != "user" {
		t.Fatalf("messages = %+v", got)
	}
	system := got[0].Content
	if strings.Contains(system, bridgeUserPrompt) {
		t.Fatalf("global identity was not replaced: %q", system)
	}
	required := strings.Index(system, bridgeRequiredPrompt)
	role := strings.Index(system, "Ты Шерлок Холмс.")
	rules := strings.Index(system, "Правила персонажа")
	if required < 0 || role < 0 || rules < 0 || !(required < role && role < rules) {
		t.Fatalf("bad layer order: %q", system)
	}
	for _, want := range []string{"Технические инструкции", "Home Assistant", "Не заявляй о выполнении действий"} {
		if !strings.Contains(system, want) {
			t.Fatalf("technical instruction %q missing: %q", want, system)
		}
	}
}

func TestEngineSnapshotsCharacterOncePerRequest(t *testing.T) {
	source := &sequenceCharacters{list: []characters.Character{
		{ID: "sherlock", Name: "Шерлок", Prompt: "Ты Шерлок Холмс."},
		{ID: "watson", Name: "Ватсон", Prompt: "Ты доктор Ватсон."},
	}}
	var mu sync.Mutex
	var systems []string
	e := &Engine{Characters: source, LLM: completionFunc(func(_ context.Context, m []llm.Message, structured bool) (string, error) {
		mu.Lock()
		systems = append(systems, m[0].Content)
		mu.Unlock()
		return "Ответ", nil
	})}
	llmRoute(t, e)
	if _, err := e.Resolve(t.Context(), "первый"); err != nil {
		t.Fatal(err)
	}
	if source.CallCount() != 1 {
		t.Fatalf("snapshot calls per request = %d", source.CallCount())
	}
	if _, err := e.Resolve(t.Context(), "второй"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(systems) != 2 || !strings.Contains(systems[0], "Ты Шерлок Холмс.") || strings.Contains(systems[0], "Ватсон") {
		t.Fatalf("first prompt = %q", systems)
	}
	if !strings.Contains(systems[1], "Ты доктор Ватсон.") {
		t.Fatalf("second prompt = %q", systems)
	}
	if source.CallCount() != 2 {
		t.Fatalf("snapshot calls after two requests = %d", source.CallCount())
	}
}

func TestEngineWithoutCharactersKeepsOldPrompt(t *testing.T) {
	var got []llm.Message
	e := &Engine{LLM: completionFunc(func(_ context.Context, m []llm.Message, structured bool) (string, error) {
		got = m
		return "Ответ", nil
	})}
	llmRoute(t, e)
	if _, err := e.Resolve(t.Context(), "вопрос"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Role != "system" || strings.Contains(got[0].Content, "Роль персонажа") {
		t.Fatalf("prompt changed without characters: %+v", got)
	}
	if !strings.HasPrefix(got[0].Content, "Ты голосовой помощник") || strings.Contains(got[0].Content, "\n\n") {
		t.Fatalf("old base prompt changed: %q", got[0].Content)
	}
}

func TestEngineFallbackSnapshotsCharacterForDirectCallers(t *testing.T) {
	source := &sequenceCharacters{list: []characters.Character{
		{ID: "sherlock", Name: "Шерлок", Prompt: "Ты Шерлок Холмс."},
		{ID: "watson", Name: "Ватсон", Prompt: "Ты доктор Ватсон."},
	}}
	var systems []string
	e := &Engine{Characters: source, LLM: completionFunc(func(_ context.Context, m []llm.Message, structured bool) (string, error) {
		systems = append(systems, m[0].Content)
		return "Ответ", nil
	})}
	if _, err := e.Fallback(t.Context(), "первый"); err != nil {
		t.Fatal(err)
	}
	if source.CallCount() != 1 {
		t.Fatalf("snapshot calls for one direct fallback = %d", source.CallCount())
	}
	if len(systems) != 1 {
		t.Fatalf("prompts = %q", systems)
	}
	system := systems[0]
	if strings.Contains(system, bridgeUserPrompt) {
		t.Fatalf("global identity was not replaced: %q", system)
	}
	required := strings.Index(system, bridgeRequiredPrompt)
	role := strings.Index(system, "Ты Шерлок Холмс.")
	technical := strings.Index(system, "Технические инструкции")
	if required < 0 || role < 0 || technical < 0 || !(required < role && role < technical) {
		t.Fatalf("bad prompt layers: required=%d role=%d technical=%d in %q", required, role, technical, system)
	}
	if _, err := e.Fallback(t.Context(), "второй"); err != nil {
		t.Fatal(err)
	}
	if source.CallCount() != 2 {
		t.Fatalf("snapshot calls after two direct fallbacks = %d", source.CallCount())
	}
	if len(systems) != 2 || strings.Contains(systems[0], "Ватсон") || !strings.Contains(systems[1], "Ты доктор Ватсон.") {
		t.Fatalf("snapshot leaked across direct fallbacks: %q", systems)
	}
}

func TestEngineFallbackKeepsExistingCharacterSnapshot(t *testing.T) {
	source := &sequenceCharacters{list: []characters.Character{
		{ID: "watson", Name: "Ватсон", Prompt: "Ты доктор Ватсон."},
	}}
	var got []llm.Message
	e := &Engine{Characters: source, LLM: completionFunc(func(_ context.Context, m []llm.Message, structured bool) (string, error) {
		got = m
		return "Ответ", nil
	})}
	ctx := snapshotCharacter(t.Context(), fixedCharacters{active: characters.Character{ID: "sherlock", Name: "Шерлок", Prompt: "Ты Шерлок Холмс."}})
	if _, err := e.Fallback(ctx, "вопрос"); err != nil {
		t.Fatal(err)
	}
	if source.CallCount() != 0 {
		t.Fatalf("existing snapshot was replaced: source calls = %d", source.CallCount())
	}
	if len(got) == 0 || !strings.Contains(got[0].Content, "Ты Шерлок Холмс.") || strings.Contains(got[0].Content, "Ватсон") {
		t.Fatalf("wrong prompt for existing snapshot: %+v", got)
	}
}

func TestEngineFallbackWithoutCharactersKeepsOldPrompt(t *testing.T) {
	var got []llm.Message
	e := &Engine{LLM: completionFunc(func(_ context.Context, m []llm.Message, structured bool) (string, error) {
		got = m
		return "Ответ", nil
	})}
	if _, err := e.Fallback(t.Context(), "вопрос"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Role != "system" || got[1].Role != "user" {
		t.Fatalf("messages = %+v", got)
	}
	if !strings.HasPrefix(got[0].Content, "Ты голосовой помощник") || strings.Contains(got[0].Content, "Роль персонажа") || strings.Contains(got[0].Content, "\n\n") {
		t.Fatalf("old base prompt changed without characters: %q", got[0].Content)
	}
}
