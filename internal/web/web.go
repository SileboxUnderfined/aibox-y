// Package web serves a small settings page for AIBox inside the same process.
package web

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"aiboxy/internal/aibox"
	"aiboxy/internal/chats"
)

//go:embed index.html
var page []byte

type Backend interface {
	Settings() aibox.Settings
	Apply(aibox.Update) error
	ListModels(ctx context.Context, provider string) ([]string, error)
	SaveProvider(aibox.CustomProvider) (aibox.CustomProvider, error)
	RemoveProvider(key string) (string, error)
	AddFact(text string) error
	EditFact(id, text string) error
	DeleteFact(id string) error
	ClearFacts() error
	Status() aibox.Status
	Chats() ([]aibox.ChatSummary, error)
	Chat(id string) (*chats.Chat, error)
	DeleteChat(id string) error
}

type Server struct {
	Backend     Backend
	SecretsPath string
	// mut serializes requests that change settings or secrets, so two
	// requests cannot interleave their key writes and rollbacks.
	mut sync.Mutex
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.Backend.Settings())
	})
	mux.HandleFunc("POST /api/settings", s.save)
	mux.HandleFunc("POST /api/providers", s.saveProvider)
	mux.HandleFunc("DELETE /api/providers/{key}", s.removeProvider)
	mux.HandleFunc("POST /api/memory", func(w http.ResponseWriter, r *http.Request) {
		s.fact(w, r, func(text string) error { return s.Backend.AddFact(text) })
	})
	mux.HandleFunc("PUT /api/memory/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.fact(w, r, func(text string) error { return s.Backend.EditFact(r.PathValue("id"), text) })
	})
	mux.HandleFunc("DELETE /api/memory/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.reply(w, s.Backend.DeleteFact(r.PathValue("id")))
	})
	mux.HandleFunc("DELETE /api/memory", func(w http.ResponseWriter, _ *http.Request) {
		s.reply(w, s.Backend.ClearFacts())
	})
	mux.HandleFunc("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		ids, err := s.Backend.ListModels(ctx, r.URL.Query().Get("provider"))
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, ids)
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.Backend.Status())
	})
	mux.HandleFunc("GET /api/chats", func(w http.ResponseWriter, _ *http.Request) {
		list, err := s.Backend.Chats()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось прочитать чаты"})
			return
		}
		writeJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("GET /api/chats/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !chatID.MatchString(id) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "чат не найден"})
			return
		}
		c, err := s.Backend.Chat(id)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, c)
	})
	mux.HandleFunc("DELETE /api/chats/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !chatID.MatchString(id) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "чат не найден"})
			return
		}
		if err := s.Backend.DeleteChat(id); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	return s.guard(mux)
}

// chatID matches exactly the ids chats.NewID creates, so a path value can
// never escape the chats directory.
var chatID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// guard adds browser security headers and refuses requests that a page on
// another site could use to read or change the panel. The panel has no
// password: access is bounded by the local network and by checks that browser
// scripts from other origins cannot bypass.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		if !sameOrigin(r) || crossSite(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// Settings change state: refuse requests from other sites' pages.
		if r.Method != http.MethodGet && r.Header.Get("X-AIBox") != "1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin accepts requests without Origin, as curl and some same-origin
// navigations send none. When Origin is present, its scheme, host and port
// must match this listener exactly. The scheme comes from r.TLS, never from a
// forwarded header, so a TLS-terminating proxy that rewrites the scheme is
// not trusted to widen the check. Matching headers cannot stop DNS rebinding:
// a malicious hostname that resolves to this panel can send both Origin and
// Host with its own name, so keep the panel on a trusted LAN.
func sameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if !strings.EqualFold(u.Scheme, scheme) {
		return false
	}
	request, err := url.Parse("//" + r.Host)
	if err != nil || request.Hostname() == "" {
		return false
	}
	if !strings.EqualFold(u.Hostname(), request.Hostname()) {
		return false
	}
	return originPort(u, scheme) == originPort(request, scheme)
}

// originPort returns the explicit port or the scheme default. Comparing the
// effective port makes "http://host" and "http://host:80" the same origin
// without accepting a different port.
func originPort(u *url.URL, scheme string) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(scheme, "https") {
		return "443"
	}
	return "80"
}

// crossSite uses Fetch Metadata when the browser sends it. A missing header is
// compatible with non-browser clients; a same-site request still has to pass
// the Origin check above.
func crossSite(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site")
}

type saveRequest struct {
	aibox.Update
	Keys map[string]string `json:"keys,omitempty"` // env name -> new key
}

