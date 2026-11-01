package aibox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aiboxy/internal/characters"
	"aiboxy/internal/chats"
	"aiboxy/internal/memory"
	"aiboxy/internal/providers"
)

type characterFixture struct {
	resolver *Resolver
	llm      *fakeLLM
	chats    *chats.Store
	chars    *characters.Store
	memory   *memory.Store
}

func setupCharacters(t *testing.T) characterFixture {
	t.Helper()
	return setupCharactersAt(t, t.TempDir())
}

func setupCharactersAt(t *testing.T, dir string) characterFixture {
	t.Helper()
	chatStore, err := chats.Open(filepath.Join(dir, "chats"))
	if err != nil {
		t.Fatal(err)
	}
	characterStore, err := characters.Open(filepath.Join(dir, "characters.json"))
	if err != nil {
		t.Fatal(err)
	}
	facts, err := memory.Open(filepath.Join(dir, "memory.json"))
	if err != nil {
		t.Fatal(err)
	}
	llm := &fakeLLM{name: "deepseek"}
	r := New(Options{
		Models:       map[string]*Model{"deepseek": {Key: "deepseek", Title: "DeepSeek", Completer: llm}},
		DefaultModel: "deepseek",
		StatePath:    filepath.Join(dir, "state.json"),
		Store:        chatStore,
		Characters:   characterStore,
		Memory:       facts,
	})
	return characterFixture{resolver: r, llm: llm, chats: chatStore, chars: characterStore, memory: facts}
}

