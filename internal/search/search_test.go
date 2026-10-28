package search

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTavilyFormatsShortExcerpts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tvly-key" || r.URL.Path != "/search" {
			t.Errorf("request %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		data, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(data), `"query":"погода"`) || !strings.Contains(string(data), `"max_results":4`) {
			t.Errorf("body = %s", data)
		}
		_, _ = io.WriteString(w, `{"answer":"Плюс пять.","results":[{"title":"Погода","url":"https://www.gismeteo.ru/weather","content":"`+strings.Repeat("я", 900)+`"}]}`)
	}))
	defer srv.Close()
	t.Setenv("TAVILY_API_KEY", "tvly-key")
	s, err := New(Config{BaseURL: srv.URL})
	if err != nil || !s.Ready() {
		t.Fatal(err)
	}
	out, err := s.Search(context.Background(), "погода")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "Краткая сводка: Плюс пять.\n1. Погода (gismeteo.ru): яяя") || strings.Contains(out, "https") {
		t.Fatalf("out = %q", out)
	}
	if n := len([]rune(out)); n > 600 {
		t.Fatalf("excerpt not cut: %d runes", n)
	}
}

func TestTavilyWithoutKey(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "")
	s, _ := New(Config{})
	if s.Ready() {
		t.Fatal("ready without a key")
	}
	if _, err := s.Search(context.Background(), "x"); err != ErrNoKey {
		t.Fatalf("err = %v", err)
	}
}
