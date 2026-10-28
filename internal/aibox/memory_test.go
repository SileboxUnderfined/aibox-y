package aibox

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aiboxy/internal/chats"
	"aiboxy/internal/memory"
	"aiboxy/internal/providers"
)

// markingLLM answers like a model that was told to mark facts. Titles are
// asked in the background, so they are left out of systems.
type markingLLM struct{ systems []string }

func (m *markingLLM) Complete(_ context.Context, system string, msgs []providers.Message) (string, error) {
	if strings.HasPrefix(system, "Придумай название") {
		return "Кот", nil
	}
	m.systems = append(m.systems, system)
	if strings.Contains(msgs[len(msgs)-1].Content, "кот") {
		return "Какой милый! Запомнила.\n[запомнить: У пользователя кот Барсик]", nil
	}
	return "ответ", nil
}

// waitTitles lets background titling finish before the temp dir goes.
func waitTitles(t *testing.T, store *chats.Store, n int) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := store.List()
		titled := 0
		for _, c := range list {
			if strings.HasPrefix(c.Name, "Кот") {
				titled++
			}
		}
		if titled == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("chats not titled")
}

func setupMemory(t *testing.T) (*Resolver, *markingLLM, *memory.Store) {
	dir := t.TempDir()
	store, err := chats.Open(filepath.Join(dir, "chats"))
	if err != nil {
		t.Fatal(err)
	}
	facts, err := memory.Open(filepath.Join(dir, "memory.json"))
	if err != nil {
		t.Fatal(err)
	}
	llm := &markingLLM{}
	r := New(Options{Models: map[string]*Model{
		"deepseek": {Key: "deepseek", Title: "DeepSeek", Completer: llm},
		"claude":   {Key: "claude", Title: "Claude", Aliases: []string{"клод"}, Completer: llm},
	}, DefaultModel: "deepseek", Store: store, Memory: facts, StatePath: filepath.Join(dir, "state.json")})
	return r, llm, facts
}

func TestVoiceMemory(t *testing.T) {
	r, llm, facts := setupMemory(t)
	if route, a := say(t, r, "Алиса, запомни, что меня зовут Валерий."); route != "llm" || a != "Запомнила: Меня зовут Валерий." {
		t.Fatalf("remember: %s %q", route, a)
	}
	say(t, r, "Алиса, запомни я люблю джаз")
	if _, a := say(t, r, "Алиса, что ты обо мне знаешь"); a != "Я помню: Меня зовут Валерий; Я люблю джаз." {
		t.Fatalf("show = %q", a)
	}
	if _, a := say(t, r, "Алиса, забудь что я люблю джаз"); a != "Забыла: Я люблю джаз." {
		t.Fatalf("forget = %q", a)
	}
	if _, a := say(t, r, "Алиса, забудь про рыбалку"); a != "Не нашла такого в памяти." {
		t.Fatalf("forget unknown = %q", a)
	}
	if route, _ := say(t, r, "Алиса, забудь чат космос"); route != "llm" {
		t.Fatal("forget chat lost")
	}
	if len(facts.List()) != 1 {
		t.Fatalf("facts = %+v", facts.List())
	}
	say(t, r, "Алиса, выключи память")
	if route, _ := say(t, r, "Алиса, запомни, что я дома"); route != "yandex" {
		t.Fatal("with the memory off «запомни» should go to Alice")
	}
	say(t, r, "Алиса, включи память")
	if _, a := say(t, r, "Алиса, забудь всё обо мне"); a != "Забыла всё, что знала о вас." || len(facts.List()) != 0 {
		t.Fatalf("forget all = %q", a)
	}
	if len(llm.systems) != 0 {
		t.Fatal("memory commands reached the model")
	}
}

func TestModelRemembersAndChatsSeeFacts(t *testing.T) {
	r, llm, facts := setupMemory(t)
	say(t, r, "Алиса, запомни, что меня зовут Валерий")
	say(t, r, "Алиса, новый чат")
	if _, a := say(t, r, "у меня есть кот"); a != "Какой милый! Запомнила." {
		t.Fatalf("mark not cut: %q", a)
	}
	if got := facts.List(); len(got) != 2 || got[1].Text != "У пользователя кот Барсик" || got[1].Source != "model" {
		t.Fatalf("facts = %+v", got)
	}
	system := llm.systems[0]
	if !strings.Contains(system, "- Меня зовут Валерий") || !strings.Contains(system, "[запомнить:") {
		t.Fatalf("system prompt = %q", system)
	}
	r.mu.Lock()
	saved := r.active.chat.Messages[1].Content
	r.mu.Unlock()
	if strings.Contains(saved, "запомнить") {
		t.Fatalf("mark kept in history: %q", saved)
	}
	say(t, r, "закрой чат")

	// A temporary chat reads the facts but does not add to them.
	say(t, r, "Алиса, поговорить")
	before := len(llm.systems)
	say(t, r, "а ещё у меня кот Пушок")
	if s := llm.systems[before]; !strings.Contains(s, "кот Барсик") || strings.Contains(s, "[запомнить:") {
		t.Fatalf("temporary prompt = %q", s)
	}
	if len(facts.List()) != 2 {
		t.Fatalf("temporary chat saved a fact: %+v", facts.List())
	}
	say(t, r, "хватит")

	// Excluded models never see the facts; auto off keeps the facts but drops the rule.
	off, auto := false, false
	excluded := []string{"claude"}
	if err := r.Apply(Update{MemoryExcluded: &excluded, MemoryAuto: &auto}); err != nil {
		t.Fatal(err)
	}
	say(t, r, "Алиса, спроси клод как дела")
	if s := llm.systems[len(llm.systems)-1]; strings.Contains(s, "Валерий") {
		t.Fatalf("excluded model got facts: %q", s)
	}
	say(t, r, "хватит")
	say(t, r, "Алиса, спроси у меня кот")
	if s := llm.systems[len(llm.systems)-1]; !strings.Contains(s, "Валерий") || strings.Contains(s, "[запомнить:") {
		t.Fatalf("auto off prompt = %q", s)
	}
	if len(facts.List()) != 2 {
		t.Fatal("auto off still saved the model's fact")
	}
	if err := r.Apply(Update{MemoryEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	if s := r.Settings().Memory; s.Enabled || s.Auto || len(s.Facts) != 2 || s.Excluded[0] != "claude" {
		t.Fatalf("settings = %+v", s)
	}
	waitTitles(t, r.opt.Store, 3)
}
