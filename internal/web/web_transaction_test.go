package web

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aiboxy/internal/aibox"
	"aiboxy/internal/chats"
	"aiboxy/internal/providers"
)

func newRealPanel(t *testing.T) (*Server, *aibox.Resolver, string, string) {
	t.Helper()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	initialState := `{"model":"deepseek","system_prompt":"исходный промпт"}`
	if err := os.WriteFile(statePath, []byte(initialState), 0o600); err != nil {
		t.Fatal(err)
	}
	secrets := filepath.Join(dir, "secrets.env")
	initialSecrets := "AIBOX_IO_KEY='old-key-123'\nAIBOX_IO_KEY_2='old-key-456'\nOTHER=1\n"
	if err := os.WriteFile(secrets, []byte(initialSecrets), 0o600); err != nil {
		t.Fatal(err)
	}
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
					Model: "deepseek-chat", APIKeyEnv: "AIBOX_IO_KEY",
				},
			},
			"claude": {
				Key:   "claude",
				Title: "Claude",
				Config: providers.Config{
					Type: "anthropic", Model: "claude-sonnet-4-5", APIKeyEnv: "AIBOX_IO_KEY_2",
				},
			},
		},
		DefaultModel: "deepseek",
		StatePath:    statePath,
		Store:        store,
	})
	return &Server{Backend: r, SecretsPath: secrets}, r, statePath, secrets
}

func blockSecretsTemp(t *testing.T, secrets string) string {
	t.Helper()
	tmp := filepath.Join(filepath.Dir(secrets), ".secrets.tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	return tmp
}

func assertNoTemp(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file left behind: %v", err)
	}
}

func TestSettingsSecretWriteFailureIsAtomic(t *testing.T) {
	t.Setenv("AIBOX_IO_KEY", "old-key-123")
	srv, r, statePath, secrets := newRealPanel(t)
	block := blockSecretsTemp(t, secrets)
	beforeState, _ := os.ReadFile(statePath)
	beforeSecrets, _ := os.ReadFile(secrets)
	beforeProviders := len(r.Settings().Providers)
	beforePrompt := r.Settings().UserPrompt

	rec := do(srv.Handler(), "POST", "/api/settings", `{"user_prompt":"новый промпт","search_on":false,"keys":{"AIBOX_IO_KEY":"sk-new-key-123"}}`, true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}
	afterState, _ := os.ReadFile(statePath)
	if string(afterState) != string(beforeState) {
		t.Fatal("state file changed after a secrets write failure")
	}
	afterSecrets, _ := os.ReadFile(secrets)
	if string(afterSecrets) != string(beforeSecrets) {
		t.Fatalf("secrets changed after a write failure:\n%s", afterSecrets)
	}
	if got := r.Settings(); got.UserPrompt != beforePrompt || len(got.Providers) != beforeProviders || got.Active != "deepseek" {
		t.Fatalf("resolver settings changed: %+v", got)
	}
	if got := os.Getenv("AIBOX_IO_KEY"); got != "old-key-123" {
		t.Fatalf("environment changed: %q", got)
	}
	if !strings.Contains(rec.Body.String(), "ключ не сохранён") {
		t.Fatalf("unexpected error: %s", rec.Body)
	}
	assertNoTemp(t, block)
	if _, err := os.Stat(filepath.Join(filepath.Dir(secrets), ".secrets.recover")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected recovery file: %v", err)
	}
}

