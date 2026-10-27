package aibox

import (
	"context"
	"strings"
	"testing"
	"time"

	"aiboxy/internal/chats"
	"aiboxy/internal/providers"
)

type fakeLLM struct{ calls [][]providers.Message }

func (f *fakeLLM) Complete(_ context.Context, system string, m []providers.Message) (string, error) {
	f.calls = append(f.calls, m)
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
	llm := &fakeLLM{}
	return New(Options{Provider: llm, Store: store}), llm, store
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
