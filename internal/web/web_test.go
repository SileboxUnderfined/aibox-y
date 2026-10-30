package web

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aiboxy/internal/aibox"
	"aiboxy/internal/chats"
	"aiboxy/internal/providers"
)

type fake struct {
	applied  []aibox.Update
	applyErr error
	custom   bool
	facts    []string
	deleted  []string
}

func (f *fake) AddFact(text string) error { f.facts = append(f.facts, "add "+text); return nil }
func (f *fake) EditFact(id, text string) error {
	f.facts = append(f.facts, "edit "+id+" "+text)
	return nil
}
func (f *fake) DeleteFact(id string) error { f.facts = append(f.facts, "delete "+id); return nil }
func (f *fake) ClearFacts() error          { f.facts = append(f.facts, "clear"); return nil }

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
func (f *fake) Apply(u aibox.Update) error {
	f.applied = append(f.applied, u)
	return f.applyErr
}
func (f *fake) ListModels(context.Context, string) ([]string, error) {
	return []string{"a", "b"}, nil
}
func (f *fake) Status() aibox.Status {
	return aibox.Status{Status: "ok", Model: "deepseek", ModelTitle: "DeepSeek", Providers: 1, ReadyProviders: 1, Chats: 2, MemoryFacts: 3}
}
func (f *fake) Chats() ([]aibox.ChatSummary, error) {
	return []aibox.ChatSummary{{ID: "0123456789abcdef", Name: "Тестовый чат", Messages: 2}}, nil
}
func (f *fake) Chat(id string) (*chats.Chat, error) {
	if id != "0123456789abcdef" {
		return nil, chats.ErrNotFound
	}
	return &chats.Chat{ID: id, Name: "Тестовый чат", Messages: []providers.Message{{Role: "user", Content: "привет"}}}, nil
}
func (f *fake) DeleteChat(id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func do(h http.Handler, method, path, body string, marked bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if marked {
		req.Header.Set("X-AIBox", "1")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestOldPasswordFileIsIgnoredAndKept(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "web.password")
	const legacy = "legacy-panel-password\n"
	if err := os.WriteFile(old, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Backend: &fake{}, SecretsPath: filepath.Join(dir, "secrets.env")}).Handler()
	rec := do(h, "GET", "/", "", false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "AIBox") {
		t.Fatalf("page with old password file: %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("password challenge still sent: %q", got)
	}
	data, err := os.ReadFile(old)
	if err != nil || string(data) != legacy {
		t.Fatalf("old password file was changed or removed: %q, %v", data, err)
	}
}

func TestNoPasswordForReadsOrWrites(t *testing.T) {
	h := (&Server{Backend: &fake{}, SecretsPath: filepath.Join(t.TempDir(), "secrets.env")}).Handler()
	for _, path := range []string{"/", "/api/settings", "/api/status", "/api/chats"} {
		rec := do(h, "GET", path, "", false)
		if rec.Code == http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "" {
			t.Fatalf("%s asks for a password: %d", path, rec.Code)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s without credentials: %d %s", path, rec.Code, rec.Body)
		}
	}
	rec := do(h, "POST", "/api/settings", `{}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("write without credentials: %d %s", rec.Code, rec.Body)
	}
}

func TestOriginAndFetchMetadataGuard(t *testing.T) {
	h := (&Server{Backend: &fake{}, SecretsPath: filepath.Join(t.TempDir(), "secrets.env")}).Handler()
	cases := []struct {
		name, method, path, body, origin, site string
		marked                                 bool
		tls                                    bool
		want                                   int
	}{
		{"direct read", "GET", "/", "", "", "", false, false, http.StatusOK},
		{"same origin read", "GET", "/api/settings", "", "http://example.com", "", false, false, http.StatusOK},
		{"foreign origin read", "GET", "/api/settings", "", "http://evil.example", "", false, false, http.StatusForbidden},
		{"foreign origin write", "POST", "/api/settings", `{}`, "http://evil.example", "", true, false, http.StatusForbidden},
		{"null origin", "GET", "/api/settings", "", "null", "", false, false, http.StatusForbidden},
		{"cross-site metadata read", "GET", "/api/settings", "", "", "cross-site", false, false, http.StatusForbidden},
		{"cross-site metadata write", "POST", "/api/settings", `{}`, "", "cross-site", true, false, http.StatusForbidden},
		{"same-origin metadata read", "GET", "/api/settings", "", "", "same-origin", false, false, http.StatusOK},
		{"write without origin", "POST", "/api/settings", `{}`, "", "", true, false, http.StatusOK},
		{"write without marker", "POST", "/api/settings", `{}`, "", "", false, false, http.StatusForbidden},
		{"foreign preflight", "OPTIONS", "/api/settings", "", "http://evil.example", "cross-site", false, false, http.StatusForbidden},
		{"https origin on http listener", "GET", "/api/settings", "", "https://example.com", "", false, false, http.StatusForbidden},
		{"http origin on https listener", "GET", "/api/settings", "", "http://example.com", "", false, true, http.StatusForbidden},
		{"correct https origin", "GET", "/api/settings", "", "https://example.com", "", false, true, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.site != "" {
				req.Header.Set("Sec-Fetch-Site", tc.site)
			}
			if tc.marked {
				req.Header.Set("X-AIBox", "1")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("code = %d, want %d, body = %s", rec.Code, tc.want, rec.Body)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Fatalf("permissive CORS header: %q", got)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != "" {
				t.Fatalf("password challenge: %q", got)
			}
		})
	}
}

func TestSameOriginSchemeHostPort(t *testing.T) {
	cases := []struct {
		name, host, origin string
		tls, want          bool
	}{
		{"http default", "example.com", "http://example.com", false, true},
		{"http explicit default port", "example.com", "http://example.com:80", false, true},
		{"http explicit listener port", "example.com:8100", "http://example.com:8100", false, true},
		{"https default", "example.com", "https://example.com", true, true},
		{"https explicit default port", "example.com:443", "https://example.com", true, true},
		{"https explicit listener port", "example.com:8100", "https://example.com:8100", true, true},
		{"https origin on http listener", "example.com", "https://example.com", false, false},
		{"http origin on https listener", "example.com", "http://example.com", true, false},
		{"port mismatch", "example.com:8100", "http://example.com:8101", false, false},
		{"host mismatch", "example.com", "http://evil.example", false, false},
		{"userinfo rejected", "example.com", "http://user@example.com", false, false},
		{"no origin http", "example.com", "", false, true},
		{"no origin https", "example.com", "", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/settings", nil)
			req.Host = tc.host
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if got := sameOrigin(req); got != tc.want {
				t.Fatalf("sameOrigin() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOriginGuardOverActualTLSServer(t *testing.T) {
	h := (&Server{Backend: &fake{}, SecretsPath: filepath.Join(t.TempDir(), "secrets.env")}).Handler()
	srv := httptest.NewTLSServer(h)
	defer srv.Close()
	cases := []struct {
		name, origin string
		want         int
	}{
		{"matching https origin", srv.URL, http.StatusOK},
		{"http origin on tls listener", strings.Replace(srv.URL, "https://", "http://", 1), http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("GET", srv.URL+"/api/settings", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Origin", tc.origin)
			res, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("code = %d, want %d", res.StatusCode, tc.want)
			}
		})
	}
}

func TestForwardedHeadersDoNotChangeOriginScheme(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/settings", nil)
	req.Host = "example.com"
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "example.com")
	req.Header.Set("Forwarded", "proto=https;host=example.com")
	if sameOrigin(req) {
		t.Fatal("X-Forwarded-Proto/Forwarded changed the HTTP listener scheme")
	}

	req = httptest.NewRequest("GET", "/api/settings", nil)
	req.Host = "example.com"
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("X-Forwarded-Host", "evil.example")
	if sameOrigin(req) {
		t.Fatal("X-Forwarded-Host changed the request host")
	}
}

func TestPanel(t *testing.T) {
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets.env")
	os.WriteFile(secrets, []byte("# keys\nAIBOX_TEST_KEY=old\nOTHER=1\n"), 0o600)
	f := &fake{}
	h := (&Server{Backend: f, SecretsPath: secrets}).Handler()

	if rec := do(h, "GET", "/", "", false); rec.Code != 200 || !strings.Contains(rec.Body.String(), "AIBox") {
		t.Fatalf("page without password: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/settings", `{}`, false); rec.Code != 403 {
		t.Fatalf("unmarked post: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/settings", `{"keys":{"AIBOX_TEST_KEY":"x; rm -rf /"}}`, true); rec.Code != 400 {
		t.Fatalf("unsafe key accepted: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/settings", `{"keys":{"PATH":"sk-12345678"}}`, true); rec.Code != 400 {
		t.Fatalf("foreign env accepted: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/settings", `{"user_prompt":"ты пират","keys":{"AIBOX_TEST_KEY":"sk-new_123456"}}`, true); rec.Code != 200 {
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
	if rec := do(h, "GET", "/api/models?provider=deepseek", "", false); !strings.Contains(rec.Body.String(), `"a"`) {
		t.Fatalf("models: %s", rec.Body)
	}
}

func TestCustomProviderKeys(t *testing.T) {
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets.env")
	h := (&Server{Backend: &fake{}, SecretsPath: secrets}).Handler()
	if rec := do(h, "POST", "/api/providers", `{"title":"Groq","api_key":"gsk_abcdefgh123"}`, true); rec.Code != 200 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	data, _ := os.ReadFile(secrets)
	if !strings.Contains(string(data), "AIBOX_KEY_API_1='gsk_abcdefgh123'") || os.Getenv("AIBOX_KEY_API_1") == "" {
		t.Fatalf("key not stored: %q", data)
	}
	if rec := do(h, "DELETE", "/api/providers/api-1", "", false); rec.Code != 403 {
		t.Fatalf("unmarked delete: %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/providers/api-1", "", true); rec.Code != 200 {
		t.Fatalf("delete: %d", rec.Code)
	}
	data, _ = os.ReadFile(secrets)
	if strings.Contains(string(data), "AIBOX_KEY_API_1") || os.Getenv("AIBOX_KEY_API_1") != "" {
		t.Fatalf("key left behind: %q", data)
	}
}

func TestMemoryEndpoints(t *testing.T) {
	f := &fake{}
	h := (&Server{Backend: f, SecretsPath: filepath.Join(t.TempDir(), "secrets.env")}).Handler()
	if rec := do(h, "POST", "/api/memory", `{"text":"кот Барсик"}`, false); rec.Code != 403 {
		t.Fatalf("unmarked change allowed: %d", rec.Code)
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/memory", `{"text":"кот Барсик"}`},
		{"PUT", "/api/memory/ab12", `{"text":"кот Мурзик"}`},
		{"DELETE", "/api/memory/ab12", ""},
		{"DELETE", "/api/memory", ""},
	} {
		if rec := do(h, c.method, c.path, c.body, true); rec.Code != 200 {
			t.Fatalf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	if got := strings.Join(f.facts, "|"); got != "add кот Барсик|edit ab12 кот Мурзик|delete ab12|clear" {
		t.Fatalf("facts = %s", got)
	}
}

func TestPanelSections(t *testing.T) {
	h := (&Server{Backend: &fake{}, SecretsPath: filepath.Join(t.TempDir(), "secrets.env")}).Handler()
	rec := do(h, "GET", "/", "", false)
	body := rec.Body.String()
	for _, want := range []string{`data-route="overview"`, `data-route="providers"`, `data-route="chats"`, `data-route="memory"`, `data-route="commands"`, `data-route="search"`, `data-route="settings"`, "prefers-color-scheme:dark", "aria-live", "h1:focus{outline:3px", "revealActiveNav"} {
		if !strings.Contains(body, want) {
			t.Errorf("page misses %q", want)
		}
	}
	if strings.Contains(body, `src="http`) || strings.Contains(body, `href="http`) {
		t.Error("page loads an external resource")
	}
	if strings.Contains(body, "h1:focus{outline:none}") {
		t.Error("programmatic h1 focus has no visible outline")
	}
}

func TestStatusAndChatsAPI(t *testing.T) {
	f := &fake{}
	h := (&Server{Backend: f, SecretsPath: filepath.Join(t.TempDir(), "secrets.env")}).Handler()
	if rec := do(h, "GET", "/api/status", "", false); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"model":"deepseek"`) {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/chats", "", false); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Тестовый чат") || strings.Contains(rec.Body.String(), "привет") {
		t.Fatalf("chat list: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/chats/0123456789abcdef", "", false); rec.Code != 200 || !strings.Contains(rec.Body.String(), "привет") {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/chats/..%2f..%2fconfig", "", false); rec.Code != 404 {
		t.Fatalf("bad id accepted: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "DELETE", "/api/chats/0123456789abcdef", "", false); rec.Code != 403 {
		t.Fatalf("unmarked chat delete: %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/chats/0123456789abcdef", "", true); rec.Code != 200 {
		t.Fatalf("chat delete: %d %s", rec.Code, rec.Body)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "0123456789abcdef" {
		t.Fatalf("deleted = %v", f.deleted)
	}
}

func TestSettingsArePartial(t *testing.T) {
	f := &fake{}
	h := (&Server{Backend: f, SecretsPath: filepath.Join(t.TempDir(), "secrets.env")}).Handler()
	if rec := do(h, "POST", "/api/settings", `{"user_prompt":"только промпт"}`, true); rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	if len(f.applied) != 1 {
		t.Fatalf("applied = %d", len(f.applied))
	}
	u := f.applied[0]
	if u.UserPrompt == nil || *u.UserPrompt != "только промпт" || u.Active != nil || u.SearchOn != nil || u.MemoryEnabled != nil || len(u.Commands) != 0 {
		t.Fatalf("unexpected update: %+v", u)
	}
}

func TestRejectedApplyDoesNotWriteKey(t *testing.T) {
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets.env")
	initial := "# keys\nAIBOX_TEST_KEY='old-key-123'\nOTHER=1\n"
	if err := os.WriteFile(secrets, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIBOX_TEST_KEY", "old-key-123")
	f := &fake{applyErr: errors.New("bad update")}
	h := (&Server{Backend: f, SecretsPath: secrets}).Handler()
	rec := do(h, "POST", "/api/settings", `{"user_prompt":"не применится","keys":{"AIBOX_TEST_KEY":"sk-should-not-save"}}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}
	data, err := os.ReadFile(secrets)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != initial {
		t.Fatalf("secrets changed on rejected update:\n%s", data)
	}
	if got := os.Getenv("AIBOX_TEST_KEY"); got != "old-key-123" {
		t.Fatalf("environment changed on rejected update: %q", got)
	}
	if len(f.applied) != 1 {
		t.Fatalf("backend calls = %d", len(f.applied))
	}
}

func TestInvalidKeyDoesNotApplySettings(t *testing.T) {
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets.env")
	initial := "AIBOX_TEST_KEY='old-key-123'\n"
	if err := os.WriteFile(secrets, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fake{}
	h := (&Server{Backend: f, SecretsPath: secrets}).Handler()
	rec := do(h, "POST", "/api/settings", `{"user_prompt":"не применится","keys":{"AIBOX_TEST_KEY":"bad key with spaces"}}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}
	if len(f.applied) != 0 {
		t.Fatalf("backend applied %d updates for an invalid key", len(f.applied))
	}
	data, _ := os.ReadFile(secrets)
	if string(data) != initial {
		t.Fatalf("secrets changed: %q", data)
	}
}

func TestHTTP400LeavesSettingsAndKeysUntouched(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	initialState := `{"model":"deepseek","system_prompt":"исходный промпт"}`
	if err := os.WriteFile(statePath, []byte(initialState), 0o600); err != nil {
		t.Fatal(err)
	}
	secrets := filepath.Join(dir, "secrets.env")
	initialSecrets := "AIBOX_INTEGRATION_KEY='old-integration-key'\nOTHER=1\n"
	if err := os.WriteFile(secrets, []byte(initialSecrets), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIBOX_INTEGRATION_KEY", "old-integration-key")
	store, err := chats.Open(filepath.Join(dir, "chats"))
	if err != nil {
		t.Fatal(err)
	}
	r := aibox.New(aibox.Options{
		Models: map[string]*aibox.Model{
			"deepseek": {
				Key:   "deepseek",
				Title: "DeepSeek",
				Config: providers.Config{
					Type: "openai", BaseURL: "https://api.deepseek.com/v1",
					Model: "deepseek-chat", APIKeyEnv: "AIBOX_INTEGRATION_KEY",
				},
			},
		},
		DefaultModel: "deepseek",
		StatePath:    statePath,
		Store:        store,
	})
	h := (&Server{Backend: r, SecretsPath: secrets}).Handler()
	beforeState, _ := os.ReadFile(statePath)
	rec := do(h, "POST", "/api/settings", `{"active":"missing","user_prompt":"новый промпт","keys":{"AIBOX_INTEGRATION_KEY":"sk-new-key-123"}}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}
	afterState, _ := os.ReadFile(statePath)
	if string(afterState) != string(beforeState) {
		t.Fatal("state file changed on HTTP 400")
	}
	afterSecrets, _ := os.ReadFile(secrets)
	if string(afterSecrets) != initialSecrets {
		t.Fatalf("secrets changed on HTTP 400:\n%s", afterSecrets)
	}
	if got := os.Getenv("AIBOX_INTEGRATION_KEY"); got != "old-integration-key" {
		t.Fatalf("environment changed on HTTP 400: %q", got)
	}
	if got := r.Settings(); got.Active != "deepseek" || got.UserPrompt != "исходный промпт" {
		t.Fatalf("resolver settings changed on HTTP 400: %+v", got)
	}
}
