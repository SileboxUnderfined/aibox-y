package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aiboxy/internal/aibox"
	"aiboxy/internal/characters"
	"aiboxy/internal/chats"
	"aiboxy/internal/providers"
)

type characterPanel struct {
	server         *Server
	resolver       *aibox.Resolver
	dir            string
	statePath      string
	secretsPath    string
	charactersPath string
}

func newCharacterPanel(t *testing.T) characterPanel {
	t.Helper()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(statePath, []byte(`{"model":"deepseek","system_prompt":"исходный промпт"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	secretsPath := filepath.Join(dir, "secrets.env")
	if err := os.WriteFile(secretsPath, []byte("AIBOX_IO_KEY='old-key-123'\nOTHER=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := chats.Open(filepath.Join(dir, "chats"))
	if err != nil {
		t.Fatal(err)
	}
	charactersPath := filepath.Join(dir, "characters.json")
	characterStore, err := characters.Open(charactersPath)
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
					Model: "deepseek-chat", APIKeyEnv: "AIBOX_IO_KEY",
				},
			},
			"claude": {
				Key:   "claude",
				Title: "Claude",
				Config: providers.Config{
					Type: "anthropic", Model: "claude-sonnet-4-5", APIKeyEnv: "AIBOX_IO_KEY",
				},
			},
		},
		DefaultModel: "deepseek",
		StatePath:    statePath,
		Store:        store,
		Characters:   characterStore,
	})
	return characterPanel{
		server:         &Server{Backend: r, SecretsPath: secretsPath},
		resolver:       r,
		dir:            dir,
		statePath:      statePath,
		secretsPath:    secretsPath,
		charactersPath: charactersPath,
	}
}

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func newMarkedRequest(method, path, body, origin string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-AIBox", "1")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeCharacters(t *testing.T, body string) aibox.CharactersInfo {
	t.Helper()
	var info aibox.CharactersInfo
	if err := json.Unmarshal([]byte(body), &info); err != nil {
		t.Fatalf("decode characters: %v (%s)", err, body)
	}
	return info
}

func createCharacterAPI(t *testing.T, h http.Handler, in aibox.CharacterInput) aibox.CharactersInfo {
	t.Helper()
	rec := do(h, "POST", "/api/characters", jsonBody(t, in), true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	return decodeCharacters(t, rec.Body.String())
}

func TestCharactersAPILifecycleAndPersistence(t *testing.T) {
	p := newCharacterPanel(t)
	h := p.server.Handler()
	beforeState, err := os.ReadFile(p.statePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeSecrets, err := os.ReadFile(p.secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeSettings := p.resolver.Settings()

	rec := do(h, "GET", "/api/characters", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	info := decodeCharacters(t, rec.Body.String())
	if len(info.Characters) != 0 || info.Active != characters.DefaultID || info.Default.ID != characters.DefaultID || info.Default.Name != characters.DefaultName {
		t.Fatalf("initial state = %+v", info)
	}
	if info.Limits.Name != characters.MaxNameRunes || info.Limits.Prompt != characters.MaxPromptRunes || info.Limits.Rules != characters.MaxRules || info.Limits.Rule != characters.MaxRuleRunes {
		t.Fatalf("limits = %+v", info.Limits)
	}

	info = createCharacterAPI(t, h, aibox.CharacterInput{
		Name:   "  Шерлок   Холмс ",
		Prompt: "  Ты Шерлок Холмс.  ",
		Rules:  []string{"  Говори кратко. ", "", "Не выдумывай."},
	})
	if len(info.Characters) != 1 {
		t.Fatalf("after create = %+v", info)
	}
	sherlock := info.Characters[0]
	if len(sherlock.ID) != 16 || strings.Trim(sherlock.ID, "0123456789abcdef") != "" {
		t.Fatalf("generated id = %q", sherlock.ID)
	}
	if sherlock.Name != "Шерлок Холмс" || sherlock.Prompt != "Ты Шерлок Холмс." || len(sherlock.Rules) != 2 || sherlock.Active {
		t.Fatalf("created character = %+v", sherlock)
	}

	info = createCharacterAPI(t, h, aibox.CharacterInput{Name: "Ватсон", Prompt: "Ты доктор Ватсон."})
	if len(info.Characters) != 2 {
		t.Fatalf("after second create = %+v", info)
	}
	watson := info.Characters[1]

	rec = do(h, "PUT", "/api/characters/"+sherlock.ID, jsonBody(t, aibox.CharacterInput{
		Name: "Шерлок", Prompt: "Новый промпт.", Rules: []string{"Правило"},
	}), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	info = decodeCharacters(t, rec.Body.String())
	if len(info.Characters) != 2 || info.Characters[0].ID != sherlock.ID || info.Characters[0].Name != "Шерлок" || info.Characters[0].Prompt != "Новый промпт." || len(info.Characters[0].Rules) != 1 {
		t.Fatalf("updated character = %+v", info)
	}

	// Selection is global, survives a fresh store read, and can be reset.
	rec = do(h, "POST", "/api/characters/active", jsonBody(t, map[string]string{"id": sherlock.ID}), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("activate: %d %s", rec.Code, rec.Body)
	}
	info = decodeCharacters(t, rec.Body.String())
	if info.Active != sherlock.ID {
		t.Fatalf("active = %q", info.Active)
	}
	for _, c := range info.Characters {
		if c.Active != (c.ID == sherlock.ID) {
			t.Fatalf("active marker on %+v", c)
		}
	}
	reopened, err := characters.Open(p.charactersPath)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ActiveID() != sherlock.ID || len(reopened.List()) != 2 || reopened.List()[0].Prompt != "Новый промпт." {
		t.Fatalf("persisted state = %q %+v", reopened.ActiveID(), reopened.List())
	}

	rec = do(h, "POST", "/api/characters/reset", `{}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body)
	}
	info = decodeCharacters(t, rec.Body.String())
	if info.Active != characters.DefaultID || !info.Default.Active {
		t.Fatalf("reset state = %+v", info)
	}
	if reopened, err := characters.Open(p.charactersPath); err != nil || reopened.ActiveID() != characters.DefaultID {
		t.Fatalf("reset not persisted: %v %q", err, reopened.ActiveID())
	}

	// Deleting the active character falls back to the default.
	if rec = do(h, "POST", "/api/characters/active", jsonBody(t, map[string]string{"id": watson.ID}), true); rec.Code != http.StatusOK {
		t.Fatalf("activate watson: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "DELETE", "/api/characters/"+watson.ID, "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	info = decodeCharacters(t, rec.Body.String())
	if len(info.Characters) != 1 || info.Active != characters.DefaultID {
		t.Fatalf("after delete = %+v", info)
	}
	if reopened, err := characters.Open(p.charactersPath); err != nil || len(reopened.List()) != 1 || reopened.ActiveID() != characters.DefaultID {
		t.Fatalf("delete not persisted: %v %+v", err, reopened)
	}

	// The character API must not alter prompts, models or secrets.
	afterState, _ := os.ReadFile(p.statePath)
	afterSecrets, _ := os.ReadFile(p.secretsPath)
	if string(afterState) != string(beforeState) {
		t.Fatal("state.json changed through the character API")
	}
	if string(afterSecrets) != string(beforeSecrets) {
		t.Fatal("secrets.env changed through the character API")
	}
	afterSettings := p.resolver.Settings()
	if afterSettings.Active != beforeSettings.Active || afterSettings.UserPrompt != beforeSettings.UserPrompt || len(afterSettings.Providers) != len(beforeSettings.Providers) {
		t.Fatalf("resolver settings changed: before %+v after %+v", beforeSettings, afterSettings)
	}
}

func TestLegacyBareArrayCharacterIDIsEditableAndDeletable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "characters.json")
	if err := os.WriteFile(path, []byte(`[{"id":"one","name":"Старый","prompt":"p"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := characters.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r := aibox.New(aibox.Options{Characters: store})
	h := (&Server{Backend: r, SecretsPath: filepath.Join(dir, "secrets.env")}).Handler()

	rec := do(h, "PUT", "/api/characters/one", `{"name":"Новый","prompt":"q"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT legacy id: %d %s", rec.Code, rec.Body)
	}
	info := decodeCharacters(t, rec.Body.String())
	if len(info.Characters) != 1 || info.Characters[0].ID != "one" || info.Characters[0].Name != "Новый" || info.Characters[0].Prompt != "q" {
		t.Fatalf("updated legacy character = %+v", info)
	}

	rec = do(h, "DELETE", "/api/characters/one", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE legacy id: %d %s", rec.Code, rec.Body)
	}
	if list := store.List(); len(list) != 0 {
		t.Fatalf("store still has characters: %+v", list)
	}
	reopened, err := characters.Open(path)
	if err != nil || len(reopened.List()) != 0 {
		t.Fatalf("deleted legacy character came back: %+v %v", reopened, err)
	}
}

func TestCharacterAPIValidationAndErrorCodes(t *testing.T) {
	p := newCharacterPanel(t)
	h := p.server.Handler()
	beforePrompt := p.resolver.Settings().UserPrompt
	info := createCharacterAPI(t, h, aibox.CharacterInput{Name: "Первый", Prompt: "p", Rules: []string{"r"}})
	info = createCharacterAPI(t, h, aibox.CharacterInput{Name: "Второй"})
	second := info.Characters[1]

	manyRules := make([]string, characters.MaxRules+1)
	for i := range manyRules {
		manyRules[i] = "правило " + strings.Repeat("x", i%5)
	}

	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"empty name", "POST", "/api/characters", jsonBody(t, aibox.CharacterInput{Name: "   "}), http.StatusBadRequest},
		{"name too long", "POST", "/api/characters", jsonBody(t, aibox.CharacterInput{Name: strings.Repeat("я", characters.MaxNameRunes+1)}), http.StatusBadRequest},
		{"prompt too long", "POST", "/api/characters", jsonBody(t, aibox.CharacterInput{Name: "Длинный", Prompt: strings.Repeat("п", characters.MaxPromptRunes+1)}), http.StatusBadRequest},
		{"too many rules", "POST", "/api/characters", jsonBody(t, aibox.CharacterInput{Name: "Много", Rules: manyRules}), http.StatusBadRequest},
		{"rule too long", "POST", "/api/characters", jsonBody(t, aibox.CharacterInput{Name: "Правило", Rules: []string{strings.Repeat("п", characters.MaxRuleRunes+1)}}), http.StatusBadRequest},
		{"duplicate normalized name", "POST", "/api/characters", jsonBody(t, aibox.CharacterInput{Name: "  ПЕРВЫЙ "}), http.StatusConflict},
		{"duplicate rename", "PUT", "/api/characters/" + second.ID, jsonBody(t, aibox.CharacterInput{Name: "ПЕРВЫЙ"}), http.StatusConflict},
		{"invalid json", "POST", "/api/characters", `{"name":`, http.StatusBadRequest},
		{"unknown field", "POST", "/api/characters", `{"name":"X","prompt":"p","id":"hijack"}`, http.StatusBadRequest},
		{"trailing value", "POST", "/api/characters", `{"name":"X"} {"name":"Y"}`, http.StatusBadRequest},
		{"oversized body", "POST", "/api/characters", `{"name":"` + strings.Repeat("x", 70<<10) + `"}`, http.StatusRequestEntityTooLarge},
		{"unknown update", "PUT", "/api/characters/0123456789abcdef", jsonBody(t, aibox.CharacterInput{Name: "X"}), http.StatusNotFound},
		{"malformed id update", "PUT", "/api/characters/..%2f..%2fstate", jsonBody(t, aibox.CharacterInput{Name: "X"}), http.StatusNotFound},
		{"update default", "PUT", "/api/characters/default", jsonBody(t, aibox.CharacterInput{Name: "X"}), http.StatusBadRequest},
		{"delete default", "DELETE", "/api/characters/default", "", http.StatusBadRequest},
		{"delete unknown", "DELETE", "/api/characters/0123456789abcdef", "", http.StatusNotFound},
		{"activate unknown", "POST", "/api/characters/active", jsonBody(t, map[string]string{"id": "0123456789abcdef"}), http.StatusNotFound},
		{"activate empty", "POST", "/api/characters/active", jsonBody(t, map[string]string{"id": ""}), http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(h, tc.method, tc.path, tc.body, true)
			if rec.Code != tc.want {
				t.Fatalf("%s %s: code = %d, want %d, body = %s", tc.method, tc.path, rec.Code, tc.want, rec.Body)
			}
		})
	}

	if got := p.resolver.Settings().UserPrompt; got != beforePrompt {
		t.Fatalf("validation changed the user prompt: %q", got)
	}
	list, err := characters.Open(p.charactersPath)
	if err != nil {
		t.Fatal(err)
	}
	got := list.List()
	if len(got) != 2 || got[0].Name != "Первый" || got[1].Name != "Второй" {
		t.Fatalf("rejected requests changed the store: %+v", got)
	}
	if list.ActiveID() != characters.DefaultID {
		t.Fatalf("rejected requests changed the active character: %q", list.ActiveID())
	}
}

