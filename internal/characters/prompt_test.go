package characters

import (
	"strings"
	"testing"
)

func TestPromptOrderAndOneCharacterCopy(t *testing.T) {
	base := "Общие правила: отвечай голосом, без Markdown."
	context := "Что пользователь рассказал о себе: меня зовут Валерий."
	active := Character{ID: "sherlock", Name: "Шерлок", Prompt: "Ты Шерлок Холмс.", Rules: []string{"Говори кратко.", "Не выдумывай."}}
	got := Prompt(base, active, context)
	baseAt := strings.Index(got, base)
	roleAt := strings.Index(got, "Ты Шерлок Холмс.")
	rulesAt := strings.Index(got, "Правила персонажа")
	contextAt := strings.Index(got, context)
	if baseAt < 0 || roleAt < 0 || rulesAt < 0 || contextAt < 0 {
		t.Fatalf("a layer is missing: %q", got)
	}
	if !(baseAt < roleAt && roleAt < rulesAt && rulesAt < contextAt) {
		t.Fatalf("layer order base=%d role=%d rules=%d context=%d", baseAt, roleAt, rulesAt, contextAt)
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
	base := "Ты голосовой ассистент."
	active := Character{ID: "sherlock", Name: "Шерлок", Prompt: "Ты Шерлок Холмс.", Rules: []string{"Говори кратко."}}
	got := Prompt(base, active, "")

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
	if !strings.HasPrefix(got, base) {
		t.Fatalf("the base prompt was changed: %q", got)
	}
	if strings.Index(got, "важнее общего описания ассистента") > strings.Index(got, "Технические инструкции") {
		t.Fatal("identity precedence must not move technical instructions ahead of the role and rules")
	}
	if strings.Index(got, "Технические инструкции") < strings.Index(got, "Правила персонажа") {
		t.Fatal("technical priority must follow the character rules")
	}
	if !strings.Contains(got, "сохраняя роль") {
		t.Fatalf("honest AI self-description should keep the role: %q", got)
	}
}

func TestPromptDefaultKeepsOldBehavior(t *testing.T) {
	base := "старый системный промпт"
	context := "контекст поиска и памяти"
	if got := Prompt(base, Default(), ""); got != base {
		t.Fatalf("default without context = %q", got)
	}
	if got, want := Prompt(base, Default(), context), base+"\n\n"+context; got != want {
		t.Fatalf("default with context = %q, want %q", got, want)
	}
	if strings.Contains(Prompt(base, Default(), context), DefaultName) {
		t.Fatal("the default character should not add a layer")
	}
	empty := Character{ID: "empty"}
	if got := Prompt(base, empty, ""); got != base {
		t.Fatalf("an empty stored character should not change the prompt: %q", got)
	}
}
