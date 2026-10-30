package aibox

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"aiboxy/internal/chats"
	"aiboxy/internal/memory"
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
	Memory         MemoryInfo      `json:"memory"`
	Commands       []CommandInfo   `json:"commands"`
}

// CommandInfo is one voice command as the panel shows it.
type CommandInfo struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Builtin []string `json:"builtin"` // examples of the built-in phrases
	// Args is "none", "optional" or "required": what may follow the phrase.
	Args    string   `json:"args"`
	Add     []string `json:"add"`
	Replace bool     `json:"replace"`
}

type SearchBackend struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Ready bool   `json:"ready"`
	// Known is false for a chat model AIBox does not know to search.
	Known bool `json:"known"`
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
	SearchOn   *bool             `json:"search_on,omitempty"`
	// SearchModes maps a provider to "auto", "tool", "native" or "off".
	SearchModes map[string]string `json:"search_modes,omitempty"`
	SearchOrder *[]string         `json:"search_order,omitempty"`

	MemoryEnabled  *bool     `json:"memory_enabled,omitempty"`
	MemoryAuto     *bool     `json:"memory_auto,omitempty"`
	MemoryExcluded *[]string `json:"memory_excluded,omitempty"`
	// Commands maps a command id to its phrases; null goes back to the config.
	Commands map[string]*Phrases `json:"commands,omitempty"`
}

// Status is a small, secret-free snapshot for the overview and the header.
type Status struct {
	Status          string `json:"status"`
	Model           string `json:"model"`
	ModelTitle      string `json:"model_title"`
	Providers       int    `json:"providers"`
	ReadyProviders  int    `json:"ready_providers"`
	Chats           int    `json:"chats"`
	MemoryFacts     int    `json:"memory_facts"`
	MemoryEnabled   bool   `json:"memory_enabled"`
	SearchOn        bool   `json:"search_on"`
	SearchReady     bool   `json:"search_ready"`
	ActiveChat      string `json:"active_chat,omitempty"`
	ActiveChatTitle string `json:"active_chat_title,omitempty"`
}

// ChatSummary is a chat list row; the messages stay in the store until one
// chat is opened.
type ChatSummary struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Messages int       `json:"messages"`
}

// Status reports what is configured now without reading any secrets.
func (r *Resolver) Status() Status {
	r.mu.Lock()
	s := Status{
		Status:        "ok",
		Model:         r.state.Model,
		Providers:     len(r.opt.Models),
		MemoryEnabled: r.memoryOnLocked(),
		SearchOn:      !r.state.SearchOff,
	}
	if m, ok := r.opt.Models[r.state.Model]; ok {
		s.ModelTitle = m.spoken()
	}
	for _, m := range r.opt.Models {
		if os.Getenv(m.Config.APIKeyEnv) != "" {
			s.ReadyProviders++
		}
	}
	if r.active != nil {
		s.ActiveChat = r.active.chat.ID
		s.ActiveChatTitle = r.active.chat.Name
	}
	r.mu.Unlock()
	s.SearchReady = r.opt.Search != nil && r.opt.Search.Ready()
	if r.opt.Memory != nil {
		s.MemoryFacts = len(r.opt.Memory.List())
	}
	if r.opt.Store != nil {
		if list, err := r.opt.Store.List(); err == nil {
			s.Chats = len(list)
		}
	}
	return s
}

// Chats lists saved chats without their messages.
func (r *Resolver) Chats() ([]ChatSummary, error) {
	out := []ChatSummary{}
	if r.opt.Store == nil {
		return out, nil
	}
	list, err := r.opt.Store.List()
	if err != nil {
		return nil, err
	}
	for _, c := range list {
		out = append(out, ChatSummary{ID: c.ID, Name: c.Name, Created: c.Created, Updated: c.Updated, Messages: len(c.Messages)})
	}
	return out, nil
}

// Chat returns one saved chat, messages included.
func (r *Resolver) Chat(id string) (*chats.Chat, error) {
	if r.opt.Store == nil {
		return nil, chats.ErrNotFound
	}
	c, err := r.opt.Store.Get(id)
	if errors.Is(err, chats.ErrNotFound) {
		return nil, errors.New("чат не найден")
	}
	return c, err
}

