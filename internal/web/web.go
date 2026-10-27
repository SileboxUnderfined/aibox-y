// Package web serves a small settings page for AIBox inside the same process.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"aiboxy/internal/aibox"
)

//go:embed index.html
var page []byte

type Backend interface {
	Settings() aibox.Settings
	Apply(aibox.Update) error
	ListModels(ctx context.Context, provider string) ([]string, error)
	SaveProvider(aibox.CustomProvider) (aibox.CustomProvider, error)
	RemoveProvider(key string) (string, error)
}

type Server struct {
	Backend     Backend
	SecretsPath string
	Password    string
}

// LoadPassword reads the panel password, creating a random one on first run.
func LoadPassword(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		if p := strings.TrimSpace(string(data)); p != "" {
			return p, nil
		}
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	p := hex.EncodeToString(b)
	return p, os.WriteFile(path, []byte(p+"\n"), 0o600)
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
	mux.HandleFunc("DELETE /api/providers/{key}", func(w http.ResponseWriter, r *http.Request) {
		env, err := s.Backend.RemoveProvider(r.PathValue("key"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := s.dropKey(env); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось удалить ключ"})
			return
		}
		writeJSON(w, http.StatusOK, s.Backend.Settings())
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
	return s.auth(mux)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(s.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="AIBox", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
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

type saveRequest struct {
	aibox.Update
	Keys map[string]string `json:"keys,omitempty"` // env name -> new key
}

func (s *Server) save(w http.ResponseWriter, r *http.Request) {
	var req saveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if err := s.saveKeys(req.Keys); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.Backend.Apply(req.Update); err != nil {
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
	saved, err := s.Backend.SaveProvider(req.CustomProvider)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.saveKeys(map[string]string{saved.KeyEnv(): key}); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.Backend.Settings())
}

// dropKey removes a deleted provider's key from secrets.env and the process.
func (s *Server) dropKey(env string) error {
	_ = os.Unsetenv(env)
	data, err := os.ReadFile(s.SecretsPath)
	if err != nil {
		return nil
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), env+"=") {
			kept = append(kept, line)
		}
	}
	return s.writeSecrets(kept)
}

func (s *Server) writeSecrets(lines []string) error {
	tmp := filepath.Join(filepath.Dir(s.SecretsPath), ".secrets.tmp")
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.SecretsPath)
}

// A key ends up in a file the start script sources with sh, so only plain
// token characters are accepted.
var keyValue = regexp.MustCompile(`^[A-Za-z0-9._:\-]{8,512}$`)

func (s *Server) saveKeys(keys map[string]string) error {
	if len(keys) == 0 {
		return nil
	}
	settings := s.Backend.Settings()
	allowed := map[string]bool{settings.SearchKeyEnv: true}
	for _, p := range settings.Providers {
		allowed[p.KeyEnv] = true
	}
	for env, value := range keys {
		if !allowed[env] || env == "" {
			return errors.New("unknown key name")
		}
		if value = strings.TrimSpace(value); value != "" && !keyValue.MatchString(value) {
			return errors.New("ключ содержит недопустимые символы")
		}
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
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
