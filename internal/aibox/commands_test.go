package aibox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"aiboxy/internal/bridge"
	"aiboxy/internal/chats"
)

func setupPhrases(t *testing.T, phrases map[string]Phrases) *Resolver {
	t.Helper()
	r, _, _ := setup(t)
	r.opt.Commands = phrases
	r.cmds = buildCommands(r.phrasesLocked())
	return r
}

func TestConfigPhrasesAdd(t *testing.T) {
	r := setupPhrases(t, map[string]Phrases{
		"temporary_chat": {Add: []string{"Поболтаем!"}},
		"exit":           {Add: []string{"всё, спасибо"}},
		"new_chat":       {Add: []string{"заведи беседу"}},
	})
	if _, a := say(t, r, "Алиса, поболтаем"); !strings.HasPrefix(a, "Давай поговорим") {
		t.Fatalf("custom phrase = %q", a)
	}
	if _, a := say(t, r, "Всё, спасибо."); a != "Выхожу из чата." {
		t.Fatalf("custom exit = %q", a)
	}
	if route, _ := say(t, r, "всё спасибо"); route != "yandex" {
		t.Fatal("custom exit outside a chat should pass to Alice")
	}
	if _, a := say(t, r, "заведи беседу про космос"); a != "Создала чат «Про космос». Слушаю." {
		t.Fatalf("custom phrase with a name = %q", a)
	}
	say(t, r, "хватит")
	if _, a := say(t, r, "поговорим"); !strings.HasPrefix(a, "Давай поговорим") {
		t.Fatalf("built-in phrase lost: %q", a)
	}
}

func TestConfigPhrasesReplace(t *testing.T) {
	r := setupPhrases(t, map[string]Phrases{
		"quick": {Add: []string{"узнай у нейросети"}, Replace: true},
		"stop":  {Add: []string{"умолкни"}, Replace: true},
	})
	if route, _ := say(t, r, "Алиса, спроси, сколько времени"); route != "yandex" {
		t.Fatal("replaced built-in phrase still opens a chat")
	}
	if _, a := say(t, r, "Алиса, узнай у нейросети, почему небо голубое?"); a != "ответ Почему небо голубое?" {
		t.Fatalf("custom quick = %q", a)
	}
	over := bridge.WithInterrupted(context.Background())
	if res, _ := r.Resolve(over, "умолкни"); !res.Silent {
		t.Fatalf("custom stop: %+v", res)
	}
	if res, _ := r.Resolve(over, "замолчи"); res.Silent {
		t.Fatal("replaced stop phrase still stops")
	}
	time.Sleep(50 * time.Millisecond) // let title goroutines finish before cleanup
}

func TestConfigQuickPhraseNamesModel(t *testing.T) {
	r := setupPhrases(t, map[string]Phrases{"quick": {Add: []string{"узнай у"}}})
	claude := &fakeLLM{name: "claude"}
	r.opt.Models["claude"] = &Model{Key: "claude", Title: "Claude", Aliases: []string{"клод"}, Completer: claude}
	if _, a := say(t, r, "узнай у клода, что такое квазар"); a != "ответ Что такое квазар" || len(claude.calls) == 0 {
		t.Fatalf("answer = %q, claude calls = %d", a, len(claude.calls))
	}
	time.Sleep(50 * time.Millisecond)
}

func TestConfigRememberPhrase(t *testing.T) {
	r, _, facts := setupMemory(t)
	r.opt.Commands = map[string]Phrases{"remember": {Add: []string{"заметь себе"}}}
	r.cmds = buildCommands(r.phrasesLocked())
	if _, a := say(t, r, "Алиса, заметь себе: я люблю джаз"); a != "Запомнила: Я люблю джаз." {
		t.Fatalf("custom remember = %q", a)
	}
	if n := len(facts.List()); n != 1 {
		t.Fatalf("facts = %d", n)
	}
}

func TestPanelPhrases(t *testing.T) {
	dir := t.TempDir()
	store, _ := chats.Open(dir)
	opt := Options{Models: map[string]*Model{"deepseek": {Key: "deepseek", Completer: &fakeLLM{}}}, DefaultModel: "deepseek", StatePath: dir + "/state.json", Store: store,
		Commands: map[string]Phrases{"list_chats": {Add: []string{"что у меня есть"}}}}
	r := New(opt)
	if err := r.Apply(Update{Commands: map[string]*Phrases{"temporary_chat": {Add: []string{" поболтаем ", "", "поболтаем"}, Replace: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, a := say(t, r, "поболтаем"); !strings.HasPrefix(a, "Давай поговорим") {
		t.Fatalf("panel phrase = %q", a)
	}
	say(t, r, "хватит")
	r2 := New(opt)
	if route, _ := say(t, r2, "поговорим"); route != "yandex" {
		t.Fatal("panel replace not restored after restart")
	}
	for _, c := range r2.Settings().Commands {
		switch c.ID {
		case "temporary_chat":
			if !c.Replace || len(c.Add) != 1 || c.Add[0] != "поболтаем" {
				t.Fatalf("panel shows %+v", c)
			}
		case "list_chats":
			if len(c.Add) != 1 {
				t.Fatalf("config phrases not shown: %+v", c)
			}
		}
	}
	if err := r2.Apply(Update{Commands: map[string]*Phrases{"temporary_chat": nil}}); err != nil {
		t.Fatal(err)
	}
	if _, a := say(t, r2, "поговорим"); !strings.HasPrefix(a, "Давай поговорим") {
		t.Fatalf("reset to config = %q", a)
	}
	if err := r2.Apply(Update{Commands: map[string]*Phrases{"nope": {Add: []string{"x"}}}}); err == nil {
		t.Fatal("unknown command accepted")
	}
}

func TestPhrasesJSON(t *testing.T) {
	var cfg map[string]Phrases
	if err := json.Unmarshal([]byte(`{"quick":["узнай"],"stop":{"add":["умолкни"],"replace":true}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["quick"].Add[0] != "узнай" || cfg["quick"].Replace || !cfg["stop"].Replace {
		t.Fatalf("parsed = %+v", cfg)
	}
	if err := ValidateCommands(cfg); err != nil {
		t.Fatal(err)
	}
	if ValidateCommands(map[string]Phrases{"quick": {Add: []string{"!!"}}}) == nil {
		t.Fatal("empty phrase accepted")
	}
	if ValidateCommands(map[string]Phrases{"wake": {Add: []string{"джарвис"}}}) == nil {
		t.Fatal("unknown command accepted")
	}
}

func TestYandexWakeWord(t *testing.T) {
	r, _, _ := setup(t)
	if _, a := say(t, r, "Яндекс, поговорим"); !strings.HasPrefix(a, "Давай поговорим") {
		t.Fatalf("Яндекс wake word = %q", a)
	}
}
