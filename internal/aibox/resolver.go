// Package aibox decides which Alice requests become LLM chats and answers them.
package aibox

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"aiboxy/internal/bridge"
	"aiboxy/internal/chats"
	"aiboxy/internal/providers"
)

const DefaultSystemPrompt = "Ты голосовой ассистент в умной колонке. Отвечай по-русски, коротко и по делу, обычно в два-четыре предложения. Ответ будет прочитан вслух: без Markdown, списков, эмодзи, ссылок и формул; числа и сокращения пиши так, как их произносят."

type Options struct {
	Models         map[string]*Model
	DefaultModel   string
	StatePath      string // where voice-changed settings are kept
	Store          *chats.Store
	SystemPrompt   string
	Idle           time.Duration
	MaxHistory     int
	RequestTimeout time.Duration
	// KeepAntimat leaves Yandex's profanity masking on inside chats.
	KeepAntimat bool
}

type session struct {
	chat      *chats.Chat
	model     string // key of the model this chat asks, "" for the current one
	temporary bool
	lastUsed  time.Time
}

// Resolver implements bridge.RoutedResolver: everything that is not a chat
// command or part of an open chat goes to Yandex untouched.
type Resolver struct {
	opt    Options
	mu     sync.Mutex
	active *session
	state  state
	now    func() time.Time
}

func New(opt Options) *Resolver {
	if opt.SystemPrompt == "" {
		opt.SystemPrompt = DefaultSystemPrompt
	}
	if opt.Idle <= 0 {
		opt.Idle = 10 * time.Minute
	}
	if opt.MaxHistory <= 0 {
		opt.MaxHistory = 20
	}
	if opt.RequestTimeout <= 0 {
		opt.RequestTimeout = 40 * time.Second
	}
	r := &Resolver{opt: opt, now: time.Now, state: loadState(opt.StatePath)}
	if opt.Models == nil {
		opt.Models = map[string]*Model{}
		r.opt.Models = opt.Models
	}
	for _, cp := range r.state.Custom {
		m, err := cp.model()
		if err != nil {
			slog.Warn("custom_provider_rejected", "provider", cp.Key)
			continue
		}
		opt.Models[cp.Key] = m
	}
	if _, ok := opt.Models[r.state.Model]; !ok {
		r.state.Model = opt.DefaultModel
	}
	for key, id := range r.state.ModelIDs {
		if m, ok := opt.Models[key]; ok && id != "" {
			if err := m.setModelID(id); err != nil {
				slog.Warn("model_override_rejected", "provider", key)
			}
		}
	}
	return r
}

// model returns a copy, so a model change from the web panel never races a request.
func (r *Resolver) model() Model {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *r.opt.Models[r.state.Model]
}

// find returns a copy of the model named by speech, or nil.
func (r *Resolver) find(spoken string) *Model {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := findModel(r.opt.Models, spoken)
	if m == nil {
		return nil
	}
	c := *m
	return &c
}

func (r *Resolver) modelNames() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return modelList(r.opt.Models)
}

// UncensoredASR tells the proxy to ask Yandex for unmasked recognition; only
// while a chat is open, so ordinary Alice requests stay untouched.
func (r *Resolver) UncensoredASR() bool {
	return !r.opt.KeepAntimat && r.inChat()
}

// CurrentModel is the key of the model that answers now.
func (r *Resolver) CurrentModel() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.Model
}

func (r *Resolver) systemPrompt() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	base := r.opt.SystemPrompt
	if r.state.BasePrompt != "" {
		base = r.state.BasePrompt
	}
	if r.state.SystemPrompt == "" {
		return base
	}
	// The voice rules stay; the user's prompt adds a role or style on top.
	return base + "\n\nУказания пользователя: " + r.state.SystemPrompt
}

func (r *Resolver) update(change func(*state)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	change(&r.state)
	return saveState(r.opt.StatePath, r.state)
}

// inChat reports whether a chat is open, closing it after the idle timeout.
func (r *Resolver) inChat() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil && r.now().Sub(r.active.lastUsed) > r.opt.Idle {
		slog.Info("chat_idle_closed")
		r.active = nil
	}
	return r.active != nil
}

