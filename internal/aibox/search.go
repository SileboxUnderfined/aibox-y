package aibox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"aiboxy/internal/providers"
)

// Per-model search settings.
const (
	searchAuto   = "auto"
	searchTool   = "tool"
	searchNative = "native"
	searchOff    = "off"
)

// tavilyBackend names Tavily in the search order; other entries are models.
const tavilyBackend = "tavily"

// backendTimeout leaves time for the next backend and the final answer.
const backendTimeout = 20 * time.Second

func validSearchMode(mode string) bool {
	switch mode {
	case searchAuto, searchTool, searchNative, searchOff:
		return true
	}
	return false
}

// searchFor picks how this model reaches the web. Auto prefers our
// web_search tool: the model decides when to search and the search order
// answers, while native search sends whole pages to the model and some
// providers search on every request.
func (r *Resolver) searchFor(m Model) providers.Search {
	r.mu.Lock()
	off, mode := r.state.SearchOff, r.state.SearchModes[m.Key]
	r.mu.Unlock()
	if mode == "" {
		mode = searchAuto
	}
	backends := r.readyBackends()
	native := providers.NativeSearch(m.Config) != ""
	if mode == searchAuto && providers.NativeSearch(m.Config) == "self" {
		// Sonar searches anyway, and Perplexity takes no tools.
		mode = searchNative
	}
	switch {
	case off || mode == searchOff:
	case (mode == searchAuto || mode == searchTool) && len(backends) > 0:
		return providers.Search{Mode: providers.SearchTool, Searcher: backendSearcher{r: r, keys: backends}}
	case (mode == searchAuto || mode == searchNative) && native:
		return providers.Search{Mode: providers.SearchNative}
	}
	return providers.Search{}
}

func (r *Resolver) searchOrder() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.state.SearchOrder) == 0 {
		return []string{tavilyBackend}
	}
	return slices.Clone(r.state.SearchOrder)
}

// readyBackends is the search order without entries that cannot work now.
func (r *Resolver) readyBackends() []string {
	var ready []string
	for _, key := range r.searchOrder() {
		if r.backendReady(key) {
			ready = append(ready, key)
		}
	}
	return ready
}

func (r *Resolver) backendReady(key string) bool {
	if key == tavilyBackend {
		return r.opt.Search != nil && r.opt.Search.Ready()
	}
	r.mu.Lock()
	m, ok := r.opt.Models[key]
	r.mu.Unlock()
	return ok && canSearch(m.Config) && os.Getenv(m.Config.APIKeyEnv) != ""
}

func canSearch(cfg providers.Config) bool { return providers.NativeSearch(cfg) != "" }

// backendSearcher runs web_search through the search order: the first
// backend that answers wins, so a second one covers a failing first.
type backendSearcher struct {
	r    *Resolver
	keys []string
}

func (b backendSearcher) Search(ctx context.Context, query string) (string, error) {
	err := errors.New("no search backend")
	for _, key := range b.keys {
		var out string
		started := time.Now()
		out, err = b.r.searchWith(ctx, key, query)
		slog.Info("web_search", "backend", key, "ok", err == nil, "ms", time.Since(started).Milliseconds(), "chars", len(out))
		if err == nil && strings.TrimSpace(out) != "" {
			return out, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return "", err
}

// searchWith asks one backend. A search model gets the query alone, finds
// the answer with its own search and reports the facts briefly.
func (r *Resolver) searchWith(ctx context.Context, key, query string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, backendTimeout)
	defer cancel()
	if key == tavilyBackend {
		if r.opt.Search == nil {
			return "", errors.New("no Tavily")
		}
		return r.opt.Search.Search(ctx, query)
	}
	r.mu.Lock()
	m, ok := r.opt.Models[key]
	var model Model
	if ok {
		model = *m
	}
	r.mu.Unlock()
	if !ok {
		return "", errors.New("unknown search model")
	}
	ctx = providers.WithSearch(ctx, providers.Search{Mode: providers.SearchNative})
	return model.Completer.Complete(ctx, searcherPrompt(r.now()), []providers.Message{{Role: "user", Content: query}})
}

var months = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}

func today(now time.Time) string {
	return fmt.Sprintf("%d %s %d года", now.Day(), months[now.Month()-1], now.Year())
}

// searchPrompt tells the model today's date, which it needs to judge what
// is fresh, and how to speak what it found.
func searchPrompt(now time.Time) string {
	return "Сегодня " + today(now) + ". У тебя есть поиск в интернете: пользуйся им, когда нужны свежие или точные данные. Найденное перескажи своими словами в двух-трёх предложениях, без ссылок, адресов сайтов и сносок; источник называй, только если об этом спросили."
}

// searcherPrompt is for a search model: its answer is read by another model.
func searcherPrompt(now time.Time) string {
	return "Сегодня " + today(now) + ". Ты поисковый помощник другой нейросети. Найди в интернете ответ на запрос и изложи только факты: до пяти коротких предложений, с числами, датами и названиями, по-русски. Если данные расходятся, скажи об этом. Без ссылок и сносок."
}
