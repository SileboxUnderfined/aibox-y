// Package search looks things up on the web for models that have no search
// of their own. Results are cut to a few short excerpts: they go into the
// model's context and the answer is spoken, so less is better.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	Provider   string `json:"provider"` // "tavily"
	APIKeyEnv  string `json:"api_key_env"`
	MaxResults int    `json:"max_results"`
	BaseURL    string `json:"base_url,omitempty"`
}

var ErrNoKey = errors.New("search API key is not set")

// Tavily is the Tavily Search API; its results already carry clean excerpts.
type Tavily struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) (*Tavily, error) {
	if cfg.Provider == "" {
		cfg.Provider = "tavily"
	}
	if cfg.Provider != "tavily" {
		return nil, fmt.Errorf("unknown search provider %q", cfg.Provider)
	}
	if cfg.APIKeyEnv == "" {
		cfg.APIKeyEnv = "TAVILY_API_KEY"
	}
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = 4
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.tavily.com"
	}
	return &Tavily{cfg: cfg, http: &http.Client{Timeout: 15 * time.Second}}, nil
}

// KeyEnv is the secrets.env variable that holds the key.
func (t *Tavily) KeyEnv() string { return t.cfg.APIKeyEnv }

// Ready reports whether a key is set; the panel can add one at any time.
func (t *Tavily) Ready() bool { return strings.TrimSpace(os.Getenv(t.cfg.APIKeyEnv)) != "" }

const excerptRunes = 500

func (t *Tavily) Search(ctx context.Context, query string) (string, error) {
	key := strings.TrimSpace(os.Getenv(t.cfg.APIKeyEnv))
	if key == "" {
		return "", ErrNoKey
	}
	body, _ := json.Marshal(map[string]any{"query": query, "max_results": t.cfg.MaxResults, "search_depth": "basic", "include_answer": "basic"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.cfg.BaseURL, "/")+"/search", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := t.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("search returned HTTP %d", resp.StatusCode)
	}
	var out struct {
		Answer  string `json:"answer"`
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", errors.New("unexpected search response")
	}
	var b strings.Builder
	if a := strings.TrimSpace(out.Answer); a != "" {
		b.WriteString("Краткая сводка: " + cut(a, excerptRunes) + "\n")
	}
	for i, r := range out.Results {
		fmt.Fprintf(&b, "%d. %s (%s): %s\n", i+1, strings.TrimSpace(r.Title), site(r.URL), cut(strings.TrimSpace(r.Content), excerptRunes))
	}
	return strings.TrimSpace(b.String()), nil
}

// site keeps only the host: the model may name a source, never read a link.
func site(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "источник"
	}
	return strings.TrimPrefix(u.Host, "www.")
}

func cut(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