func (r *Resolver) Decide(_ context.Context, text string) bridge.RouteDecision {
	inChat := r.inChat()
	c := parse(text, inChat)
	if c.kind == cmdModelSwitch && r.find(c.name) == nil {
		c.kind = cmdNone
	}
	if c.kind != cmdNone {
		return bridge.RouteDecision{Route: "llm", Source: "aibox_command"}
	}
	if inChat {
		return bridge.RouteDecision{Route: "llm", Source: "aibox_chat"}
	}
	return bridge.RouteDecision{Route: "yandex", Source: "aibox_pass"}
}

func (r *Resolver) Resolve(ctx context.Context, text string) (bridge.Resolution, error) {
	return r.ResolveDecision(ctx, text, r.Decide(ctx, text))
}

func (r *Resolver) ResolveDecision(ctx context.Context, text string, d bridge.RouteDecision) (bridge.Resolution, error) {
	if d.Route != "llm" {
		return bridge.Resolution{Route: "yandex"}, nil
	}
	if bridge.Interrupted(ctx) && r.inChat() && stopPhrases.MatchString(cleanText(text)) {
		// "Алиса, стоп" over an answer only silences it and waits for the next question.
		r.mu.Lock()
		if r.active != nil {
			r.active.lastUsed = r.now()
		}
		r.mu.Unlock()
		slog.Info("chat_answer_stopped")
		return bridge.Resolution{Route: "llm", Silent: true}, nil
	}
	answer, ok := r.handle(ctx, text)
	if !ok {
		// Any model failure hands the request to Alice as if AIBox were not there.
		slog.Info("llm_fallback_to_yandex")
		return bridge.Resolution{Route: "yandex"}, nil
	}
	return bridge.Resolution{Route: "llm", Answer: answer}, nil
}

// handle answers a chat request; false means the model failed and Alice
// should answer instead.
func (r *Resolver) handle(ctx context.Context, text string) (string, bool) {
	c := parse(text, r.inChat())
	if c.kind == cmdModelSwitch && r.find(c.name) == nil {
		c.kind = cmdNone
	}
	slog.Info("chat_command", "kind", int(c.kind))
	switch c.kind {
	case cmdNew:
		chat := &chats.Chat{ID: chats.NewID(), Created: r.now(), Updated: r.now()}
		name := ""
		if c.name != "" {
			name = r.opt.Store.UniqueName(capitalize(c.name), chat.ID)
			chat.Name = name
		}
		if name != "" {
			if err := r.opt.Store.Save(chat); err != nil {
				return "Не получилось сохранить чат.", true
			}
		}
		r.open(&session{chat: chat})
		if name != "" {
			return "Создала чат «" + name + "». Слушаю.", true
		}
		return "Новый чат. Слушаю.", true
	case cmdContinue:
		chat, err := r.opt.Store.Find(c.name)
		if err != nil {
			return "Не нашла чат «" + c.name + "». " + r.listText(), true
		}
		r.open(&session{chat: chat})
		return "Продолжаем чат «" + chat.Name + "». Слушаю.", true
	case cmdQuick:
		model, question := r.quick(text)
		r.open(&session{chat: &chats.Chat{ID: chats.NewID(), Created: r.now(), Updated: r.now()}, model: model})
		if question == "" {
			if model != "" {
				return "Новый чат, отвечает " + r.modelFor(model).spoken() + ". Слушаю.", true
			}
			return "Новый чат. Слушаю.", true
		}
		answer, ok := r.askQuestion(ctx, question)
		if !ok {
			r.mu.Lock()
			r.active = nil // nothing was asked yet, so nothing to keep open
			r.mu.Unlock()
		}
		return answer, ok
	case cmdTemporary:
		r.open(&session{chat: &chats.Chat{ID: chats.NewID()}, temporary: true})
		return "Давай поговорим. Этот разговор я не сохраню.", true
	case cmdForget:
		chat, err := r.opt.Store.Find(c.name)
		if err != nil {
			return "Не нашла чат «" + c.name + "».", true
		}
		if err := r.opt.Store.Delete(chat.ID); err != nil {
			return "Не получилось удалить чат.", true
		}
		r.mu.Lock()
		if r.active != nil && r.active.chat.ID == chat.ID {
			r.active = nil
		}
		r.mu.Unlock()
		return "Забыла чат «" + chat.Name + "».", true
	case cmdList:
		return r.listText(), true
	case cmdExit:
		r.mu.Lock()
		r.active = nil
		r.mu.Unlock()
		return "Выхожу из чата.", true
	case cmdModel, cmdModelSwitch:
		m := r.find(c.name)
		if m == nil {
			return "Не знаю модель «" + c.name + "». Есть: " + r.modelNames() + ".", true
		}
		if err := r.update(func(s *state) {
			s.Model = m.Key
			if r.active != nil {
				r.active.model = "" // the open chat follows the switch too
			}
		}); err != nil {
			slog.Warn("state_save_failed")
		}
		return "Теперь отвечает " + m.spoken() + ".", true
	case cmdWhichModel:
		return "Сейчас отвечает " + r.model().spoken() + ". Можно выбрать: " + r.modelNames() + ".", true
	case cmdSetPrompt:
		prompt := capitalize(c.name)
		if err := r.update(func(s *state) { s.SystemPrompt = prompt }); err != nil {
			slog.Warn("state_save_failed")
		}
		return "Запомнила новый системный промпт: " + prompt + ".", true
	case cmdResetPrompt:
		if err := r.update(func(s *state) { s.SystemPrompt = "" }); err != nil {
			slog.Warn("state_save_failed")
		}
		return "Вернула обычный системный промпт.", true
	case cmdShowPrompt:
		r.mu.Lock()
		custom := r.state.SystemPrompt
		r.mu.Unlock()
		if custom == "" {
			return "Сейчас обычный системный промпт, без дополнительной роли.", true
		}
		return "Сейчас системный промпт: " + custom + ".", true
	}
	return r.ask(ctx, text)
}

