package aibox

import (
	"context"
	"errors"
	"os"
	"strings"

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
}

// Update carries the fields the panel changed; nil means unchanged.
type Update struct {
	Active     *string           `json:"active,omitempty"`
	UserPrompt *string           `json:"user_prompt,omitempty"`
	BasePrompt *string           `json:"base_prompt,omitempty"`
	ModelIDs   map[string]string `json:"model_ids,omitempty"`
}

func (r *Resolver) Settings() Settings {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Settings{Active: r.state.Model, UserPrompt: r.state.SystemPrompt, BasePrompt: r.state.BasePrompt, DefaultBasePrompt: r.opt.SystemPrompt}
	for _, key := range sortedKeys(r.opt.Models) {
		m := r.opt.Models[key]
		s.Providers = append(s.Providers, ProviderInfo{Key: key, Title: m.spoken(), Type: providerType(m.Config), Model: m.Config.Model, KeyEnv: m.Config.APIKeyEnv, HasKey: os.Getenv(m.Config.APIKeyEnv) != "", BaseURL: m.Config.BaseURL, Aliases: m.Aliases, Custom: r.isCustom(key) >= 0})
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
