package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aiboxy/internal/aibox"
)

type fake struct {
	applied []aibox.Update
	custom  bool
}

func (f *fake) Settings() aibox.Settings {
	s := aibox.Settings{Providers: []aibox.ProviderInfo{{Key: "deepseek", KeyEnv: "AIBOX_TEST_KEY"}}}
	if f.custom {
		s.Providers = append(s.Providers, aibox.ProviderInfo{Key: "api-1", KeyEnv: "AIBOX_KEY_API_1", Custom: true})
	}
	return s
}
func (f *fake) SaveProvider(c aibox.CustomProvider) (aibox.CustomProvider, error) {
	c.Key, f.custom = "api-1", true
	return c, nil
}
func (f *fake) RemoveProvider(string) (string, error) {
	f.custom = false
	return "AIBOX_KEY_API_1", nil
}
func (f *fake) Apply(u aibox.Update) error { f.applied = append(f.applied, u); return nil }
func (f *fake) ListModels(context.Context, string) ([]string, error) {
	return []string{"a", "b"}, nil
}

func do(h http.Handler, method, path, body, pass string, marked bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if pass != "" {
		req.SetBasicAuth("admin", pass)
	}
	if marked {
		req.Header.Set("X-AIBox", "1")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPanel(t *testing.T) {
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets.env")
	os.WriteFile(secrets, []byte("# keys\nAIBOX_TEST_KEY=old\nOTHER=1\n"), 0o600)
	f := &fake{}
	h := (&Server{Backend: f, SecretsPath: secrets, Password: "pw"}).Handler()

	if rec := do(h, "GET", "/", "", "", false); rec.Code != 401 {
		t.Fatalf("no password: %d", rec.Code)
	}
	if rec := do(h, "GET", "/", "", "pw", false); rec.Code != 200 || !strings.Contains(rec.Body.String(), "AIBox") {
		t.Fatalf("page: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/settings", `{}`, "pw", false); rec.Code != 403 {
		t.Fatalf("unmarked post: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/settings", `{"keys":{"AIBOX_TEST_KEY":"x; rm -rf /"}}`, "pw", true); rec.Code != 400 {
		t.Fatalf("unsafe key accepted: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/settings", `{"keys":{"PATH":"sk-12345678"}}`, "pw", true); rec.Code != 400 {
		t.Fatalf("foreign env accepted: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/settings", `{"user_prompt":"ты пират","keys":{"AIBOX_TEST_KEY":"sk-new_123456"}}`, "pw", true); rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	data, _ := os.ReadFile(secrets)
	if got := string(data); got != "# keys\nAIBOX_TEST_KEY='sk-new_123456'\nOTHER=1\n" {
		t.Fatalf("secrets = %q", got)
	}
	if os.Getenv("AIBOX_TEST_KEY") != "sk-new_123456" {
		t.Fatal("env not updated")
	}
	if len(f.applied) != 1 || *f.applied[0].UserPrompt != "ты пират" {
		t.Fatal("update not applied")
	}
	if rec := do(h, "GET", "/api/models?provider=deepseek", "", "pw", false); !strings.Contains(rec.Body.String(), `"a"`) {
		t.Fatalf("models: %s", rec.Body)
	}
}

func TestCustomProviderKeys(t *testing.T) {
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets.env")
	h := (&Server{Backend: &fake{}, SecretsPath: secrets, Password: "pw"}).Handler()
	if rec := do(h, "POST", "/api/providers", `{"title":"Groq","api_key":"gsk_abcdefgh123"}`, "pw", true); rec.Code != 200 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	data, _ := os.ReadFile(secrets)
	if !strings.Contains(string(data), "AIBOX_KEY_API_1='gsk_abcdefgh123'") || os.Getenv("AIBOX_KEY_API_1") == "" {
		t.Fatalf("key not stored: %q", data)
	}
	if rec := do(h, "DELETE", "/api/providers/api-1", "", "pw", false); rec.Code != 403 {
		t.Fatalf("unmarked delete: %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/providers/api-1", "", "pw", true); rec.Code != 200 {
		t.Fatalf("delete: %d", rec.Code)
	}
	data, _ = os.ReadFile(secrets)
	if strings.Contains(string(data), "AIBOX_KEY_API_1") || os.Getenv("AIBOX_KEY_API_1") != "" {
		t.Fatalf("key left behind: %q", data)
	}
}