func (r *Resolver) open(s *session) {
	s.lastUsed = r.now()
	r.mu.Lock()
	r.active = s
	r.mu.Unlock()
}

func (r *Resolver) listText() string {
	list, err := r.opt.Store.List()
	if err != nil || len(list) == 0 {
		return "Сохранённых чатов пока нет."
	}
	names := make([]string, 0, len(list))
	for i, c := range list {
		if i == 7 {
			break
		}
		names = append(names, c.Name)
	}
	return "Ваши чаты: " + strings.Join(names, ", ") + "."
}

func (r *Resolver) ask(ctx context.Context, text string) (string, bool) {
	return r.askQuestion(ctx, stripWake(text))
}

func (r *Resolver) askQuestion(ctx context.Context, question string) (string, bool) {
	r.mu.Lock()
	s := r.active
	r.mu.Unlock()
	if s == nil {
		return "", false
	}
	history := s.chat.Messages
	if len(history) > r.opt.MaxHistory {
		history = history[len(history)-r.opt.MaxHistory:]
	}
	messages := append(append([]providers.Message{}, history...), providers.Message{Role: "user", Content: question})
	ctx, cancel := context.WithTimeout(ctx, r.opt.RequestTimeout)
	defer cancel()
	model := r.modelFor(s.model)
	started := r.now()
	answer, err := model.Completer.Complete(ctx, r.systemPrompt(), messages)
	slog.Info("llm_answer", "ok", err == nil, "ms", time.Since(started).Milliseconds(), "chars", len(answer))
	if err == nil && strings.TrimSpace(answer) == "" {
		err = errors.New("empty answer")
	}
	if err != nil {
		slog.Warn("llm_failed", "no_key", errors.Is(err, providers.ErrNoKey), "timeout", ctx.Err() != nil)
		return "", false
	}
	r.mu.Lock()
	s.chat.Messages = append(s.chat.Messages, providers.Message{Role: "user", Content: question}, providers.Message{Role: "assistant", Content: answer})
	s.chat.Updated = r.now()
	s.lastUsed = r.now()
	needsName := !s.temporary && s.chat.Name == ""
	if needsName {
		s.chat.Name = r.opt.Store.UniqueName(fallbackName(question), s.chat.ID)
	}
	chat := s.chat
	if !s.temporary {
		// Saved under the lock: the title goroutine edits the same chat.
		if err := r.opt.Store.Save(chat); err != nil {
			slog.Warn("chat_save_failed")
		}
	}
	r.mu.Unlock()
	if needsName {
		go r.title(model, chat, question)
	}
	return answer, true
}