func (s *Server) save(w http.ResponseWriter, r *http.Request) {
	s.mut.Lock()
	defer s.mut.Unlock()
	var req saveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if err := s.validateKeys(req.Keys); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var snap secretsSnapshot
	envs := mapKeys(req.Keys)
	if len(envs) > 0 {
		var err error
		snap, err = s.snapshotSecrets(envs)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось прочитать secrets.env: " + err.Error()})
			return
		}
		// Write keys first: if this fails, nothing in the resolver has changed.
		if err := s.saveKeys(req.Keys); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "ключ не сохранён: " + err.Error()})
			return
		}
	}
	if err := s.Backend.Apply(req.Update); err != nil {
		if len(envs) > 0 {
			if rbErr := s.rollbackSecrets(snap, envs); rbErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "настройки не применены, но откат ключей не удался: " + rbErr.Error()})
				return
			}
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.Backend.Settings())
}

type providerRequest struct {
	aibox.CustomProvider
	APIKey string `json:"api_key,omitempty"`
}

func (s *Server) saveProvider(w http.ResponseWriter, r *http.Request) {
	s.mut.Lock()
	defer s.mut.Unlock()
	var req providerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	key := strings.TrimSpace(req.APIKey)
	if key != "" && !keyValue.MatchString(key) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ключ содержит недопустимые символы"})
		return
	}
	settings := s.Backend.Settings()
	editing := strings.TrimSpace(req.Key) != ""
	expectedKey := ""
	env := ""
	if editing {
		p, ok := providerByKey(settings, req.Key)
		if !ok || !p.Custom {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "встроенного провайдера здесь не изменить"})
			return
		}
		env = p.KeyEnv
	} else {
		expectedKey = nextCustomKey(settings)
		env = (aibox.CustomProvider{Key: expectedKey}).KeyEnv()
	}
	var snap secretsSnapshot
	envs := []string{}
	if key != "" {
		envs = []string{env}
		if err := s.validateKeys(map[string]string{env: key}, env); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		var err error
		snap, err = s.snapshotSecrets(envs)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось прочитать secrets.env: " + err.Error()})
			return
		}
		if err := s.saveKeys(map[string]string{env: key}, env); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "ключ не сохранён, провайдер не изменён: " + err.Error()})
			return
		}
	}
	saved, err := s.Backend.SaveProvider(req.CustomProvider)
	if err != nil {
		if len(envs) > 0 {
			if rbErr := s.rollbackSecrets(snap, envs); rbErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "провайдер не сохранён, но откат ключей не удался: " + rbErr.Error()})
				return
			}
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !editing && saved.Key != expectedKey {
		// The resolver should allocate the key we reserved; if it did not,
		// undo both sides rather than leave a provider with a mismatched key.
		if len(envs) > 0 {
			if rbErr := s.rollbackSecrets(snap, envs); rbErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "провайдер сохранён с другим ключом, откат ключей не удался: " + rbErr.Error()})
				return
			}
		}
		if _, rerr := s.Backend.RemoveProvider(saved.Key); rerr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось согласовать ключ нового провайдера: " + rerr.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось согласовать ключ нового провайдера"})
		return
	}
	writeJSON(w, http.StatusOK, s.Backend.Settings())
}

func (s *Server) removeProvider(w http.ResponseWriter, r *http.Request) {
	s.mut.Lock()
	defer s.mut.Unlock()
	key := r.PathValue("key")
	p, ok := providerByKey(s.Backend.Settings(), key)
	if !ok || !p.Custom {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "можно удалить только добавленный провайдер"})
		return
	}
	env := p.KeyEnv
	snap, err := s.snapshotSecrets([]string{env})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось прочитать secrets.env: " + err.Error()})
		return
	}
	// Remove the secret first; a failure here leaves the provider untouched.
	if err := s.dropKey(env); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось удалить ключ, провайдер не изменён: " + err.Error()})
		return
	}
	removedEnv, err := s.Backend.RemoveProvider(key)
	if err != nil {
		if rbErr := s.rollbackSecrets(snap, []string{env}); rbErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "провайдер не удалён, но откат ключей не удался: " + rbErr.Error()})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if removedEnv != "" && removedEnv != env {
		if rbErr := s.rollbackSecrets(snap, []string{env}); rbErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "ключ провайдера не совпал, откат не удался: " + rbErr.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "ключ провайдера не совпал с настройками"})
		return
	}
	writeJSON(w, http.StatusOK, s.Backend.Settings())
}

func providerByKey(s aibox.Settings, key string) (aibox.ProviderInfo, bool) {
	for _, p := range s.Providers {
		if p.Key == key {
			return p, true
		}
	}
	return aibox.ProviderInfo{}, false
}

// nextCustomKey mirrors Resolver.SaveProvider's allocation so a new
// provider's key can be stored before the provider itself is committed.
func nextCustomKey(s aibox.Settings) string {
	taken := map[string]bool{}
	for _, p := range s.Providers {
		taken[p.Key] = true
	}
	for n := 1; ; n++ {
		key := "api-" + strconv.Itoa(n)
		if !taken[key] {
			return key
		}
	}
}

func mapKeys(keys map[string]string) []string {
	out := make([]string, 0, len(keys))
	for env, value := range keys {
		if strings.TrimSpace(value) != "" {
			out = append(out, env)
		}
	}
	return out
}

