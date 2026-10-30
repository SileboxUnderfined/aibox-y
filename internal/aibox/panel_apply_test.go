package aibox

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"aiboxy/internal/chats"
	"aiboxy/internal/providers"
)

func panelTestResolver(t *testing.T) (*Resolver, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := chats.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "state.json")
	if err := saveState(statePath, state{
		Model:        "deepseek",
		SystemPrompt: "исходный промпт",
		SearchModes:  map[string]string{"claude": "tool"},
		SearchOrder:  []string{"tavily"},
		Commands:     map[string]Phrases{"new_chat": {Add: []string{"новая беседа"}}},
	}); err != nil {
		t.Fatal(err)
	}
	models := map[string]*Model{
		"deepseek": {
			Key:       "deepseek",
			Title:     "DeepSeek",
			Config:    providers.Config{Type: "openai", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat"},
			Completer: &fakeLLM{},
		},
		"claude": {
			Key:       "claude",
			Title:     "Claude",
			Config:    providers.Config{Type: "anthropic", Model: "claude-sonnet-4-5"},
			Completer: &fakeLLM{},
		},
	}
	r := New(Options{Models: models, DefaultModel: "deepseek", StatePath: statePath, Store: store})
	return r, statePath
}

func TestApplyValidationChangesNothing(t *testing.T) {
	r, statePath := panelTestResolver(t)
	before := r.state.clone()
	beforeFile, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeModel := r.opt.Models["deepseek"].Config.Model
	changed := "этот промпт не должен сохраниться"
	badOrder := []string{"unknown-backend"}
	badExcluded := []string{"unknown-provider"}

	cases := []struct {
		name string
		u    Update
	}{
		{"bad search order after a valid mode", Update{
			SearchModes: map[string]string{"claude": "native"},
			SearchOrder: &badOrder,
			UserPrompt:  &changed,
		}},
		{"bad model id", Update{
			ModelIDs:   map[string]string{"deepseek": "  "},
			UserPrompt: &changed,
		}},
		{"bad active provider", Update{
			Active:     &badOrder[0],
			UserPrompt: &changed,
		}},
		{"unknown command", Update{
			Commands:   map[string]*Phrases{"no_such_command": {Add: []string{"x"}}},
			UserPrompt: &changed,
		}},
		{"unknown memory exclusion", Update{
			MemoryExcluded: &badExcluded,
			UserPrompt:     &changed,
		}},
		{"bad search mode", Update{
			SearchModes: map[string]string{"deepseek": "warp"},
			UserPrompt:  &changed,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := r.Apply(tc.u); err == nil {
				t.Fatal("invalid update accepted")
			}
			if !reflect.DeepEqual(r.state, before) {
				t.Fatalf("state changed on validation error:\nbefore %+v\nafter  %+v", before, r.state)
			}
			afterFile, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			if string(afterFile) != string(beforeFile) {
				t.Fatal("state file changed on validation error")
			}
			if got := r.opt.Models["deepseek"].Config.Model; got != beforeModel {
				t.Fatalf("provider model changed: %q", got)
			}
			if got := r.CurrentModel(); got != "deepseek" {
				t.Fatalf("active model changed: %q", got)
			}
			if r.Settings().UserPrompt != "исходный промпт" {
				t.Fatal("prompt changed on validation error")
			}
		})
	}
}

func TestApplyRollsBackWhenStateCannotBeSaved(t *testing.T) {
	r, _ := panelTestResolver(t)
	before := r.state.clone()
	beforeModel := *r.opt.Models["deepseek"]
	// A missing directory makes saveState fail after the in-memory changes.
	r.opt.StatePath = filepath.Join(t.TempDir(), "missing", "state.json")
	changed := "не сохранится"
	if err := r.Apply(Update{UserPrompt: &changed, SearchModes: map[string]string{"claude": "native"}}); err == nil {
		t.Fatal("apply succeeded with an unwritable state path")
	}
	if !reflect.DeepEqual(r.state, before) {
		t.Fatalf("state not rolled back:\nbefore %+v\nafter  %+v", before, r.state)
	}
	if !reflect.DeepEqual(*r.opt.Models["deepseek"], beforeModel) {
		t.Fatal("provider model not rolled back")
	}
}

func TestProviderChangesRollBackWhenStateCannotBeSaved(t *testing.T) {
	r, statePath := panelTestResolver(t)
	before := r.state.clone()
	beforeProviders := len(r.Settings().Providers)
	// Add with an unwritable state path must not leave a provider in memory.
	r.opt.StatePath = filepath.Join(t.TempDir(), "missing", "state.json")
	if _, err := r.SaveProvider(CustomProvider{Title: "Groq", BaseURL: "https://api.groq.com/openai/v1", Model: "llama"}); err == nil {
		t.Fatal("SaveProvider succeeded with an unwritable state path")
	}
	if !reflect.DeepEqual(r.state, before) {
		t.Fatal("state changed after a failed provider add")
	}
	if got := len(r.Settings().Providers); got != beforeProviders {
		t.Fatalf("provider count changed: %d -> %d", beforeProviders, got)
	}
	if _, ok := r.opt.Models["api-1"]; ok {
		t.Fatal("provider model left behind")
	}

	// A failed remove must keep the provider both in memory and on disk.
	r.opt.StatePath = statePath
	c, err := r.SaveProvider(CustomProvider{Title: "Groq", BaseURL: "https://api.groq.com/openai/v1", Model: "llama"})
	if err != nil {
		t.Fatal(err)
	}
	beforeRemove := r.state.clone()
	r.opt.StatePath = filepath.Join(t.TempDir(), "missing", "state.json")
	if _, err := r.RemoveProvider(c.Key); err == nil {
		t.Fatal("RemoveProvider succeeded with an unwritable state path")
	}
	if !reflect.DeepEqual(r.state, beforeRemove) {
		t.Fatal("state changed after a failed provider remove")
	}
	if _, ok := r.opt.Models[c.Key]; !ok {
		t.Fatal("provider model removed despite a failed state save")
	}
	found := false
	for _, p := range r.Settings().Providers {
		if p.Key == c.Key {
			found = true
		}
	}
	if !found {
		t.Fatal("provider missing after a failed remove")
	}
}