// title asks the model for a short spoken name for a new chat.
func (r *Resolver) title(model Model, chat *chats.Chat, question string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	name, err := model.Completer.Complete(ctx, "Придумай название для разговора по первому вопросу: одно-три простых русских слова, которые легко произнести. Выведи только название, без кавычек и точки.", []providers.Message{{Role: "user", Content: question}})
	name = strings.Trim(strings.TrimSpace(name), "«»\"'.")
	if err != nil || name == "" || len([]rune(name)) > 40 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	chat.Name = r.opt.Store.UniqueName(capitalize(name), chat.ID)
	if err := r.opt.Store.Save(chat); err != nil {
		slog.Warn("chat_save_failed")
	}
}

var wakeWord = regexp.MustCompile(`(?i)^\s*алис[аы]?[\s,.!:—-]+`)

// stripWake drops a leading "Алиса," that recognition keeps in the text.
func stripWake(text string) string {
	text = strings.TrimSpace(text)
	q := strings.TrimSpace(wakeWord.ReplaceAllString(text, ""))
	if q == "" || q == text {
		return text
	}
	return capitalize(q)
}

// modelFor returns a copy of the model with this key, or the current one.
func (r *Resolver) modelFor(key string) Model {
	r.mu.Lock()
	m, ok := r.opt.Models[key]
	r.mu.Unlock()
	if !ok {
		return r.model()
	}
	return *m
}

// quick splits a quick question into the model it names (if any) and the
// question itself, as it was recognized.
func (r *Resolver) quick(text string) (model, question string) {
	cleaned := cleanText(text)
	m := quickPattern.FindStringSubmatchIndex(cleaned)
	if m == nil {
		return "", ""
	}
	restStart := m[len(m)-2]
	if restStart < 0 {
		restStart = len(cleaned)
	}
	skip := len(strings.Fields(chats.Normalize(text))) - len(strings.Fields(cleaned)) + len(strings.Fields(cleaned[:restStart]))
	rest := strings.Fields(cleaned[restStart:])
	neuro := m[2] >= 0 || m[4] >= 0
	if !neuro {
		if key, n := r.modelPrefix(rest); key != "" {
			model, skip = key, skip+n
		}
	}
	return model, capitalize(dropWords(text, skip))
}

// quickModelThreshold is stricter than chat matching, so the first word of
// a question is not taken for a model name.
const quickModelThreshold = 0.8

// modelPrefix finds a model named by the first one to three words.
func (r *Resolver) modelPrefix(words []string) (string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key, used, best := "", 0, quickModelThreshold
	for n := 1; n <= 3 && n <= len(words); n++ {
		spoken := strings.Join(words[:n], " ")
		for _, k := range sortedKeys(r.opt.Models) {
			m := r.opt.Models[k]
			for _, name := range append([]string{m.Key, m.Title}, m.Aliases...) {
				if name == "" {
					continue
				}
				if score := chats.Similarity(spoken, name); score >= best {
					key, used, best = k, n, score
				}
			}
		}
	}
	return key, used
}

// dropWords removes the first n normalized words from recognized text and
// keeps the rest as it was said, punctuation included.
func dropWords(text string, n int) string {
	fields := strings.Fields(text)
	for len(fields) > 0 && n > 0 {
		n -= len(strings.Fields(chats.Normalize(fields[0])))
		fields = fields[1:]
	}
	return strings.TrimLeft(strings.Join(fields, " "), ",.!:;—- ")
}

func fallbackName(question string) string {
	w := strings.Fields(chats.Normalize(question))
	if len(w) > 4 {
		w = w[:4]
	}
	if len(w) == 0 {
		return "Чат"
	}
	return capitalize(strings.Join(w, " "))
}

func capitalize(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return s
	}
	return strings.ToUpper(string(r[:1])) + string(r[1:])
}
