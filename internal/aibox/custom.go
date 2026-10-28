package aibox

import (
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"aiboxy/internal/providers"
)

// CustomProvider is an API endpoint added from the web panel.
type CustomProvider struct {
	Key       string   `json:"key"`
	Title     string   `json:"title"`
	Type      string   `json:"type"` // "openai" or "anthropic"
	BaseURL   string   `json:"base_url"`
	Model     string   `json:"model"`
	Aliases   []string `json:"aliases,omitempty"`
	MaxTokens int      `json:"max_tokens,omitempty"`
	// SearchOnly keeps the provider out of chats: it only searches the web.
	SearchOnly bool `json:"search_only,omitempty"`
}

// KeyEnv is where the provider's API key lives in secrets.env.
func (c CustomProvider) KeyEnv() string {
	return "AIBOX_KEY_" + strings.ToUpper(strings.ReplaceAll(c.Key, "-", "_"))
}

func (c CustomProvider) config() providers.Config {
	maxTokens := c.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 600
	}
	return providers.Config{Type: c.Type, BaseURL: c.BaseURL, Model: c.Model, APIKeyEnv: c.KeyEnv(), MaxTokens: maxTokens, Title: c.Title, Aliases: c.Aliases}
}

func (c CustomProvider) model() (*Model, error) {
	cfg := c.config()
	completer, err := providers.New(cfg)
	if err != nil {
		return nil, err
	}
	return &Model{Key: c.Key, Title: c.Title, Aliases: c.Aliases, Config: cfg, Completer: completer, SearchOnly: c.SearchOnly}, nil
}

func (c *CustomProvider) validate() error {
	c.Title = strings.TrimSpace(c.Title)
	c.Model = strings.TrimSpace(c.Model)
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if c.Type == "" {
		c.Type = "openai"
	}
	if c.Type != "openai" && c.Type != "anthropic" {
		return errors.New("тип должен быть openai или anthropic")
	}
	if c.Title == "" || len([]rune(c.Title)) > 40 {
		return errors.New("нужно название до 40 символов")
	}
	if c.Model == "" {
		return errors.New("нужна модель")
	}
	u, err := url.Parse(c.BaseURL)
	if c.Type == "openai" || c.BaseURL != "" {
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("адрес API должен начинаться с http:// или https://")
		}
	}
	var aliases []string
	for _, a := range c.Aliases {
		if a = strings.TrimSpace(a); a != "" {
			aliases = append(aliases, a)
		}
	}
	c.Aliases = aliases
	return nil
}

func (r *Resolver) isCustom(key string) int {
	for i, c := range r.state.Custom {
		if c.Key == key {
			return i
		}
	}
	return -1
}

// SaveProvider adds a custom provider, or replaces one when Key is set.
func (r *Resolver) SaveProvider(c CustomProvider) (CustomProvider, error) {
	if err := c.validate(); err != nil {
		return c, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	index := -1
	if c.Key != "" {
		if index = r.isCustom(c.Key); index < 0 {
			return c, errors.New("встроенного провайдера здесь не изменить")
		}
	} else {
		for n := 1; ; n++ {
			c.Key = "api-" + strconv.Itoa(n)
			if _, taken := r.opt.Models[c.Key]; !taken {
				break
			}
		}
	}
	m, err := c.model()
	if err != nil {
		return c, err
	}
	if index >= 0 {
		r.state.Custom[index] = c
	} else {
		r.state.Custom = append(r.state.Custom, c)
	}
	if c.SearchOnly {
		if r.state.Model == c.Key {
			r.state.Model = r.opt.DefaultModel
		}
		if len(r.state.SearchOrder) == 0 {
			// The first search provider starts searching without more setup.
			r.state.SearchOrder = []string{c.Key}
		}
	}
	delete(r.state.ModelIDs, c.Key)
	r.opt.Models[c.Key] = m
	return c, saveState(r.opt.StatePath, r.state)
}

// RemoveProvider deletes a custom provider and returns its key variable.
func (r *Resolver) RemoveProvider(key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	index := r.isCustom(key)
	if index < 0 {
		return "", errors.New("можно удалить только добавленный провайдер")
	}
	env := r.state.Custom[index].KeyEnv()
	r.state.Custom = append(r.state.Custom[:index], r.state.Custom[index+1:]...)
	delete(r.opt.Models, key)
	delete(r.state.ModelIDs, key)
	delete(r.state.SearchModes, key)
	r.state.SearchOrder = slices.DeleteFunc(r.state.SearchOrder, func(k string) bool { return k == key })
	if r.state.Model == key {
		r.state.Model = r.opt.DefaultModel
	}
	return env, saveState(r.opt.StatePath, r.state)
}