func mustCharacter(t *testing.T, store *characters.Store, c characters.Character) characters.Character {
	t.Helper()
	got, err := store.Create(c)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// answerSystems returns only the prompts of real answers, without the
// background title request.
func answerSystems(llm *fakeLLM) []string {
	var out []string
	for _, system := range llm.systemsSnapshot() {
		if !strings.HasPrefix(system, "Придумай название") {
			out = append(out, system)
		}
	}
	return out
}

func TestCharacterCommandStatusSwitchUnknownReset(t *testing.T) {
	f := setupCharacters(t)
	sherlock := mustCharacter(t, f.chars, characters.Character{Name: "Шерлок", Prompt: "Ты сыщик.", Rules: []string{"Говори кратко."}})
	if _, a := say(t, f.resolver, "Алиса, персонаж"); !strings.Contains(a, characters.DefaultName) || !strings.Contains(a, "как обычно") {
		t.Fatalf("status without a character = %q", a)
	}
	if _, a := say(t, f.resolver, "персонаж Шерлок"); !strings.Contains(a, "Шерлок") {
		t.Fatalf("switch = %q", a)
	}
	if f.chars.ActiveID() != sherlock.ID {
		t.Fatalf("active = %q", f.chars.ActiveID())
	}
	if f.resolver.CurrentModel() != "deepseek" {
		t.Fatalf("character switch changed provider to %q", f.resolver.CurrentModel())
	}
	if f.resolver.inChat() {
		t.Fatal("character command opened a chat")
	}
	if n := f.llm.callCount(); n != 0 {
		t.Fatalf("character command reached LLM: %d calls", n)
	}
	if _, a := say(t, f.resolver, "персонаж Мориарти"); !strings.Contains(a, "Не знаю") || f.chars.ActiveID() != sherlock.ID {
		t.Fatalf("unknown character switched: %q active=%q", a, f.chars.ActiveID())
	}
	if _, a := say(t, f.resolver, "персонаж по умолчанию"); !strings.Contains(a, characters.DefaultName) || f.chars.ActiveID() != characters.DefaultID {
		t.Fatalf("reset = %q active=%q", a, f.chars.ActiveID())
	}
	if n := f.llm.callCount(); n != 0 {
		t.Fatalf("status/switch/reset reached LLM: %d calls", n)
	}
}

func TestCharacterVoiceNameUsesSpeechNormalization(t *testing.T) {
	f := setupCharacters(t)
	artem := mustCharacter(t, f.chars, characters.Character{Name: "Артём", Prompt: "Ты Артём."})
	if _, a := say(t, f.resolver, "персонаж артем"); f.chars.ActiveID() != artem.ID {
		t.Fatalf("ё-name not selected: answer=%q active=%q", a, f.chars.ActiveID())
	}
	if _, a := say(t, f.resolver, "персонаж по умолчанию"); f.chars.ActiveID() != characters.DefaultID {
		t.Fatalf("reset = %q active=%q", a, f.chars.ActiveID())
	}
	spider := mustCharacter(t, f.chars, characters.Character{Name: "Человек-паук", Prompt: "Ты герой."})
	if _, a := say(t, f.resolver, "персонаж человек паук"); f.chars.ActiveID() != spider.ID {
		t.Fatalf("punctuated name not selected: answer=%q active=%q", a, f.chars.ActiveID())
	}
	if _, err := f.chars.Create(characters.Character{Name: "человек паук"}); err == nil {
		t.Fatal("a speech-equivalent duplicate name was accepted")
	}
	iod := mustCharacter(t, f.chars, characters.Character{Name: "Йод", Prompt: "Ты йод."})
	if _, a := say(t, f.resolver, "персонаж и\u0306од"); f.chars.ActiveID() != iod.ID {
		t.Fatalf("decomposed name not selected: answer=%q active=%q", a, f.chars.ActiveID())
	}
}

func TestCharacterVoiceAmbiguousNameIsErrorWithoutSwitch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "characters.json")
	data := `{"version":1,"active":"one","characters":[
		{"id":"one","name":"Артём","prompt":"p1"},
		{"id":"two","name":"Артем","prompt":"p2"},
		{"id":"three","name":"Шерлок","prompt":"p3"}
	]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	f := setupCharactersAt(t, dir)
	if f.chars.ActiveID() != "one" || len(f.chars.List()) != 3 {
		t.Fatalf("legacy fixture active=%q list=%+v", f.chars.ActiveID(), f.chars.List())
	}

	_, answer := say(t, f.resolver, "персонаж артем")
	if f.chars.ActiveID() != "one" {
		t.Fatalf("ambiguous name switched to %q", f.chars.ActiveID())
	}
	for _, want := range []string{"Неоднознач", "нескольким", "переключение не выполнено"} {
		if !strings.Contains(answer, want) {
			t.Fatalf("ambiguous answer %q does not contain %q", answer, want)
		}
	}

	// A later unambiguous command still works, and an unpronounceable name is
	// rejected locally without changing the current selection.
	if _, a := say(t, f.resolver, "персонаж шерлок"); f.chars.ActiveID() != "three" {
		t.Fatalf("unambiguous name not selected: answer=%q active=%q", a, f.chars.ActiveID())
	}
	// The command parser drops punctuation before this local handler, so call
	// it directly to cover a name whose speech normalization is empty.
	if a := f.resolver.handleCharacter("---"); f.chars.ActiveID() != "three" || !strings.Contains(a, "Не поняла имя") {
		t.Fatalf("unspeakable name changed selection: answer=%q active=%q", a, f.chars.ActiveID())
	}
}

func TestCharactersSnapshotHasExactlyOneActive(t *testing.T) {
	f := setupCharacters(t)
	errs := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			c, err := f.chars.Create(characters.Character{Name: "Temp"})
			if err == nil {
				err = f.chars.SetActive(c.ID)
			}
			if err == nil {
				err = f.chars.Delete(c.ID)
			}
			if err != nil {
				errs <- err
				return
			}
		}
	}()
	for i := 0; i < 5000; i++ {
		info, err := f.resolver.Characters()
		if err != nil {
			t.Fatal(err)
		}
		active := 0
		if info.Default.Active {
			active++
		}
		for _, c := range info.Characters {
			if c.Active {
				active++
			}
		}
		if active != 1 {
			t.Fatalf("snapshot %d has %d active characters: active=%q default=%v list=%d", i, active, info.Active, info.Default.Active, len(info.Characters))
		}
	}
	<-done
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
}

func TestCharacterCommandStaysOutOfChatAndHistory(t *testing.T) {
	f := setupCharacters(t)
	mustCharacter(t, f.chars, characters.Character{Name: "Шерлок", Prompt: "Ты сыщик."})
	say(t, f.resolver, "новый чат детективы")
	say(t, f.resolver, "персонаж Шерлок")
	if _, a := say(t, f.resolver, "кто ты"); a != "ответ кто ты" {
		t.Fatalf("answer = %q", a)
	}
	say(t, f.resolver, "персонаж")
	say(t, f.resolver, "закрой чат")
	list, err := f.chats.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("chats = %+v err=%v", list, err)
	}
	msgs := list[0].Messages
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[0].Content != "кто ты" || msgs[1].Role != "assistant" {
		t.Fatalf("history was rewritten by commands: %+v", msgs)
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, "персонаж") || strings.Contains(m.Content, "Шерлок") {
			t.Fatalf("command leaked into history: %+v", m)
		}
	}
}

func TestCharacterPromptLayerOrderAndTechnicallyAuthoritative(t *testing.T) {
	f := setupCharacters(t)
	if _, err := f.memory.Add("Меня зовут Валерий", "panel"); err != nil {
		t.Fatal(err)
	}
	// Unique markers prove that the character replaces both global parts:
	// the base prompt and the user's own instructions.
	base := "ГЛОБАЛЬНЫЙ_БАЗОВЫЙ_МАРКЕР"
	user := "ГЛОБАЛЬНЫЙ_ПОЛЬЗОВАТЕЛЬСКИЙ_МАРКЕР"
	if err := f.resolver.Apply(Update{BasePrompt: &base, UserPrompt: &user}); err != nil {
		t.Fatal(err)
	}
	mustCharacter(t, f.chars, characters.Character{Name: "Шерлок", Prompt: "Ты Шерлок Холмс.", Rules: []string{"Говори кратко.", "Не выдумывай."}})
	f.chars.SetActive(f.chars.List()[0].ID)

	say(t, f.resolver, "новый чат разговор")
	if _, a := say(t, f.resolver, "кто ты"); a != "ответ кто ты" {
		t.Fatalf("answer = %q", a)
	}
	systems := answerSystems(f.llm)
	if len(systems) != 1 {
		t.Fatalf("answer prompts = %d", len(systems))
	}
	system := systems[0]
	if strings.Contains(system, base) || strings.Contains(system, user) {
		t.Fatalf("global user layer leaked with a character: %q", system)
	}
	required := strings.Index(system, characters.BuiltinPrompt)
	role := strings.Index(system, "Ты Шерлок Холмс.")
	rules := strings.Index(system, "Правила персонажа")
	memory := strings.Index(system, "Меня зовут Валерий")
	if required < 0 || role < 0 || rules < 0 || memory < 0 {
		t.Fatalf("layer missing in %q", system)
	}
	if !(required < role && role < rules && rules < memory) {
		t.Fatalf("layer order required=%d role=%d rules=%d memory=%d in %q", required, role, rules, memory, system)
	}
	if !strings.Contains(system, "Технические инструкции") || !strings.Contains(system, "важнее") {
		t.Fatalf("technical priority is not stated: %q", system)
	}
	if strings.Count(system, "Ты Шерлок Холмс.") != 1 {
		t.Fatalf("character prompt repeated: %q", system)
	}

	// The next turn gets one fresh copy and keeps only user/assistant history.
	if _, a := say(t, f.resolver, "а теперь"); a != "ответ а теперь" {
		t.Fatalf("answer = %q", a)
	}
	systems = answerSystems(f.llm)
	if len(systems) != 2 || strings.Count(systems[1], "Ты Шерлок Холмс.") != 1 {
		t.Fatalf("prompt accumulated: %q", systems)
	}
	list, _ := f.chats.List()
	if len(list) != 1 || len(list[0].Messages) != 4 {
		t.Fatalf("history = %+v", list)
	}
	for _, m := range list[0].Messages {
		if m.Role == "system" || strings.Contains(m.Content, "Ты Шерлок Холмс.") || strings.Contains(m.Content, "Технические инструкции") || strings.Contains(m.Content, base) || strings.Contains(m.Content, user) {
			t.Fatalf("prompt written into history: %+v", m)
		}
	}

	// Resetting restores the global base and the user's own instructions.
	say(t, f.resolver, "персонаж по умолчанию")
	say(t, f.resolver, "ещё вопрос")
	system = answerSystems(f.llm)[2]
	if strings.Contains(system, "Шерлок") || strings.Contains(system, "Правила персонажа") {
		t.Fatalf("default still has the persona: %q", system)
	}
	if !strings.Contains(system, base) || !strings.Contains(system, user) {
		t.Fatalf("reset did not restore the global prompt: %q", system)
	}
	if !strings.Contains(system, "Меня зовут Валерий") {
		t.Fatalf("default lost the memory context: %q", system)
	}
}

func TestCharacterKeepsSearchContextAndTechnicalLayer(t *testing.T) {
	dir := t.TempDir()
	chatStore, err := chats.Open(filepath.Join(dir, "chats"))
	if err != nil {
		t.Fatal(err)
	}
	characterStore, err := characters.Open(filepath.Join(dir, "characters.json"))
	if err != nil {
		t.Fatal(err)
	}
	sherlock := mustCharacter(t, characterStore, characters.Character{Name: "Шерлок", Prompt: "Ты Шерлок Холмс."})
	if err := characterStore.SetActive(sherlock.ID); err != nil {
		t.Fatal(err)
	}
	llm := &searchLLM{}
	r := New(Options{
		Models:       map[string]*Model{"deepseek": {Key: "deepseek", Title: "DeepSeek", Config: providers.Config{BaseURL: "https://api.deepseek.com/v1"}, Completer: llm}},
		DefaultModel: "deepseek",
		StatePath:    filepath.Join(dir, "state.json"),
		Store:        chatStore,
		Characters:   characterStore,
		Search:       &fakeSearcher{ready: true},
	})
	base := "ГЛОБАЛЬНЫЙ_БАЗОВЫЙ_МАРКЕР"
	user := "ГЛОБАЛЬНЫЙ_ПОЛЬЗОВАТЕЛЬСКИЙ_МАРКЕР"
	if err := r.Apply(Update{BasePrompt: &base, UserPrompt: &user}); err != nil {
		t.Fatal(err)
	}
	say(t, r, "поговорим")
	say(t, r, "какая погода")
	system := answerSystems(&llm.fakeLLM)[0]
	if strings.Contains(system, base) || strings.Contains(system, user) {
		t.Fatalf("global user layer leaked with a character: %q", system)
	}
	for _, want := range []string{characters.BuiltinPrompt, "Ты Шерлок Холмс.", "Сегодня ", "поиск в интернете"} {
		if !strings.Contains(system, want) {
			t.Fatalf("search/technical context %q is missing: %q", want, system)
		}
	}
	if len(llm.modes) != 1 || llm.modes[0] != providers.SearchTool {
		t.Fatalf("search mode changed with a character: %v", llm.modes)
	}
}

func TestCharacterSnapshotForEveryRequestKind(t *testing.T) {
	must := func(t *testing.T, f characterFixture) {
		sherlock := mustCharacter(t, f.chars, characters.Character{Name: "Шерлок", Prompt: "Ты Шерлок Холмс.", Rules: []string{"Говори кратко."}})
		if err := f.chars.SetActive(sherlock.ID); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("new chat", func(t *testing.T) {
		f := setupCharacters(t)
		must(t, f)
		say(t, f.resolver, "новый чат разговор")
		say(t, f.resolver, "вопрос")
		if s := answerSystems(f.llm)[0]; !strings.Contains(s, "Ты Шерлок Холмс.") {
			t.Fatalf("new chat prompt = %q", s)
		}
	})
	t.Run("current chat", func(t *testing.T) {
		f := setupCharacters(t)
		must(t, f)
		say(t, f.resolver, "новый чат разговор")
		say(t, f.resolver, "первый")
		say(t, f.resolver, "второй")
		systems := answerSystems(f.llm)
		if len(systems) != 2 || !strings.Contains(systems[1], "Ты Шерлок Холмс.") || strings.Count(systems[1], "Ты Шерлок Холмс.") != 1 {
			t.Fatalf("current chat prompts = %q", systems)
		}
	})
	t.Run("continued chat", func(t *testing.T) {
		f := setupCharacters(t)
		must(t, f)
		chat := &chats.Chat{ID: chats.NewID(), Name: "Старый", Messages: []providers.Message{{Role: "user", Content: "давно"}, {Role: "assistant", Content: "было"}}}
		if err := f.chats.Save(chat); err != nil {
			t.Fatal(err)
		}
		if _, a := say(t, f.resolver, "продолжи чат старый"); !strings.Contains(a, "Старый") {
			t.Fatalf("continue = %q", a)
		}
		say(t, f.resolver, "сейчас")
		if s := answerSystems(f.llm)[0]; !strings.Contains(s, "Ты Шерлок Холмс.") {
			t.Fatalf("continued chat prompt = %q", s)
		}
		got, err := f.chats.Get(chat.ID)
		if err != nil || len(got.Messages) != 4 || got.Messages[0].Content != "давно" {
			t.Fatalf("continued history rewritten: %+v %v", got, err)
		}
	})
	t.Run("temporary chat", func(t *testing.T) {
		f := setupCharacters(t)
		must(t, f)
		say(t, f.resolver, "поговорим")
		say(t, f.resolver, "вопрос")
		if s := answerSystems(f.llm)[0]; !strings.Contains(s, "Ты Шерлок Холмс.") {
			t.Fatalf("temporary chat prompt = %q", s)
		}
		if list, _ := f.chats.List(); len(list) != 0 {
			t.Fatalf("temporary chat saved: %+v", list)
		}
	})
	t.Run("quick question", func(t *testing.T) {
		f := setupCharacters(t)
		must(t, f)
		if _, a := say(t, f.resolver, "спроси, кто ты"); a != "ответ Кто ты" {
			t.Fatalf("quick = %q", a)
		}
		if s := answerSystems(f.llm)[0]; !strings.Contains(s, "Ты Шерлок Холмс.") {
			t.Fatalf("quick prompt = %q", s)
		}
		time.Sleep(50 * time.Millisecond) // let the background title request finish
	})
}

// gatedLLM blocks the first answer so the test can switch characters while a
// request is in flight, then records the prompts of later requests.
type gatedLLM struct {
	once    sync.Once
	started chan string
	release chan struct{}
	mu      sync.Mutex
	systems []string
}

func newGatedLLM() *gatedLLM {
	return &gatedLLM{started: make(chan string, 1), release: make(chan struct{})}
}

func (g *gatedLLM) Complete(_ context.Context, system string, m []providers.Message) (string, error) {
	if strings.HasPrefix(system, "Придумай название") {
		return "Космос", nil
	}
	g.mu.Lock()
	g.systems = append(g.systems, system)
	g.mu.Unlock()
	g.once.Do(func() {
		g.started <- system
		<-g.release
	})
	return "ответ " + m[len(m)-1].Content, nil
}

func TestCharacterIsSnapshottedAtRequestStart(t *testing.T) {
	dir := t.TempDir()
	chatStore, _ := chats.Open(dir)
	characterStore, _ := characters.Open(filepath.Join(dir, "characters.json"))
	sherlock := mustCharacter(t, characterStore, characters.Character{Name: "Шерлок", Prompt: "Ты Шерлок Холмс."})
	watson := mustCharacter(t, characterStore, characters.Character{Name: "Ватсон", Prompt: "Ты доктор Ватсон."})
	if err := characterStore.SetActive(sherlock.ID); err != nil {
		t.Fatal(err)
	}
	llm := newGatedLLM()
	r := New(Options{Models: map[string]*Model{"deepseek": {Key: "deepseek", Title: "DeepSeek", Completer: llm}}, DefaultModel: "deepseek", Store: chatStore, Characters: characterStore})
	say(t, r, "новый чат разговор")

	done := make(chan struct{})
	go func() {
		defer close(done)
		say(t, r, "первый вопрос")
	}()
	inflight := <-llm.started
	if !strings.Contains(inflight, "Ты Шерлок Холмс.") {
		t.Fatalf("in-flight prompt = %q", inflight)
	}
	if err := characterStore.SetActive(watson.ID); err != nil {
		t.Fatal(err)
	}
	close(llm.release)
	<-done

	if _, a := say(t, r, "второй вопрос"); a != "ответ второй вопрос" {
		t.Fatalf("second = %q", a)
	}
	llm.mu.Lock()
	systems := append([]string(nil), llm.systems...)
	llm.mu.Unlock()
	if len(systems) != 2 {
		t.Fatalf("prompts = %q", systems)
	}
	if strings.Contains(systems[0], "Ватсон") || !strings.Contains(systems[1], "Ты доктор Ватсон.") {
		t.Fatalf("snapshot leaked across requests: %q", systems)
	}
	chat, err := chatStore.List()
	if err != nil || len(chat) != 1 {
		t.Fatalf("chat = %+v %v", chat, err)
	}
	for _, m := range chat[0].Messages {
		if strings.Contains(m.Content, "Шерлок") || strings.Contains(m.Content, "Ватсон") {
			t.Fatalf("prompt stored in history: %+v", m)
		}
	}
}

func TestCharacterPhraseSettingAndTemporaryMemory(t *testing.T) {
	f := setupCharacters(t)
	sherlock := mustCharacter(t, f.chars, characters.Character{Name: "Шерлок", Prompt: "Ты Шерлок Холмс."})
	found := false
	for _, c := range f.resolver.Settings().Commands {
		if c.ID == "character" {
			found = c.Args == "optional" && len(c.Builtin) == 1 && c.Builtin[0] == "персонаж"
		}
	}
	if !found {
		t.Fatal("character command missing from the phrase settings")
	}
	if err := f.resolver.Apply(Update{Commands: map[string]*Phrases{"character": {Add: []string{"стань"}, Replace: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, a := say(t, f.resolver, "стань шерлок"); !strings.Contains(a, "Шерлок") || f.chars.ActiveID() != sherlock.ID {
		t.Fatalf("custom phrase = %q active=%q", a, f.chars.ActiveID())
	}
	say(t, f.resolver, "хватит")
	if route, a := say(t, f.resolver, "персонаж шерлок"); route != "yandex" || a != "" {
		t.Fatalf("replaced built-in phrase still switched: %s %q", route, a)
	}

	// Temporary chat still shows facts but does not take the auto-remember rule.
	if _, err := f.memory.Add("У меня кот Барсик", "panel"); err != nil {
		t.Fatal(err)
	}
	say(t, f.resolver, "поговорим")
	say(t, f.resolver, "привет")
	system := answerSystems(f.llm)[0]
	if !strings.Contains(system, "У меня кот Барсик") || strings.Contains(system, "запомнить:") {
		t.Fatalf("temporary memory changed: %q", system)
	}
}

func TestShowPromptDefaultKeepsPreviousAnswers(t *testing.T) {
	f := setupCharacters(t)
	// A stored but inactive character must not change the default answer.
	mustCharacter(t, f.chars, characters.Character{Name: "Шерлок", Prompt: "Ты сыщик."})
	if route, a := say(t, f.resolver, "какой промпт"); route != "llm" || a != "Сейчас обычный системный промпт, без дополнительной роли." {
		t.Fatalf("default answer = %s %q", route, a)
	}
	say(t, f.resolver, "системный промпт отвечай как пират")
	if _, a := say(t, f.resolver, "какая у тебя роль"); a != "Сейчас системный промпт: Отвечай как пират." {
		t.Fatalf("custom prompt answer = %q", a)
	}
	if n := f.llm.callCount(); n != 0 {
		t.Fatalf("prompt commands reached LLM: %d calls", n)
	}
	if list, err := f.chats.List(); err != nil || len(list) != 0 {
		t.Fatalf("prompt commands created chats: %+v err=%v", list, err)
	}
}

func TestShowPromptReportsSelectedCharacterAndUserInstructions(t *testing.T) {
	f := setupCharacters(t)
	sherlock := mustCharacter(t, f.chars, characters.Character{
		Name:   "Шерлок",
		Prompt: "Ты Шерлок Холмс.",
		Rules:  []string{"Говори кратко.", "Не выдумывай."},
	})
	if err := f.chars.SetActive(sherlock.ID); err != nil {
		t.Fatal(err)
	}
	say(t, f.resolver, "системный промпт отвечай как пират")

	route, answer := say(t, f.resolver, "какой промпт")
	if route != "llm" {
		t.Fatalf("route = %s", route)
	}
	for _, want := range []string{
		"Сейчас персонаж «Шерлок»",
		"Промпт персонажа: Ты Шерлок Холмс.",
		"Правила персонажа: Говори кратко. Не выдумывай.",
		"Промпт и правила персонажа заменяют ваш системный промпт и базовые правила.",
		"Ваши указания сохранены, но сейчас не используются: Отвечай как пират.",
		"после сброса персонажа ваши настройки вернутся",
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("answer %q does not contain %q", answer, want)
		}
	}
	if strings.Contains(answer, "голосовой ассистент") {
		t.Fatalf("answer claims to show the whole prompt: %q", answer)
	}
	if n := f.llm.callCount(); n != 0 {
		t.Fatalf("prompt command reached LLM: %d calls", n)
	}
	if list, err := f.chats.List(); err != nil || len(list) != 0 {
		t.Fatalf("prompt command created chats: %+v err=%v", list, err)
	}
	if f.resolver.inChat() {
		t.Fatal("prompt command opened a chat")
	}
}

func TestShowPromptReportsCharacterWithoutOwnPrompt(t *testing.T) {
	f := setupCharacters(t)
	mute := mustCharacter(t, f.chars, characters.Character{Name: "Молчун"})
	if err := f.chars.SetActive(mute.ID); err != nil {
		t.Fatal(err)
	}
	if _, a := say(t, f.resolver, "какая у тебя роль"); !strings.Contains(a, "Молчун") || !strings.Contains(a, "Своего промпта и правил у персонажа нет.") {
		t.Fatalf("answer = %q", a)
	}
}

func TestShowPromptCommandStaysOutOfHistory(t *testing.T) {
	f := setupCharacters(t)
	sherlock := mustCharacter(t, f.chars, characters.Character{Name: "Шерлок", Prompt: "Ты Шерлок Холмс."})
	if err := f.chars.SetActive(sherlock.ID); err != nil {
		t.Fatal(err)
	}
	say(t, f.resolver, "новый чат разговор")
	if _, a := say(t, f.resolver, "кто ты"); a != "ответ кто ты" {
		t.Fatalf("answer = %q", a)
	}
	if _, a := say(t, f.resolver, "какая у тебя роль"); !strings.Contains(a, "Ты Шерлок Холмс.") {
		t.Fatalf("prompt answer = %q", a)
	}
	say(t, f.resolver, "закрой чат")
	list, err := f.chats.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("chats = %+v err=%v", list, err)
	}
	msgs := list[0].Messages
	if len(msgs) != 2 || msgs[0].Content != "кто ты" {
		t.Fatalf("history was rewritten by the prompt command: %+v", msgs)
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, "Шерлок") || strings.Contains(m.Content, "Промпт персонажа") {
			t.Fatalf("prompt leaked into history: %+v", m)
		}
	}
}