func TestCharacterAPIGuardAndNoInternalLeak(t *testing.T) {
	p := newCharacterPanel(t)
	h := p.server.Handler()

	// Writes keep the existing header + Origin protection; reads stay open on
	// this local panel but a foreign page cannot read them either.
	if rec := do(h, "POST", "/api/characters", `{"name":"Без метки"}`, false); rec.Code != http.StatusForbidden {
		t.Fatalf("unmarked create: %d", rec.Code)
	}
	req := newMarkedRequest("POST", "/api/characters", `{"name":"Чужой"}`, "http://evil.example")
	rec := serve(h, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin create: %d", rec.Code)
	}
	req = newMarkedRequest("GET", "/api/characters", "", "http://evil.example")
	if rec := serve(h, req); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin read: %d", rec.Code)
	}
	req = newMarkedRequest("POST", "/api/characters/active", `{"id":"default"}`, "")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if rec := serve(h, req); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site create: %d", rec.Code)
	}

	// A backend failure is logged locally but the browser gets a generic
	// message, never a path or an os error.
	bad := &fake{characterErr: errors.New("open /srv/secret/characters.json: permission denied")}
	badServer := &Server{Backend: bad, SecretsPath: filepath.Join(t.TempDir(), "secrets.env")}
	rec = do(badServer.Handler(), "GET", "/api/characters", "", false)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("backend failure: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, leak := range []string{"/srv/secret", "permission denied", "characters.json"} {
		if strings.Contains(body, leak) {
			t.Fatalf("response leaked %q: %s", leak, body)
		}
	}
}

func TestCharactersPageHasAccessibleSection(t *testing.T) {
	h := (&Server{Backend: &fake{}, SecretsPath: filepath.Join(t.TempDir(), "secrets.env")}).Handler()
	body := do(h, "GET", "/", "", false).Body.String()
	for _, want := range []string{
		`data-route="characters"`,
		`data-view="characters"`,
		`id="characterForm"`,
		`id="charRules"`,
		`id="charAddRule"`,
		`aria-labelledby="charRulesLabel"`,
		`data-dirty="characters"`,
		`Голосом: «Алиса, персонаж Имя»`,
		`id="characterNew"`,
		`id="charCancelEdit"`,
		`confirmDialog`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page misses %q", want)
		}
	}
}
