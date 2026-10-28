package providers

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

// Searcher looks a query up on the web and returns short text the model can
// read: a few results, each a title, a site and an excerpt.
type Searcher interface {
	Search(ctx context.Context, query string) (string, error)
}

// Search modes for one request.
const (
	SearchTool   = "tool"   // our web_search function, run by Searcher
	SearchNative = "native" // the provider's own search
)

type Search struct {
	Mode     string
	Searcher Searcher
}

type searchKey struct{}

// WithSearch lets the model reach the web during this completion. Without it
// a completion never searches, so titles and other side requests stay cheap.
func WithSearch(ctx context.Context, s Search) context.Context {
	if s.Mode == "" || (s.Mode == SearchTool && s.Searcher == nil) {
		return ctx
	}
	return context.WithValue(ctx, searchKey{}, s)
}

func searchFrom(ctx context.Context) Search {
	s, _ := ctx.Value(searchKey{}).(Search)
	return s
}

// NativeSearch names the provider's own web search, or "" when it has none.
// "OpenAI-compatible" APIs only share the request format: web search is a
// feature of OpenAI itself, of OpenRouter and of Anthropic.
func NativeSearch(cfg Config) string {
	switch {
	case cfg.Type != "anthropic" && (strings.Contains(cfg.BaseURL, "perplexity.ai") || strings.HasPrefix(cfg.Model, "perplexity/") || strings.HasSuffix(cfg.Model, ":online")):
		// Perplexity Sonar and OpenRouter's :online models search on every request.
		return "self"
	case cfg.Type == "anthropic" && (cfg.BaseURL == "" || strings.Contains(cfg.BaseURL, "anthropic.com")):
		return "anthropic"
	case cfg.Type == "anthropic":
		return ""
	case strings.Contains(cfg.BaseURL, "api.openai.com"):
		return "openai"
	case strings.Contains(cfg.BaseURL, "openrouter.ai"):
		return "openrouter"
	}
	return ""
}

// maxSearchRounds bounds the tool loop: after it the model must answer.
const maxSearchRounds = 3

const searchToolName = "web_search"

const searchToolDescription = "Поиск в интернете. Вызывай, когда нужны свежие или точные факты: новости, погода, курсы, цены, расписания, результаты матчей, события после твоих знаний. Для общих вопросов не вызывай. Запрос пиши коротко, как в поисковике."

var searchToolParameters = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Поисковый запрос"}},"required":["query"]}`)

// runSearch executes one web_search call and never fails: an error becomes
// text, so the model can still answer from what it knows.
func runSearch(ctx context.Context, s Searcher, arguments string) string {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil || strings.TrimSpace(args.Query) == "" {
		return "Поиск не выполнен: пустой запрос."
	}
	out, err := s.Search(ctx, args.Query)
	if err != nil {
		return "Поиск не удался. Ответь по своим знаниям и скажи, что данные могут быть неточными."
	}
	if strings.TrimSpace(out) == "" {
		return "Ничего не нашлось."
	}
	return out
}

var (
	markdownLink = regexp.MustCompile(`\[([^\]]+)\]\((?:https?://|www\.)[^)]*\)`)
	bareURL      = regexp.MustCompile(`\(?(?:https?://|www\.)\S+\)?`)
	citation     = regexp.MustCompile(`\[\d+(?:[,\s]*\d+)*\]|【[^】]*】`)
	emphasis     = regexp.MustCompile(`\*\*|__|\x60`)
	spaces       = regexp.MustCompile(`[ \t]{2,}`)
	spaceBefore  = regexp.MustCompile(`\s+([.,!?;:])`)
)

// speakable removes what search answers tend to carry and TTS would read
// out: links, addresses and citation marks.
func speakable(text string) string {
	text = markdownLink.ReplaceAllString(text, "$1")
	text = bareURL.ReplaceAllString(text, "")
	text = citation.ReplaceAllString(text, "")
	text = emphasis.ReplaceAllString(text, "")
	text = spaces.ReplaceAllString(text, " ")
	text = spaceBefore.ReplaceAllString(text, "$1")
	return strings.TrimSpace(text)
}

// SearchMode is the search mode this completion runs with, "" for none.
func SearchMode(ctx context.Context) string { return searchFrom(ctx).Mode }