func TestProviderSecretWriteFailureIsAtomic(t *testing.T) {
	t.Setenv("AIBOX_IO_KEY", "old-key-123")
	t.Setenv("AIBOX_KEY_API_1", "before-api-key")
	srv, r, statePath, secrets := newRealPanel(t)
	block := blockSecretsTemp(t, secrets)
	beforeState, _ := os.ReadFile(statePath)
	beforeSecrets, _ := os.ReadFile(secrets)
	beforeProviders := len(r.Settings().Providers)

	rec := do(srv.Handler(), "POST", "/api/providers", `{"title":"Groq","type":"openai","base_url":"https://api.groq.com/openai/v1","model":"llama","api_key":"gsk-new-key-123"}`, true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}
	afterState, _ := os.ReadFile(statePath)
	if string(afterState) != string(beforeState) {
		t.Fatal("state file changed after a provider key failure")
	}
	afterSecrets, _ := os.ReadFile(secrets)
	if string(afterSecrets) != string(beforeSecrets) {
		t.Fatalf("secrets changed after a provider key failure:\n%s", afterSecrets)
	}
	after := r.Settings()
	if len(after.Providers) != beforeProviders {
		t.Fatalf("provider was added: %d -> %d", beforeProviders, len(after.Providers))
	}
	for _, p := range after.Providers {
		if p.Key == "api-1" {
			t.Fatal("new provider is present after a key failure")
		}
	}
	if got := os.Getenv("AIBOX_KEY_API_1"); got != "before-api-key" {
		t.Fatalf("provider environment changed: %q", got)
	}
	if !strings.Contains(rec.Body.String(), "ключ не сохранён") {
		t.Fatalf("unexpected error: %s", rec.Body)
	}
	assertNoTemp(t, block)
}

func TestWriteSecretsDataRemovesTempOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets.env")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	srv := &Server{SecretsPath: secrets}
	if err := srv.writeSecrets([]string{"A=1"}); err == nil {
		t.Fatal("rename onto a directory succeeded")
	}
	assertNoTemp(t, filepath.Join(dir, ".secrets.tmp"))
}

type sabotagingBackend struct {
	*fake
	sabotage func()
}

func (b *sabotagingBackend) Apply(u aibox.Update) error {
	b.sabotage()
	return errors.New("backend rejected the update")
}

