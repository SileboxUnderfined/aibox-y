package aibox

import (
	"fmt"
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

func validSearchMode(mode string) bool {
	switch mode {
	case searchAuto, searchTool, searchNative, searchOff:
		return true
	}
	return false
}

// searchFor picks how this model reaches the web. Auto prefers our search:
// its excerpts are short and cheap, while native search sends whole pages
// to the model and some providers search on every request.
func (r *Resolver) searchFor(m Model) providers.Search {
	r.mu.Lock()
	off, mode := r.state.SearchOff, r.state.SearchModes[m.Key]
	r.mu.Unlock()
	if mode == "" {
		mode = searchAuto
	}
	ours := r.opt.Search != nil && r.opt.Search.Ready()
	native := providers.NativeSearch(m.Config) != ""
	switch {
	case off || mode == searchOff:
	case (mode == searchAuto || mode == searchTool) && ours:
		return providers.Search{Mode: providers.SearchTool, Searcher: r.opt.Search}
	case (mode == searchAuto || mode == searchNative) && native:
		return providers.Search{Mode: providers.SearchNative}
	}
	return providers.Search{}
}

var months = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}

// searchPrompt tells the model today's date, which it needs to judge what
// is fresh, and how to speak what it found.
func searchPrompt(now time.Time) string {
	return fmt.Sprintf("Сегодня %d %s %d года. У тебя есть поиск в интернете: пользуйся им, когда нужны свежие или точные данные. Найденное перескажи своими словами в двух-трёх предложениях, без ссылок, адресов сайтов и сносок; источник называй, только если об этом спросили.", now.Day(), months[now.Month()-1], now.Year())
}