type secretsSnapshot struct {
	data   []byte
	exists bool
	env    map[string]string
}

func (s *Server) snapshotSecrets(envs []string) (secretsSnapshot, error) {
	snap := secretsSnapshot{env: map[string]string{}}
	data, err := os.ReadFile(s.SecretsPath)
	switch {
	case err == nil:
		snap.data, snap.exists = data, true
	case errors.Is(err, os.ErrNotExist):
	default:
		return snap, err
	}
	for _, env := range envs {
		snap.env[env] = os.Getenv(env)
	}
	return snap, nil
}

// restoreSecrets puts the old file and the old process environment back.
func (s *Server) restoreSecrets(snap secretsSnapshot, envs []string) error {
	if snap.exists {
		if err := s.writeSecretsData(snap.data); err != nil {
			return err
		}
	} else if err := os.Remove(s.SecretsPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, env := range envs {
		if value := snap.env[env]; value != "" {
			if err := os.Setenv(env, value); err != nil {
				return err
			}
		} else {
			_ = os.Unsetenv(env)
		}
	}
	return nil
}

// rollbackSecrets restores the snapshot. If even that fails, it keeps a
// private recovery copy so old keys are not lost, and reports both errors.
func (s *Server) rollbackSecrets(snap secretsSnapshot, envs []string) error {
	if err := s.restoreSecrets(snap, envs); err != nil {
		if !snap.exists {
			return errors.New("не удалось восстановить secrets.env: " + err.Error())
		}
		recovery := filepath.Join(filepath.Dir(s.SecretsPath), ".secrets.recover")
		if werr := os.WriteFile(recovery, snap.data, 0o600); werr != nil {
			return errors.New("не удалось восстановить secrets.env: " + err.Error() + "; резервная копия не создана: " + werr.Error())
		}
		return errors.New("не удалось восстановить secrets.env: " + err.Error() + "; старые ключи сохранены в .secrets.recover")
	}
	return nil
}

func (s *Server) fact(w http.ResponseWriter, r *http.Request, change func(text string) error) {
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.reply(w, change(req.Text))
}

// reply answers a change with the new settings, or with its error.
func (s *Server) reply(w http.ResponseWriter, err error) {
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.Backend.Settings())
}

// dropKey removes a deleted provider's key from secrets.env and the process.
// The file is replaced first: if that fails, neither the file nor the process
// environment has changed.
func (s *Server) dropKey(env string) error {
	data, err := os.ReadFile(s.SecretsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_ = os.Unsetenv(env)
			return nil
		}
		return err
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), env+"=") {
			kept = append(kept, line)
		}
	}
	if err := s.writeSecrets(kept); err != nil {
		return err
	}
	_ = os.Unsetenv(env)
	return nil
}

func (s *Server) writeSecrets(lines []string) error {
	return s.writeSecretsData([]byte(strings.Join(lines, "\n") + "\n"))
}

// writeSecretsData replaces secrets.env atomically and always removes its
// temporary file, including when the write or rename fails.
func (s *Server) writeSecretsData(data []byte) error {
	tmp := filepath.Join(filepath.Dir(s.SecretsPath), ".secrets.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, s.SecretsPath); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	return nil
}

// A key ends up in a file the start script sources with sh, so only plain
// token characters are accepted.
var keyValue = regexp.MustCompile(`^[A-Za-z0-9._:\-]{8,512}$`)

// validateKeys checks names and values against the current providers without
// writing anything; the caller can then reject bad input before Apply.
func (s *Server) validateKeys(keys map[string]string, extra ...string) error {
	if len(keys) == 0 {
		return nil
	}
	settings := s.Backend.Settings()
	allowed := map[string]bool{settings.SearchKeyEnv: true}
	for _, p := range settings.Providers {
		allowed[p.KeyEnv] = true
	}
	for _, env := range extra {
		allowed[env] = true
	}
	for env, value := range keys {
		if !allowed[env] || env == "" {
			return errors.New("unknown key name")
		}
		if value = strings.TrimSpace(value); value != "" && !keyValue.MatchString(value) {
			return errors.New("ключ содержит недопустимые символы")
		}
	}
	return nil
}

func (s *Server) saveKeys(keys map[string]string, extra ...string) error {
	if err := s.validateKeys(keys, extra...); err != nil {
		return err
	}
	var lines []string
	if data, err := os.ReadFile(s.SecretsPath); err == nil {
		lines = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
	for env, value := range keys {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		entry := env + "='" + value + "'"
		replaced := false
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), env+"=") {
				lines[i], replaced = entry, true
			}
		}
		if !replaced {
			lines = append(lines, entry)
		}
	}
	if err := s.writeSecrets(lines); err != nil {
		return err
	}
	for env, value := range keys {
		if value = strings.TrimSpace(value); value != "" {
			_ = os.Setenv(env, value)
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
