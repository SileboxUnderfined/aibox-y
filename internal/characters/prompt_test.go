package characters

import (
	"strings"
	"testing"
)

const (
	testRequired = "ВСТРОЕННЫЙ_ОБЯЗАТЕЛЬНЫЙ_МАРКЕР"
	testGlobal   = "ГЛОБАЛЬНЫЙ_БАЗОВЫЙ_МАРКЕР"
	testContext  = "КОНТЕКСТ_ПАМЯТИ_МАРКЕР"
)

func TestPromptCharacterReplacesGlobalLayer(t *testing.T) {
	active := Character{ID: "sherlock", Name: "Шерлок", Prompt: "Ты Шерлок Холмс.", Rules: []string{"Говори кратко.", "Не выдумывай."}}
	got := Prompt(testRequired, testGlobal, active, testContext)

	if strings.Contains(got, testGlobal) {
		t.Fatalf("the global user layer leaked into the character prompt: %q", got)
	}
	for _, want := range []string{testRequired, "Ты Шерлок Холмс.", "Правила персонажа", testContext} {
		if !strings.Contains(got, want) {
			t.Fatalf("a layer is missing: %q not in %q", want, got)
		}
	}
	requiredAt := strings.Index(got, testRequired)
	roleAt := strings.Index(got, "Ты Шерлок Холмс.")
	rulesAt := strings.Index(got, "Правила персонажа")
	contextAt := strings.Index(got, testContext)
	if !(requiredAt < roleAt && roleAt < rulesAt && rulesAt < contextAt) {
		t.Fatalf("layer order required=%d role=%d rules=%d context=%d", requiredAt, roleAt, rulesAt, contextAt)
	}
	if strings.Count(got, "Ты Шерлок Холмс.") != 1 || strings.Count(got, "Правила персонажа") != 1 {
		t.Fatalf("character layer repeated: %q", got)
	}
	if !strings.Contains(got, "Технические инструкции") || !strings.Contains(got, "важнее") {
		t.Fatalf("technical priority is not documented in the prompt: %q", got)
	}
	if strings.Index(got, "Технические инструкции") < rulesAt {
		t.Fatal("priority statement must follow the character rules")
	}
}

func TestPromptCharacterOwnsIdentityWithoutOverridingTechnicalRules(t *testing.T) {
	active := Character{ID: "sherlock", Name: "Шерлок", Prompt: "Ты Шерлок Холмс.", Rules: []string{"Говори кратко."}}
	got := Prompt(BuiltinPrompt, DefaultUserPrompt, active, "")

	for _, want := range []string{
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
		if !strings.Contains(got, want) {
			t.Fatalf("identity/precedence text %q is missing: %q", want, got)
		}
	}
	if strings.Contains(got, DefaultUserPrompt) {
		t.Fatalf("the default global identity was not replaced: %q", got)
	}
	// BuiltinPrompt also names the technical priority, so the character's own
	// statement is the last occurrence.
	technicalAt := strings.LastIndex(got, "Технические инструкции")
	if strings.Index(got, "важнее общего описания ассистента") > technicalAt {
		t.Fatal("identity precedence must not move technical instructions ahead of the role and rules")
	}
	if technicalAt < strings.Index(got, "Правила персонажа") {
		t.Fatal("technical priority must follow the character rules")
	}
	if !strings.Contains(got, "сохраняя роль") {
		t.Fatalf("honest AI self-description should keep the role: %q", got)
	}
}

func TestPromptDefaultKeepsOldBehavior(t *testing.T) {
	global := "старый системный промпт"
	context := "контекст поиска и памяти"
	if got := Prompt(testRequired, global, Default(), ""); got != global {
		t.Fatalf("default without context = %q", got)
	}
	if got, want := Prompt(testRequired, global, Default(), context), global+"\n\n"+context; got != want {
		t.Fatalf("default with context = %q, want %q", got, want)
	}
	if strings.Contains(Prompt(testRequired, global, Default(), context), testRequired) {
		t.Fatal("the mandatory layer must not be duplicated when the default global prompt already has it")
	}
	if strings.Contains(Prompt(testRequired, global, Default(), context), DefaultName) {
		t.Fatal("the default character should not add a layer")
	}
	empty := Character{ID: "empty"}
	if got := Prompt(testRequired, global, empty, ""); got != testRequired {
		t.Fatalf("a stored character replaces the global layer even without its own text: %q", got)
	}
}

func TestDefaultPromptCompositionIsStable(t *testing.T) {
	const want = "Ты голосовой ассистент в умной колонке. Отвечай по-русски, коротко и по делу, обычно в два-четыре предложения. Ответ будет прочитан вслух: без Markdown, списков, эмодзи, ссылок и формул; числа и сокращения пиши так, как их произносят."
	if got := DefaultUserPrompt + " " + VoicePrompt; got != want {
		t.Fatalf("default global prompt changed: %q", got)
	}
	if !strings.Contains(BuiltinPrompt, VoicePrompt) {
		t.Fatal("the mandatory layer must keep the voice rules")
	}
}
