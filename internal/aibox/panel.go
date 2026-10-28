package aibox

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"

	"aiboxy/internal/providers"
)

// ProviderInfo describes one configured provider for the web panel. The API
// key itself never leaves the device; only whether it is set.
type ProviderInfo struct {
	Key     string   `json:"key"`
	Title   string   `json:"title"`
	Type    string   `json:"type"`
	Model   string   `json:"model"`
	KeyEnv  string   `json:"key_env"`
	HasKey  bool     `json:"has_key"`
	BaseURL string   `json:"base_url"`
	Aliases []string `json:"aliases"`
	Custom  bool     `json:"custom"`
	// SearchMode is the panel setting; Search is what it means now:
	// "tool", "native" or "" for none.
	SearchMode   string `json:"search_mode"`
	Search       string `json:"search"`
	NativeSearch bool   `json:"native_search"`
	SearchOnly   bool   `json:"search_only"`
}

type Settings struct {
	Active            string         `json:"active"`
	UserPrompt        string         `json:"user_prompt"`
	BasePrompt        string         `json:"base_prompt"`
	DefaultBasePrompt string         `json:"default_base_prompt"`
	Providers         []ProviderInfo `json:"providers"`
	SearchOn          bool           `json:"search_on"`
	SearchKeyEnv      string         `json:"search_key_env"`
	SearchHasKey      bool           `json:"search_has_key"`
	// SearchOrder is who runs web_search, first to last; SearchBackends is
	// what can be put there.
	SearchOrder    []string        `json:"search_order"`
	SearchBackends []SearchBackend `json:"search_backends"`
}

type SearchBackend struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Ready bool   `json:"ready"`
	// Known is false for a chat model AIBox does not know to search.
	Known bool `json:"known"`
}

// Update carries the fields the panel changed; nil means unchanged.
type Update struct {
	Active     *string           `json:"active,omitempty"`
	UserPrompt *string           `json:"user_prompt,omitempty"`
	BasePrompt *string           `json:"base_prompt,omitempty"`
	ModelIDs   map[string]string `json:"model_ids,omitempty"`
	SearchOn   *bool             `json:"search_on,omitempty"`
	// SearchModes maps a provider to "auto", "tool", "native" or "off".
	SearchModes map[string]string `json:"search_modes,omitempty"`
	SearchOrder *[]string         `json:"search_order,omitempty"`
}

func (r *Resolver) Settings() Settings {
	r.mu.Lock()
	s := Settings{Active: r.state.Model, UserPrompt: r.state.SystemPrompt, BasePrompt: r.state.BasePrompt, DefaultBasePrompt: r.opt.SystemPrompt, SearchOn: !r.state.SearchOff}
	if r.opt.Search != nil {
		s.SearchKeyEnv, s.SearchHasKey = r.opt.Search.KeyEnv(), r.opt.Search.Ready()
	}
	var models []Model
	for _, key := range sortedKeys(r.opt.Models) {
		m := r.opt.Models[key]
		mode := r.state.SearchModes[key]
		if mode == "" {
			mode = searchAuto
		}
		models = append(models, *m)
		s.Providers = append(s.Providers, ProviderInfo{Key: key, Title: m.spoken(), Type: providerType(m.Config), Model: m.Config.Model, KeyEnv: m.Config.APIKeyEnv, HasKey: os.Getenv(m.Config.APIKeyEnv) != "", BaseURL: m.Config.BaseURL, Aliases: m.Aliases, Custom: r.isCustom(key) >= 0, SearchOnly: m.SearchOnly, SearchMode: mode, NativeSearch: providers.NativeSearch(m.Config) != ""})
	}
	r.mu.Unlock()
	for i, m := range models {
		s.Providers[i].Search = r.searchFor(m).Mode
	}
	s.SearchOrder = r.searchOrder()
	if r.opt.Search != nil {
		s.SearchBackends = append(s.SearchBackends, SearchBackend{Key: tavilyBackend, Title: "Tavily", Ready: r.backendReady(tavilyBackend)})
	}
	// Search providers first, then models known to search, then the rest:
	// any model may search, the user knows what theirs can do.
	rank := func(m Model) int {
		switch {
		case m.SearchOnly:
			return 0
		case canSearch(m.Config):
			return 1
		}
		return 2
	}
	sorted := slices.Clone(models)
	slices.SortStableFunc(sorted, func(a, b Model) int { return rank(a) - rank(b) })
	for _, m := range sorted {
		s.SearchBackends = append(s.SearchBackends, SearchBackend{Key: m.Key, Title: m.spoken() + " (" + m.Config.Model + ")", Ready: r.backendReady(m.Key), Known: rank(m) < 2})
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
		if m, ok := r.opt.Models[*u.Active]; !ok || m.SearchOnly {
			return errors.New("unknown provider")
		}
	}
	for key, id := range u.ModelIDs {
		if _, ok := r.opt.Models[key]; !ok || strings.TrimSpace(id) == "" {
			return errors.New("bad model for " + key)
		}
	}
	for key, mode := range u.SearchModes {
		if _, ok := r.opt.Models[key]; !ok || !validSearchMode(mode) {
			return errors.New("bad search mode for " + key)
		}
	}
	for key, mode := range u.SearchModes {
		if r.state.SearchModes == nil {
			r.state.SearchModes = map[string]string{}
		}
		if mode == searchAuto {
			delete(r.state.SearchModes, key)
		} else {
			r.state.SearchModes[key] = mode
		}
	}
	if u.SearchOrder != nil {
		var order []string
		for _, key := range *u.SearchOrder {
			if _, ok := r.opt.Models[key]; key != tavilyBackend && !ok {
				return errors.New("unknown search provider: " + key)
			}
			if !slices.Contains(order, key) {
				order = append(order, key)
			}
		}
		r.state.SearchOrder = order
	}
	if u.SearchOn != nil {
		r.state.SearchOff = !*u.SearchOn
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
