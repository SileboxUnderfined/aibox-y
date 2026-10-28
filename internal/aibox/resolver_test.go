package aibox

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"aiboxy/internal/chats"
	"aiboxy/internal/providers"
)

type fakeLLM struct {
	name    string
	calls   [][]providers.Message
	systems []string
}

func (f *fakeLLM) Complete(_ context.Context, system string, m []providers.Message) (string, error) {
	f.calls = append(f.calls, m)
	f.systems = append(f.systems, system)
	if strings.HasPrefix(system, "Придумай название") {
		return "Космос", nil
	}
	return "ответ " + m[len(m)-1].Content, nil
}

func setup(t *testing.T) (*Resolver, *fakeLLM, *chats.Store) {
	store, err := chats.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	llm := &fakeLLM{name: "deepseek"}
	return New(Options{Models: map[string]*Model{"deepseek": {Key: "deepseek", Title: "DeepSeek", Aliases: []string{"дипсик"}, Completer: llm}}, DefaultModel: "deepseek", Store: store}), llm, store
}

func say(t *testing.T, r *Resolver, text string) (string, string) {
	t.Helper()
	res, err := r.Resolve(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	return res.Route, res.Answer
}

func TestPassThroughOutsideChat(t *testing.T) {
	r, llm, _ := setup(t)
	if route, _ := say(t, r, "Алиса, включи музыку"); route != "yandex" {
		t.Fatalf("route = %s", route)
	}
	if route, _ := say(t, r, "хватит"); route != "yandex" {
		t.Fatalf("exit outside chat should pass, got %s", route)
	}
	if len(llm.calls) != 0 {
		t.Fatal("LLM called outside a chat")
	}
}

func TestNewChatSavesAndContinues(t *testing.T) {
	r, llm, store := setup(t)
	if _, a := say(t, r, "Алиса, новый чат"); !strings.Contains(a, "Новый чат") {
		t.Fatalf("answer = %q", a)
	}
	if route, a := say(t, r, "что такое черная дыра"); route != "llm" || a != "ответ что такое черная дыра" {
		t.Fatalf("got %s %q", route, a)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		list, _ := store.List()
		if len(list) == 1 && list[0].Name == "Космос" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("chat not titled: %+v", list)
		}
		time.Sleep(10 * time.Millisecond)
	}
	say(t, r, "закрой чат")
	if route, _ := say(t, r, "какая погода"); route != "yandex" {
		t.Fatal("chat still open after exit")
	}
	if _, a := say(t, r, "алиса продолжить чат космос"); !strings.Contains(a, "Космос") {
		t.Fatalf("continue answer = %q", a)
	}
	say(t, r, "а сколько их")
	last := llm.calls[len(llm.calls)-1]
	if len(last) != 3 {
		t.Fatalf("history not sent: %d messages", len(last))
	}
}

func TestTemporaryChatIsNotSaved(t *testing.T) {
	r, _, store := setup(t)
	say(t, r, "поговорить")
	say(t, r, "привет")
	if list, _ := store.List(); len(list) != 0 {
		t.Fatalf("temporary chat saved: %d", len(list))
	}
}

func TestForgetAndIdle(t *testing.T) {
	r, _, store := setup(t)
	say(t, r, "новый чат рецепты")
	if _, a := say(t, r, "забудь чат рецепт"); !strings.Contains(a, "Забыла") {
		t.Fatalf("forget answer = %q", a)
	}
	if list, _ := store.List(); len(list) != 0 {
		t.Fatal("chat not deleted")
	}
	if route, _ := say(t, r, "привет"); route != "yandex" {
		t.Fatal("deleted chat still active")
	}
	say(t, r, "поговорить")
	now := time.Now()
	r.now = func() time.Time { return now.Add(time.Hour) }
	if route, _ := say(t, r, "привет"); route != "yandex" {
		t.Fatal("idle chat not closed")
	}
}

func TestUnknownChat(t *testing.T) {
	r, _, _ := setup(t)
	if _, a := say(t, r, "продолжи чат марс"); !strings.Contains(a, "Не нашла") {
		t.Fatalf("answer = %q", a)
	}
}