func TestRollbackFailureKeepsRecoveryCopy(t *testing.T) {
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets.env")
	initial := "AIBOX_TEST_KEY='old-key-123'\nOTHER=1\n"
	if err := os.WriteFile(secrets, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIBOX_TEST_KEY", "old-key-123")
	f := &fake{}
	sabotage := func() {
		_ = os.Remove(secrets)
		_ = os.Mkdir(secrets, 0o700)
	}
	srv := &Server{Backend: &sabotagingBackend{fake: f, sabotage: sabotage}, SecretsPath: secrets}
	rec := do(srv.Handler(), "POST", "/api/settings", `{"user_prompt":"x","keys":{"AIBOX_TEST_KEY":"sk-new-key-123"}}`, true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}
	recovery, err := os.ReadFile(filepath.Join(dir, ".secrets.recover"))
	if err != nil {
		t.Fatalf("recovery copy missing: %v", err)
	}
	if string(recovery) != initial {
		t.Fatalf("recovery copy = %q", recovery)
	}
	assertNoTemp(t, filepath.Join(dir, ".secrets.tmp"))
	if !strings.Contains(rec.Body.String(), "secrets.recover") {
		t.Fatalf("rollback failure not reported: %s", rec.Body)
	}
}

func TestProviderValidationFailureRestoresKeyAndState(t *testing.T) {
	t.Setenv("AIBOX_IO_KEY", "old-key-123")
	t.Setenv("AIBOX_KEY_API_1", "before-api-key")
	srv, r, statePath, secrets := newRealPanel(t)
	beforeState, _ := os.ReadFile(statePath)
	beforeSecrets, _ := os.ReadFile(secrets)
	beforeProviders := len(r.Settings().Providers)

	rec := do(srv.Handler(), "POST", "/api/providers", `{"title":"Groq","type":"openai","base_url":"ftp://bad","model":"llama","api_key":"gsk-new-key-123"}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}
	afterState, _ := os.ReadFile(statePath)
	if string(afterState) != string(beforeState) {
		t.Fatal("state file changed after provider validation failed")
	}
	afterSecrets, _ := os.ReadFile(secrets)
	if string(afterSecrets) != string(beforeSecrets) {
		t.Fatalf("secrets were not restored:\n%s", afterSecrets)
	}
	if got := os.Getenv("AIBOX_KEY_API_1"); got != "before-api-key" {
		t.Fatalf("provider environment was not restored: %q", got)
	}
	if got := len(r.Settings().Providers); got != beforeProviders {
		t.Fatalf("provider count changed: %d -> %d", beforeProviders, got)
	}
}

func TestConcurrentSettingsSavesKeepBothKeys(t *testing.T) {
	t.Setenv("AIBOX_IO_KEY", "old-key-123")
	t.Setenv("AIBOX_IO_KEY_2", "old-key-456")
	srv, _, _, secrets := newRealPanel(t)
	h := srv.Handler()
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for _, body := range []string{
		`{"user_prompt":"первый","keys":{"AIBOX_IO_KEY":"sk-first-key-123"}}`,
		`{"user_prompt":"второй","keys":{"AIBOX_IO_KEY_2":"sk-second-key-456"}}`,
	} {
		wg.Add(1)
		go func(body string) {
			defer wg.Done()
			results <- do(h, "POST", "/api/settings", body, true).Code
		}(body)
	}
	wg.Wait()
	close(results)
	for code := range results {
		if code != http.StatusOK {
			t.Fatalf("concurrent save returned %d", code)
		}
	}
	data, err := os.ReadFile(secrets)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"AIBOX_IO_KEY='sk-first-key-123'", "AIBOX_IO_KEY_2='sk-second-key-456'", "OTHER=1"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("secrets lost %q:\n%s", want, data)
		}
	}
	if os.Getenv("AIBOX_IO_KEY") != "sk-first-key-123" || os.Getenv("AIBOX_IO_KEY_2") != "sk-second-key-456" {
		t.Fatal("process environment lost a concurrent key update")
	}
	assertNoTemp(t, filepath.Join(filepath.Dir(secrets), ".secrets.tmp"))
}

func TestProviderDeleteSecretFailureIsAtomic(t *testing.T) {
	t.Setenv("AIBOX_IO_KEY", "old-key-123")
	t.Setenv("AIBOX_KEY_API_1", "")
	srv, r, statePath, secrets := newRealPanel(t)
	rec := do(srv.Handler(), "POST", "/api/providers", `{"title":"Groq","type":"openai","base_url":"https://api.groq.com/openai/v1","model":"llama","api_key":"gsk-delete-key-123"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup provider: %d %s", rec.Code, rec.Body)
	}
	block := blockSecretsTemp(t, secrets)
	beforeState, _ := os.ReadFile(statePath)
	beforeSecrets, _ := os.ReadFile(secrets)
	beforeEnv := os.Getenv("AIBOX_KEY_API_1")
	beforeProviders := len(r.Settings().Providers)

	rec = do(srv.Handler(), "DELETE", "/api/providers/api-1", "", true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}
	afterState, _ := os.ReadFile(statePath)
	if string(afterState) != string(beforeState) {
		t.Fatal("state changed after a delete key failure")
	}
	afterSecrets, _ := os.ReadFile(secrets)
	if string(afterSecrets) != string(beforeSecrets) {
		t.Fatalf("secrets changed after a delete key failure:\n%s", afterSecrets)
	}
	if got := os.Getenv("AIBOX_KEY_API_1"); got != beforeEnv {
		t.Fatalf("environment changed after a delete key failure: %q", got)
	}
	if got := r.Settings(); len(got.Providers) != beforeProviders {
		t.Fatalf("provider count changed: %d -> %d", beforeProviders, len(got.Providers))
	}
	found := false
	for _, p := range r.Settings().Providers {
		if p.Key == "api-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("provider disappeared after a delete key failure")
	}
	assertNoTemp(t, block)
}
