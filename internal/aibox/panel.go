package aibox

import (
	"context"
	"errors"
	"os"
	"strings"

	"aiboxy/internal/memory"
	"aiboxy/internal/providers"
)

// ProviderInfo describes one configured provider for the web panel. The API
// key itself never leaves the device; only whether it is set.
type ProviderInfo struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Type   string `json:"type"`
	Model  string `json:"model"`
	KeyEnv  string   `json:"key_env"`
	HasKey  bool     `json:"has_key"`
	BaseURL string   `json:"base_url"`
	Aliases []string `json:"aliases"`
	Custom  bool     `json:"custom"`
}

type Settings struct {
	Active            string         `json:"active"`
	UserPrompt        string         `json:"user_prompt"`
	BasePrompt        string         `json:"base_prompt"`
	DefaultBasePrompt string         `json:"default_base_prompt"`
	Providers         []ProviderInfo `json:"providers"`
	Memory            MemoryInfo     `json:"memory"`
}

// MemoryInfo is the shared memory as the panel shows it.
type MemoryInfo struct {
	Available bool          `json:"available"`
	Enabled   bool          `json:"enabled"`
	Auto      bool          `json:"auto"`
	Excluded  []string      `json:"excluded"`
	Facts     []memory.Fact `json:"facts"`
	Max       int           `json:"max"`
}

// Update carries the fields the panel changed; nil means unchanged.
type Update struct {
	Active     *string           `json:"active,omitempty"`
	UserPrompt *string           `json:"user_prompt,omitempty"`
	BasePrompt *string           `json:"base_prompt,omitempty"`
	ModelIDs   map[string]string `json:"model_ids,omitempty"`

	MemoryEnabled  *bool     `json:"memory_enabled,omitempty"`
	MemoryAuto     *bool     `json:"memory_auto,omitempty"`
	MemoryExcluded *[]string `json:"memory_excluded,omitempty"`
}

func (r *Resolver) Settings() Settings {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Settings{Active: r.state.Model, UserPrompt: r.state.SystemPrompt, BasePrompt: r.state.BasePrompt, DefaultBasePrompt: r.opt.SystemPrompt}
	for _, key := range sortedKeys(r.opt.Models) {
		m := r.opt.Models[key]
		s.Providers = append(s.Providers, ProviderInfo{Key: key, Title: m.spoken(), Type: providerType(m.Config), Model: m.Config.Model, KeyEnv: m.Config.APIKeyEnv, HasKey: os.Getenv(m.Config.APIKeyEnv) != "", BaseURL: m.Config.BaseURL, Aliases: m.Aliases, Custom: r.isCustom(key) >= 0})
	}
	s.Memory = MemoryInfo{Available: r.opt.Memory != nil, Enabled: r.memoryOnLocked(), Auto: !r.state.MemoryAutoOff, Excluded: append([]string{}, r.state.MemoryExcluded...), Facts: []memory.Fact{}, Max: memory.MaxFacts}
	if r.opt.Memory != nil {
		s.Memory.Facts = r.opt.Memory.List()
	}
	return s
}

func providerType(c providers.Config) string {
	if c.Type == "" {
		return "openai"
	}
	return c.Type
}

func (r *Resolver) Apply(u Update) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u.Active != nil {
		if _, ok := r.opt.Models[*u.Active]; !ok {
			return errors.New("unknown provider")
		}
	}
	for key, id := range u.ModelIDs {
		if _, ok := r.opt.Models[key]; !ok || strings.TrimSpace(id) == "" {
			return errors.New("bad model for " + key)
		}
	}
	if u.MemoryExcluded != nil {
		for _, key := range *u.MemoryExcluded {
			if _, ok := r.opt.Models[key]; !ok {
				return errors.New("unknown provider")
			}
		}
	}
	for key, id := range u.ModelIDs {
		id = strings.TrimSpace(id)
		if err := r.opt.Models[key].setModelID(id); err != nil {
			return err
		}
		if r.state.ModelIDs == nil {
			r.state.ModelIDs = map[string]string{}
		}
		r.state.ModelIDs[key] = id
	}
	if u.Active != nil {
		r.state.Model = *u.Active
	}
	if u.UserPrompt != nil {
		r.state.SystemPrompt = strings.TrimSpace(*u.UserPrompt)
	}
	if u.BasePrompt != nil {
		base := strings.TrimSpace(*u.BasePrompt)
		if base == strings.TrimSpace(r.opt.SystemPrompt) {
			base = ""
		}
		r.state.BasePrompt = base
	}
	if u.MemoryEnabled != nil {
		r.state.MemoryOff = !*u.MemoryEnabled
	}
	if u.MemoryAuto != nil {
		r.state.MemoryAutoOff = !*u.MemoryAuto
	}
	if u.MemoryExcluded != nil {
		r.state.MemoryExcluded = append([]string(nil), *u.MemoryExcluded...)
	}
	return saveState(r.opt.StatePath, r.state)
}

// ListModels asks a provider which models the configured key can use.
func (r *Resolver) ListModels(ctx context.Context, key string) ([]string, error) {
	r.mu.Lock()
	m, ok := r.opt.Models[key]
	var cfg providers.Config
	if ok {
		cfg = m.Config
	}
	r.mu.Unlock()
	if !ok {
		return nil, errors.New("unknown provider")
	}
	return providers.ListModels(ctx, cfg)
}

func (m *Model) setModelID(id string) error {
	cfg := m.Config
	cfg.Model = id
	c, err := providers.New(cfg)
	if err != nil {
		return err
	}
	m.Config, m.Completer = cfg, c
	return nil
}

var errNoMemory = errors.New("memory is not configured")

// AddFact, EditFact, DeleteFact and ClearFacts change the shared memory from the panel.
func (r *Resolver) AddFact(text string) error {
	if r.opt.Memory == nil {
		return errNoMemory
	}
	_, err := r.opt.Memory.Add(text, "panel")
	return factError(err)
}

func (r *Resolver) EditFact(id, text string) error {
	if r.opt.Memory == nil {
		return errNoMemory
	}
	_, err := r.opt.Memory.Edit(id, text)
	return factError(err)
}

func (r *Resolver) DeleteFact(id string) error {
	if r.opt.Memory == nil {
		return errNoMemory
	}
	return factError(r.opt.Memory.Delete(id))
}

func (r *Resolver) ClearFacts() error {
	if r.opt.Memory == nil {
		return errNoMemory
	}
	return r.opt.Memory.Clear()
}

func factError(err error) error {
	switch {
	case errors.Is(err, memory.ErrFull):
		return errors.New("память заполнена, удалите лишнее")
	case errors.Is(err, memory.ErrEmpty):
		return errors.New("пустой факт")
	case errors.Is(err, memory.ErrNotFound):
		return errors.New("факт не найден")
	}
	return err
}