func TestModelAndPrompt(t *testing.T) {
	dir := t.TempDir()
	store, _ := chats.Open(dir)
	ds, cl := &fakeLLM{name: "deepseek"}, &fakeLLM{name: "claude"}
	opt := Options{
		Models: map[string]*Model{
			"deepseek": {Key: "deepseek", Title: "DeepSeek", Aliases: []string{"дипсик"}, Completer: ds},
			"claude":   {Key: "claude", Title: "Claude", Aliases: []string{"клод"}, Completer: cl},
		},
		DefaultModel: "deepseek", StatePath: dir + "/state.json", Store: store,
	}
	r := New(opt)
	if route, _ := say(t, r, "Алиса, переключись на кухню"); route != "yandex" {
		t.Fatal("unknown switch target must go to Yandex")
	}
	if _, a := say(t, r, "переключись на клод"); !strings.Contains(a, "Claude") {
		t.Fatalf("switch answer = %q", a)
	}
	if _, a := say(t, r, "выбери модель марсианин"); !strings.Contains(a, "Не знаю") {
		t.Fatalf("unknown model answer = %q", a)
	}
	if _, a := say(t, r, "системный промпт ты пират"); !strings.Contains(a, "Ты пират") {
		t.Fatalf("prompt answer = %q", a)
	}
	say(t, r, "поговорить")
	say(t, r, "привет")
	if len(cl.calls) != 1 || len(ds.calls) != 0 {
		t.Fatalf("calls: claude %d deepseek %d", len(cl.calls), len(ds.calls))
	}
	if !strings.Contains(cl.systems[0], "Ты пират") || !strings.Contains(cl.systems[0], "прочитан вслух") {
		t.Fatalf("system prompt = %q", cl.systems[0])
	}
	// Settings survive a restart.
	r2 := New(opt)
	if r2.CurrentModel() != "claude" || !strings.Contains(r2.systemPrompt(), "Ты пират") {
		t.Fatal("state not restored")
	}
	say(t, r2, "сбрось системный промпт")
	if strings.Contains(r2.systemPrompt(), "пират") {
		t.Fatal("prompt not reset")
	}
	if _, a := say(t, r2, "какая модель"); !strings.Contains(a, "Сейчас отвечает Claude") {
		t.Fatalf("which model = %q", a)
	}
}

func TestPanelApply(t *testing.T) {
	dir := t.TempDir()
	store, _ := chats.Open(dir)
	opt := Options{
		Models:       map[string]*Model{"deepseek": {Key: "deepseek", Config: providers.Config{Type: "openai", BaseURL: "https://x", Model: "deepseek-chat"}, Completer: &fakeLLM{}}},
		DefaultModel: "deepseek", StatePath: dir + "/state.json", Store: store,
	}
	r := New(opt)
	base := "Свои правила"
	if err := r.Apply(Update{ModelIDs: map[string]string{"deepseek": "deepseek-reasoner"}, BasePrompt: &base}); err != nil {
		t.Fatal(err)
	}
	if got := r.Settings().Providers[0].Model; got != "deepseek-reasoner" {
		t.Fatalf("model = %s", got)
	}
	bad := "nope"
	if err := r.Apply(Update{Active: &bad}); err == nil {
		t.Fatal("unknown provider accepted")
	}
	opt.Models["deepseek"].Config.Model = "deepseek-chat"
	r2 := New(opt)
	if r2.Settings().Providers[0].Model != "deepseek-reasoner" || !strings.HasPrefix(r2.systemPrompt(), "Свои правила") {
		t.Fatal("panel settings not restored")
	}
}

