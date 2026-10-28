package aibox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aiboxy/internal/chats"
	"aiboxy/internal/providers"
)

// Model is one selectable LLM: a provider entry from the config plus the
// words people use to name it out loud.
type Model struct {
	Key       string
	Title     string
	Aliases   []string
	Config    providers.Config
	Completer providers.Completer
}

// state is what voice commands and the web panel change; it survives restarts.
type state struct {
	Model        string            `json:"model,omitempty"`
	SystemPrompt string            `json:"system_prompt,omitempty"`
	BasePrompt   string            `json:"base_prompt,omitempty"`
	ModelIDs     map[string]string `json:"model_ids,omitempty"`
	Custom       []CustomProvider  `json:"custom_providers,omitempty"`
	// The shared memory is on by default, so the flags say what is off.
	MemoryOff      bool     `json:"memory_off,omitempty"`
	MemoryAutoOff  bool     `json:"memory_auto_off,omitempty"`
	MemoryExcluded []string `json:"memory_excluded,omitempty"` // models that never see the facts
}

func loadState(path string) state {
	var s state
	if path == "" {
		return s
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s
}

func saveState(path string, s state) error {
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), ".state.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// findModel returns the model whose key, title or alias best matches speech.
func findModel(models map[string]*Model, spoken string) *Model {
	var best *Model
	bestScore := 0.0
	for _, key := range sortedKeys(models) {
		m := models[key]
		for _, name := range append([]string{m.Key, m.Title}, m.Aliases...) {
			if name == "" {
				continue
			}
			if score := chats.Similarity(spoken, name); score > bestScore {
				best, bestScore = m, score
			}
		}
	}
	if bestScore < chats.MatchThreshold {
		return nil
	}
	return best
}

func sortedKeys(models map[string]*Model) []string {
	keys := make([]string, 0, len(models))
	for k := range models {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (m Model) spoken() string {
	if m.Title != "" {
		return m.Title
	}
	return m.Key
}

func modelList(models map[string]*Model) string {
	names := make([]string, 0, len(models))
	for _, k := range sortedKeys(models) {
		names = append(names, models[k].spoken())
	}
	return strings.Join(names, ", ")
}
