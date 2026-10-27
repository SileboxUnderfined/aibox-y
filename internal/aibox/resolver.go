// Package aibox decides which Alice requests become LLM chats and answers them.
package aibox

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"aiboxy/internal/bridge"
	"aiboxy/internal/chats"
	"aiboxy/internal/providers"
)

const DefaultSystemPrompt = "Ты голосовой ассистент в умной колонке. Отвечай по-русски, коротко и по делу, обычно в два-четыре предложения. Ответ будет прочитан вслух: без Markdown, списков, эмодзи, ссылок и формул; числа и сокращения пиши так, как их произносят."

type Options struct {
	Provider       providers.Completer
	Store          *chats.Store
	SystemPrompt   string
	Idle           time.Duration
	MaxHistory     int
	RequestTimeout time.Duration
}

type session struct {
	chat      *chats.Chat
	temporary bool
	lastUsed  time.Time
}

// Resolver implements bridge.RoutedResolver: everything that is not a chat
// command or part of an open chat goes to Yandex untouched.
type Resolver struct {
	opt    Options
	mu     sync.Mutex
	active *session
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
	return &Resolver{opt: opt, now: time.Now}
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
	answer := r.handle(ctx, text)
	return bridge.Resolution{Route: "llm", Answer: answer}, nil
}

func (r *Resolver) handle(ctx context.Context, text string) string {
	c := parse(text, r.inChat())
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
				return "Не получилось сохранить чат."
			}
		}
		r.open(&session{chat: chat})
		if name != "" {
			return "Создала чат «" + name + "». Слушаю."
		}
		return "Новый чат. Слушаю."
	case cmdContinue:
		chat, err := r.opt.Store.Find(c.name)
		if err != nil {
			return "Не нашла чат «" + c.name + "». " + r.listText()
		}
		r.open(&session{chat: chat})
		return "Продолжаем чат «" + chat.Name + "». Слушаю."
	case cmdTemporary:
		r.open(&session{chat: &chats.Chat{ID: chats.NewID()}, temporary: true})
		return "Давай поговорим. Этот разговор я не сохраню."
	case cmdForget:
		chat, err := r.opt.Store.Find(c.name)
		if err != nil {
			return "Не нашла чат «" + c.name + "»."
		}
		if err := r.opt.Store.Delete(chat.ID); err != nil {
			return "Не получилось удалить чат."
		}
		r.mu.Lock()
		if r.active != nil && r.active.chat.ID == chat.ID {
			r.active = nil
		}
		r.mu.Unlock()
		return "Забыла чат «" + chat.Name + "»."
	case cmdList:
		return r.listText()
	case cmdExit:
		r.mu.Lock()
		r.active = nil
		r.mu.Unlock()
		return "Выхожу из чата."
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

func (r *Resolver) ask(ctx context.Context, text string) string {
	r.mu.Lock()
	s := r.active
	r.mu.Unlock()
	if s == nil {
		return "Чат закрыт. Скажите «новый чат» или «поговорить»."
	}
	question := strings.TrimSpace(text)
	history := s.chat.Messages
	if len(history) > r.opt.MaxHistory {
		history = history[len(history)-r.opt.MaxHistory:]
	}
	messages := append(append([]providers.Message{}, history...), providers.Message{Role: "user", Content: question})
	ctx, cancel := context.WithTimeout(ctx, r.opt.RequestTimeout)
	defer cancel()
	started := r.now()
	answer, err := r.opt.Provider.Complete(ctx, r.opt.SystemPrompt, messages)
	slog.Info("llm_answer", "ok", err == nil, "ms", time.Since(started).Milliseconds(), "chars", len(answer))
	if err != nil {
		if errors.Is(err, providers.ErrNoKey) {
			return "Не задан ключ API для нейросети."
		}
		if ctx.Err() != nil {
			return "Нейросеть не ответила вовремя."
		}
		return "Не получилось получить ответ нейросети."
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
		go r.title(chat, question)
	}
	return answer
}

// title asks the model for a short spoken name for a new chat.
func (r *Resolver) title(chat *chats.Chat, question string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	name, err := r.opt.Provider.Complete(ctx, "Придумай название для разговора по первому вопросу: одно-три простых русских слова, которые легко произнести. Выведи только название, без кавычек и точки.", []providers.Message{{Role: "user", Content: question}})
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