func TestCustomProvider(t *testing.T) {
	dir := t.TempDir()
	store, _ := chats.Open(dir)
	opt := Options{
		Models:       map[string]*Model{"deepseek": {Key: "deepseek", Title: "DeepSeek", Completer: &fakeLLM{}}},
		DefaultModel: "deepseek", StatePath: dir + "/state.json", Store: store,
	}
	r := New(opt)
	if _, err := r.SaveProvider(CustomProvider{Title: "Groq", BaseURL: "ftp://x", Model: "m"}); err == nil {
		t.Fatal("bad URL accepted")
	}
	c, err := r.SaveProvider(CustomProvider{Title: "Groq", BaseURL: "https://api.groq.com/openai/v1/", Model: "llama", Aliases: []string{" грок ", ""}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Key != "api-1" || c.KeyEnv() != "AIBOX_KEY_API_1" || c.BaseURL != "https://api.groq.com/openai/v1" || len(c.Aliases) != 1 {
		t.Fatalf("saved = %+v", c)
	}
	if _, a := say(t, r, "переключись на грок"); !strings.Contains(a, "Groq") {
		t.Fatalf("switch = %q", a)
	}
	// Restored after restart, then edited and removed.
	opt.Models = map[string]*Model{"deepseek": opt.Models["deepseek"]}
	r2 := New(opt)
	if r2.CurrentModel() != "api-1" {
		t.Fatalf("custom provider not restored: %s", r2.CurrentModel())
	}
	c.Model = "llama-2"
	if _, err := r2.SaveProvider(c); err != nil {
		t.Fatal(err)
	}
	if _, err := r2.SaveProvider(CustomProvider{Key: "deepseek", Title: "x", BaseURL: "https://x", Model: "m"}); err == nil {
		t.Fatal("built-in provider overwritten")
	}
	if _, err := r2.RemoveProvider("deepseek"); err == nil {
		t.Fatal("built-in provider removed")
	}
	env, err := r2.RemoveProvider("api-1")
	if err != nil || env != "AIBOX_KEY_API_1" || r2.CurrentModel() != "deepseek" {
		t.Fatalf("remove: %s %v %s", env, err, r2.CurrentModel())
	}
}

func TestStripWakeAndUncensored(t *testing.T) {
	for in, want := range map[string]string{"Алиса полный это": "Полный это", "алиса, сколько": "Сколько", "Алисата": "Алисата", "Алиса": "Алиса"} {
		if got := stripWake(in); got != want {
			t.Errorf("stripWake(%q) = %q, want %q", in, got, want)
		}
	}
	r, _, _ := setup(t)
	if r.UncensoredASR() {
		t.Fatal("uncensored outside a chat")
	}
	say(t, r, "поговорить")
	if !r.UncensoredASR() {
		t.Fatal("not uncensored inside a chat")
	}
}

func TestQuickQuestion(t *testing.T) {
	r, llm, store := setup(t)
	titled := func(n int) []*chats.Chat {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			list, _ := store.List()
			done := 0
			for _, c := range list {
				if strings.HasPrefix(c.Name, "Космос") {
					done++
				}
			}
			if len(list) == n && done == n {
				return list
			}
			if time.Now().After(deadline) {
				t.Fatalf("chats not titled: %+v", list)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	route, a := say(t, r, "Алиса, спроси нейросеть, почему небо голубое?")
	if route != "llm" || a != "ответ Почему небо голубое?" {
		t.Fatalf("route = %s, answer = %q", route, a)
	}
	titled(1)
	if _, a := say(t, r, "а ночью?"); a != "ответ а ночью?" {
		t.Fatalf("follow-up = %q", a)
	}
	if len(llm.calls[len(llm.calls)-1]) != 3 {
		t.Fatal("follow-up lost the quick question")
	}
	say(t, r, "хватит")
	if list := titled(1); len(list[0].Messages) != 4 {
		t.Fatalf("saved chat = %+v", list[0])
	}
	if _, a := say(t, r, "Задай вопрос нейросети: сколько лет Луне"); a != "ответ Сколько лет Луне" {
		t.Fatalf("variant = %q", a)
	}
	titled(2)
	say(t, r, "хватит")
	if _, a := say(t, r, "спроси нейросеть"); a != "Новый чат. Слушаю." {
		t.Fatalf("empty question = %q", a)
	}
	if route, _ := say(t, r, "хватит"); route != "llm" {
		t.Fatal("bare command should open a chat")
	}
}

func TestQuickQuestionModel(t *testing.T) {
	r, deepseek, _ := setup(t)
	claude := &fakeLLM{name: "claude"}
	r.opt.Models["claude"] = &Model{Key: "claude", Title: "Claude", Aliases: []string{"клод"}, Completer: claude}
	if _, a := say(t, r, "Алиса, спроси у клода, что такое квазар"); a != "ответ Что такое квазар" {
		t.Fatalf("answer = %q", a)
	}
	say(t, r, "а пульсар?")
	if n := len(claude.calls); n < 2 {
		t.Fatalf("claude calls = %d", n)
	}
	for _, c := range deepseek.calls {
		if c[len(c)-1].Content != "Что такое квазар" { // only the title may go elsewhere
			t.Fatalf("default model asked %q", c[len(c)-1].Content)
		}
	}
	if r.CurrentModel() != "deepseek" {
		t.Fatal("quick question changed the default model")
	}
	say(t, r, "хватит")
	if _, a := say(t, r, "спроси почему трава зеленая"); a != "ответ Почему трава зеленая" {
		t.Fatalf("bare спроси = %q", a)
	}
	say(t, r, "хватит")
	if _, a := say(t, r, "спроси клод"); !strings.Contains(a, "Claude") {
		t.Fatalf("model without question = %q", a)
	}
	time.Sleep(50 * time.Millisecond) // let title goroutines finish before cleanup
}

type brokenLLM struct{}

func (brokenLLM) Complete(context.Context, string, []providers.Message) (string, error) {
	return "", providers.ErrNoKey
}

func TestModelFailureFallsBackToAlice(t *testing.T) {
	r, _, store := setup(t)
	r.opt.Models["deepseek"].Completer = brokenLLM{}
	if route, _ := say(t, r, "Алиса, спроси, какая погода"); route != "yandex" {
		t.Fatalf("quick question route = %s", route)
	}
	if r.inChat() {
		t.Fatal("failed quick question left a chat open")
	}
	say(t, r, "новый чат")
	if route, _ := say(t, r, "расскажи анекдот"); route != "yandex" {
		t.Fatalf("chat route = %s", route)
	}
	if !r.inChat() {
		t.Fatal("chat closed after one failure")
	}
	if list, _ := store.List(); len(list) != 0 {
		t.Fatalf("failed answers saved: %+v", list)
	}
}

type searchLLM struct {
	fakeLLM
	modes []string
}

func (f *searchLLM) Complete(ctx context.Context, system string, m []providers.Message) (string, error) {
	f.modes = append(f.modes, providers.SearchMode(ctx))
	return f.fakeLLM.Complete(ctx, system, m)
}

type fakeSearcher struct{ ready bool }

func (f *fakeSearcher) Search(context.Context, string) (string, error) { return "", nil }
func (f *fakeSearcher) Ready() bool                                    { return f.ready }
func (f *fakeSearcher) KeyEnv() string                                 { return "TAVILY_API_KEY" }

func TestSearchModes(t *testing.T) {
	store, _ := chats.Open(t.TempDir())
	deepseek, claude := &searchLLM{}, &searchLLM{}
	searcher := &fakeSearcher{}
	r := New(Options{Models: map[string]*Model{
		"deepseek": {Key: "deepseek", Title: "DeepSeek", Config: providers.Config{BaseURL: "https://api.deepseek.com/v1"}, Completer: deepseek},
		"claude":   {Key: "claude", Title: "Claude", Aliases: []string{"клод"}, Config: providers.Config{Type: "anthropic"}, Completer: claude},
	}, DefaultModel: "deepseek", Store: store, Search: searcher})

	say(t, r, "Алиса, поговорим")
	say(t, r, "какая погода")
	if deepseek.modes[0] != "" {
		t.Fatalf("DeepSeek without a search key searched: %q", deepseek.modes[0])
	}
	if strings.Contains(deepseek.systems[0], "поиск в интернете") {
		t.Fatal("search prompt added without search")
	}
	say(t, r, "переключись на клод")
	say(t, r, "какая погода")
	if claude.modes[0] != providers.SearchNative {
		t.Fatalf("Claude without a key should use its own search, got %q", claude.modes[0])
	}
	if !strings.Contains(claude.systems[0], "Сегодня ") {
		t.Fatal("no date in the search prompt")
	}
	searcher.ready = true
	say(t, r, "а завтра")
	if claude.modes[1] != providers.SearchTool {
		t.Fatalf("with a key auto should use our search, got %q", claude.modes[1])
	}
	if err := r.Apply(Update{SearchModes: map[string]string{"claude": "native"}}); err != nil {
		t.Fatal(err)
	}
	say(t, r, "а послезавтра")
	if claude.modes[2] != providers.SearchNative {
		t.Fatalf("native setting ignored: %q", claude.modes[2])
	}
	if _, answer := say(t, r, "выключи интернет"); !strings.Contains(answer, "Выключила") {
		t.Fatalf("answer = %q", answer)
	}
	say(t, r, "ещё вопрос")
	if claude.modes[3] != "" {
		t.Fatalf("search still on: %q", claude.modes[3])
	}
	if r.Settings().SearchOn {
		t.Fatal("panel shows search on")
	}
	if _, answer := say(t, r, "включи интернет"); !strings.Contains(answer, "Включила") {
		t.Fatalf("answer = %q", answer)
	}
	if err := r.Apply(Update{SearchModes: map[string]string{"claude": "warp"}}); err == nil {
		t.Fatal("bad mode accepted")
	}
}

type failingLLM struct{ calls int }

func (f *failingLLM) Complete(context.Context, string, []providers.Message) (string, error) {
	f.calls++
	return "", context.DeadlineExceeded
}

func TestSearchModelsInOrder(t *testing.T) {
	store, _ := chats.Open(t.TempDir())
	t.Setenv("SONAR_KEY", "k")
	t.Setenv("GPT_KEY", "k")
	sonar, gpt := &failingLLM{}, &searchLLM{}
	r := New(Options{Models: map[string]*Model{
		"deepseek": {Key: "deepseek", Config: providers.Config{BaseURL: "https://api.deepseek.com/v1"}, Completer: &searchLLM{}},
		"sonar":    {Key: "sonar", Config: providers.Config{BaseURL: "https://api.perplexity.ai", Model: "sonar", APIKeyEnv: "SONAR_KEY"}, Completer: sonar},
		"gpt":      {Key: "gpt", Config: providers.Config{BaseURL: "https://api.openai.com/v1", APIKeyEnv: "GPT_KEY"}, Completer: gpt},
	}, DefaultModel: "deepseek", Store: store, Search: &fakeSearcher{}})
	if err := r.Apply(Update{SearchOrder: &[]string{"deepseek"}}); err == nil {
		t.Fatal("a model without search accepted as a search backend")
	}
	if err := r.Apply(Update{SearchOrder: &[]string{"tavily", "sonar", "gpt"}}); err != nil {
		t.Fatal(err)
	}
	search := r.searchFor(r.model())
	if search.Mode != providers.SearchTool {
		t.Fatalf("DeepSeek with search models gets %q", search.Mode)
	}
	out, err := search.Searcher.Search(context.Background(), "курс доллара")
	if err != nil || !strings.HasPrefix(out, "ответ курс доллара") {
		t.Fatalf("out %q, err %v", out, err)
	}
	if sonar.calls != 1 || len(gpt.modes) != 1 || gpt.modes[0] != providers.SearchNative || !strings.Contains(gpt.systems[0], "поисковый помощник") {
		t.Fatalf("Tavily without a key must be skipped, Sonar tried, GPT asked with its own search: sonar %d, gpt %v", sonar.calls, gpt.modes)
	}
	var backends []string
	for _, b := range r.Settings().SearchBackends {
		backends = append(backends, b.Key)
	}
	if strings.Join(backends, ",") != "tavily,gpt,sonar" {
		t.Fatalf("backends = %v", backends)
	}
}

func TestSonarSearchesByItself(t *testing.T) {
	r, _, _ := setup(t)
	r.opt.Search = &fakeSearcher{ready: true}
	m := Model{Key: "sonar", Config: providers.Config{BaseURL: "https://api.perplexity.ai", Model: "sonar"}}
	if got := r.searchFor(m).Mode; got != providers.SearchNative {
		t.Fatalf("Sonar gets %q", got)
	}
}

func TestCustomProviderSearches(t *testing.T) {
	r, _, _ := setup(t)
	c, err := r.SaveProvider(CustomProvider{Title: "Поиск", BaseURL: "https://api.example.com/v1", Model: "web-model"})
	if err != nil {
		t.Fatal(err)
	}
	web := &searchLLM{}
	r.opt.Models[c.Key].Completer = web
	if err := r.Apply(Update{SearchOrder: &[]string{c.Key}}); err != nil {
		t.Fatalf("a custom API refused as a search backend: %v", err)
	}
	deepseek := "deepseek"
	if err := r.Apply(Update{Active: &deepseek}); err != nil {
		t.Fatal(err)
	}
	search := r.searchFor(r.model())
	if search.Mode != providers.SearchTool {
		t.Fatalf("DeepSeek with a custom search API gets %q", search.Mode)
	}
	if out, err := search.Searcher.Search(context.Background(), "курс доллара"); err != nil || !strings.HasPrefix(out, "ответ курс доллара") {
		t.Fatalf("out %q, err %v", out, err)
	}
	if len(web.systems) != 1 || !strings.Contains(web.systems[0], "поисковый помощник") {
		t.Fatalf("custom API not asked as a searcher: %v", web.systems)
	}
	var backends []string
	for _, b := range r.Settings().SearchBackends {
		backends = append(backends, b.Key+":"+strconv.FormatBool(b.Ready))
	}
	if strings.Join(backends, ",") != c.Key+":true" {
		t.Fatalf("backends = %v", backends)
	}
}
