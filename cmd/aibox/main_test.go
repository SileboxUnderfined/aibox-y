package main

import (
	"strings"
	"testing"

	"aiboxy/internal/aibox"
	"aiboxy/internal/characters"
)

func TestAskSystemPromptWithoutCharacterKeepsDefault(t *testing.T) {
	if got := askSystemPrompt(characters.Default()); got != aibox.DefaultSystemPrompt {
		t.Fatalf("one-shot prompt changed without a character: %q", got)
	}
}

func TestAskSystemPromptCharacterReplacesGlobalLayer(t *testing.T) {
	active := characters.Character{ID: "sherlock", Name: "Шерлок", Prompt: "Ты Шерлок Холмс.", Rules: []string{"Говори кратко."}}
	got := askSystemPrompt(active)
	if strings.Contains(got, characters.DefaultUserPrompt) {
		t.Fatalf("global identity was not replaced: %q", got)
	}
	if !strings.HasPrefix(got, characters.BuiltinPrompt) {
		t.Fatalf("mandatory layer is missing: %q", got)
	}
	for _, want := range []string{"Ты Шерлок Холмс.", "Правила персонажа", "Технические инструкции"} {
		if !strings.Contains(got, want) {
			t.Fatalf("one-shot character prompt lacks %q: %q", want, got)
		}
	}
}
