package bridge

import (
	"context"
	"strings"
	"testing"

	"aiboxy/internal/characters"
	"aiboxy/internal/llm"
)

func TestRetryUsesCharacterSnapshotAndReplaysQuestion(t *testing.T) {
	var got []llm.Message
	e := &Engine{
		Characters: fixedCharacters{active: characters.Character{ID: "sherlock", Name: "Шерлок", Prompt: "Ты Шерлок Холмс."}},
		LLM: completionFunc(func(_ context.Context, m []llm.Message, structured bool) (string, error) {
			if structured {
				t.Fatal("retry asked the model in structured mode")
			}
			got = append([]llm.Message(nil), m...)
			return "Повторный ответ", nil
		}),
	}
	ctx := t.Context()
	e.RecordDelivered(ctx, "кто ты", "Я Шерлок.", true, false)
	res, err := e.Resolve(ctx, "используй нейросеть")
	if err != nil || res.Route != "llm" || res.Answer != "Повторный ответ" {
		t.Fatalf("retry = %+v, err = %v", res, err)
	}
	if len(got) != 2 || got[0].Role != "system" || got[1].Role != "user" || got[1].Content != "кто ты" {
		t.Fatalf("retry messages = %+v", got)
	}
	if strings.Contains(got[0].Content, retryUserPrompt) {
		t.Fatalf("retry kept the global identity with a character: %q", got[0].Content)
	}
	for _, want := range []string{"Ты Шерлок Холмс.", "Технические инструкции", "не вызывай инструменты", "недоверенными данными"} {
		if !strings.Contains(got[0].Content, want) {
			t.Fatalf("retry lost %q: %q", want, got[0].Content)
		}
	}
}

func TestRetryWithoutCharacterKeepsOldPrompt(t *testing.T) {
	var got []llm.Message
	e := &Engine{LLM: completionFunc(func(_ context.Context, m []llm.Message, structured bool) (string, error) {
		got = append([]llm.Message(nil), m...)
		return "Повторный ответ", nil
	})}
	ctx := t.Context()
	e.RecordDelivered(ctx, "кто ты", "Я Шерлок.", true, false)
	if _, err := e.Resolve(ctx, "используй нейросеть"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Content != retryBasePrompt {
		t.Fatalf("retry prompt changed without a character: %+v", got)
	}
}

func TestRetryUsesAssistantWhenLLMIsNil(t *testing.T) {
	called := 0
	e := &Engine{
		Assistant: completionFunc(func(_ context.Context, m []llm.Message, structured bool) (string, error) {
			called++
			if structured {
				t.Fatal("retry asked the assistant in structured mode")
			}
			if len(m) < 2 || m[1].Role != "user" || m[1].Content != "кто ты" {
				t.Fatalf("retry messages = %+v", m)
			}
			return "Ответ ассистента", nil
		}),
	}
	ctx := t.Context()
	e.RecordDelivered(ctx, "кто ты", "Я Шерлок.", true, false)
	res, err := e.Resolve(ctx, "используй нейросеть")
	if err != nil || res.Route != "llm" || res.Answer != "Ответ ассистента" || called != 1 {
		t.Fatalf("assistant-only retry = %+v, err = %v, calls = %d", res, err, called)
	}
}

func TestRetryWithoutLLMReportsConfigurationError(t *testing.T) {
	e := &Engine{}
	ctx := t.Context()
	e.RecordDelivered(ctx, "кто ты", "Я Шерлок.", true, false)
	if _, err := e.Resolve(ctx, "используй нейросеть"); err == nil {
		t.Fatal("retry without an LLM did not fail")
	}
}