// DeleteChat moves a chat to the trash and closes it if it is open, exactly
// like the spoken "забудь чат" command.
func (r *Resolver) DeleteChat(id string) error {
	if r.opt.Store == nil {
		return errors.New("чаты не настроены")
	}
	if _, err := r.opt.Store.Get(id); err != nil {
		if errors.Is(err, chats.ErrNotFound) {
			return errors.New("чат не найден")
		}
		return err
	}
	if err := r.opt.Store.Delete(id); err != nil {
		return err
	}
	r.mu.Lock()
	if r.active != nil && r.active.chat.ID == id {
		r.active = nil
	}
	r.mu.Unlock()
	return nil
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
	phrases := r.phrasesLocked()
	for _, d := range commandDefs {
		p := phrases[d.id]
		s.Commands = append(s.Commands, CommandInfo{ID: d.id, Title: d.title, Builtin: d.example, Args: [...]string{"none", "optional", "required"}[d.args], Add: append([]string{}, p.Add...), Replace: p.Replace})
	}
	s.Memory = MemoryInfo{Available: r.opt.Memory != nil, Enabled: r.memoryOnLocked(), Auto: !r.state.MemoryAutoOff, Excluded: append([]string{}, r.state.MemoryExcluded...), Facts: []memory.Fact{}, Max: memory.MaxFacts}
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

	// Validate everything before touching the live state: a rejected panel
	// update must leave the settings, providers and state file as they were.
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
	var order []string
	if u.SearchOrder != nil {
		for _, key := range *u.SearchOrder {
			if _, ok := r.opt.Models[key]; key != tavilyBackend && !ok {
				return errors.New("unknown search provider: " + key)
			}
			if !slices.Contains(order, key) {
				order = append(order, key)
			}
		}
	}
	for id, p := range u.Commands {
		if _, ok := commandByID(id); !ok {
			return errors.New("unknown command: " + id)
		}
		for _, a := range p.addList() {
			if chats.Normalize(a) == "" {
				return errors.New("пустая фраза: " + a)
			}
		}
	}
	if u.MemoryExcluded != nil {
		for _, key := range *u.MemoryExcluded {
			if _, ok := r.opt.Models[key]; !ok {
				return errors.New("unknown provider")
			}
		}
	}
	// Build the replacement completers now, so a provider that cannot be
	// constructed is rejected before any model is changed.
	type modelUpdate struct {
		key      string
		config   providers.Config
		complete providers.Completer
	}
	var modelUpdates []modelUpdate
	for key, id := range u.ModelIDs {
		m := r.opt.Models[key]
		cfg := m.Config
		cfg.Model = strings.TrimSpace(id)
		completer, err := providers.New(cfg)
		if err != nil {
			return err
		}
		modelUpdates = append(modelUpdates, modelUpdate{key: key, config: cfg, complete: completer})
	}

	oldState := r.state.clone()
	oldCommands := r.cmds
	oldModels := map[string]Model{}
	for _, mu := range modelUpdates {
		oldModels[mu.key] = *r.opt.Models[mu.key]
	}
	restore := func() {
		r.state = oldState
		r.cmds = oldCommands
		for key, m := range oldModels {
			*r.opt.Models[key] = m
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
		r.state.SearchOrder = order
	}
	if u.SearchOn != nil {
		r.state.SearchOff = !*u.SearchOn
	}
	for _, mu := range modelUpdates {
		m := r.opt.Models[mu.key]
		m.Config, m.Completer = mu.config, mu.complete
		if r.state.ModelIDs == nil {
			r.state.ModelIDs = map[string]string{}
		}
		r.state.ModelIDs[mu.key] = mu.config.Model
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
	if len(u.Commands) > 0 {
		r.applyCommandsLocked(u.Commands)
	}
	if err := saveState(r.opt.StatePath, r.state); err != nil {
		restore()
		return err
	}
	return nil
}

// clone copies the mutable parts of state so a failed apply can be rolled back.
func (s state) clone() state {
	c := s
	if s.ModelIDs != nil {
		c.ModelIDs = make(map[string]string, len(s.ModelIDs))
		for k, v := range s.ModelIDs {
			c.ModelIDs[k] = v
		}
	}
	if s.SearchModes != nil {
		c.SearchModes = make(map[string]string, len(s.SearchModes))
		for k, v := range s.SearchModes {
			c.SearchModes[k] = v
		}
	}
	if s.SearchOrder != nil {
		c.SearchOrder = append([]string(nil), s.SearchOrder...)
	}
	if s.MemoryExcluded != nil {
		c.MemoryExcluded = append([]string(nil), s.MemoryExcluded...)
	}
	c.Custom = append([]CustomProvider(nil), s.Custom...)
	if s.Commands != nil {
		c.Commands = make(map[string]Phrases, len(s.Commands))
		for k, v := range s.Commands {
			v.Add = append([]string(nil), v.Add...)
			c.Commands[k] = v
		}
	}
	return c
}

func (r *Resolver) applyCommandsLocked(changes map[string]*Phrases) {
	for id, p := range changes {
		var clean Phrases
		if p != nil {
			clean = Phrases{Add: p.addList(), Replace: p.Replace}
		}
		if cfg := r.opt.Commands[id]; p == nil || (clean.Replace == cfg.Replace && slices.Equal(clean.Add, cfg.addList())) {
			delete(r.state.Commands, id)
			continue
		}
		if r.state.Commands == nil {
			r.state.Commands = map[string]Phrases{}
		}
		r.state.Commands[id] = clean
	}
	r.cmds = buildCommands(r.phrasesLocked())
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
