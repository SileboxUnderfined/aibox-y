// Command aibox is a local proxy between the Yandex Station firmware and the
// Alice cloud. Chat commands and open chats are answered by an LLM; every other
// request goes to Yandex unchanged.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"aiboxy/internal/aibox"
	"aiboxy/internal/bridge"
	"aiboxy/internal/chats"
	"aiboxy/internal/providers"
	"aiboxy/internal/web"
)

type Config struct {
	Listen       string                      `json:"listen"`
	Upstream     string                      `json:"upstream"`
	Mode         string                      `json:"mode"` // "observe" passes everything through, "chat" enables chats
	ChatsDir     string                      `json:"chats_dir"`
	Provider     string                      `json:"provider"`
	Providers    map[string]providers.Config `json:"providers"`
	SystemPrompt string                      `json:"system_prompt"`
	IdleMinutes  int                         `json:"idle_minutes"`
	MaxHistory   int                         `json:"max_history"`
	TimeoutSec   int                         `json:"request_timeout_seconds"`
	Web          struct {
		Enabled bool   `json:"enabled"`
		Listen  string `json:"listen"`
	} `json:"web"`
	Progress struct {
		Enabled    bool `json:"enabled"`
		AfterMS    int  `json:"after_ms"`
		IntervalMS int  `json:"interval_ms"`
	} `json:"progress"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{Listen: "127.0.0.1:8443", Upstream: "wss://uniproxy.alice.yandex.net/uni.ws", Mode: "observe", ChatsDir: "chats"}
	cfg.Web.Listen = ":8100"
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if cfg.Mode != "observe" && cfg.Mode != "chat" {
		return nil, errors.New("config: mode must be observe or chat")
	}
	if host, _, err := net.SplitHostPort(cfg.Listen); err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
		return nil, errors.New("config: listen must be a loopback address")
	}
	if cfg.Mode == "chat" || cfg.Provider != "" {
		if _, ok := cfg.Providers[cfg.Provider]; !ok {
			return nil, fmt.Errorf("config: provider %q is not in providers", cfg.Provider)
		}
	}
	if !filepath.IsAbs(cfg.ChatsDir) {
		cfg.ChatsDir = filepath.Join(filepath.Dir(path), cfg.ChatsDir)
	}
	return cfg, nil
}

func main() {
	path := flag.String("config", "config.json", "configuration file")
	check := flag.Bool("check-config", false, "validate configuration and exit")
	ask := flag.String("ask", "", "send one question to the configured provider and exit")
	speakerAction := flag.String("speaker-endpoint", "", "apply or rollback the on-device loopback endpoint (requires writable firmware)")
	speakerConfig := flag.String("speaker-config", "/system/vendor/quasar/quasar.cfg", "firmware configuration path")
	speakerBackup := flag.String("speaker-backup", "/data/opt/aibox/endpoint-backup.json", "persistent endpoint backup")
	speakerURL := flag.String("speaker-url", "ws://127.0.0.1:8443/uni.ws", "on-device proxy URL")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	if *speakerAction != "" {
		if err := setSpeakerEndpoint(*speakerConfig, *speakerBackup, *speakerURL, *speakerAction); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	cfg, err := loadConfig(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *check {
		fmt.Println("config ok, mode", cfg.Mode)
		return
	}
	var resolver *aibox.Resolver
	if cfg.Mode == "chat" || *ask != "" {
		models := map[string]*aibox.Model{}
		for key, pc := range cfg.Providers {
			completer, err := providers.New(pc)
			if err != nil {
				fmt.Fprintf(os.Stderr, "provider %s: %v\n", key, err)
				os.Exit(1)
			}
			models[key] = &aibox.Model{Key: key, Title: pc.Title, Aliases: pc.Aliases, Config: pc, Completer: completer}
		}
		provider := models[cfg.Provider].Completer
		if *ask != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			answer, err := provider.Complete(ctx, aibox.DefaultSystemPrompt, []providers.Message{{Role: "user", Content: *ask}})
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Println(answer)
			return
		}
		store, err := chats.Open(cfg.ChatsDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		resolver = aibox.New(aibox.Options{
			Models:         models,
			DefaultModel:   cfg.Provider,
			StatePath:      filepath.Join(filepath.Dir(*path), "state.json"),
			Store:          store,
			SystemPrompt:   cfg.SystemPrompt,
			Idle:           time.Duration(cfg.IdleMinutes) * time.Minute,
			MaxHistory:     cfg.MaxHistory,
			RequestTimeout: time.Duration(cfg.TimeoutSec) * time.Second,
		})
	}

	var clientFrames, serverFrames, sessions atomic.Int64
	proxy := &bridge.Proxy{Upstream: cfg.Upstream, Observer: func(direction string, _ websocket.MessageType, _ []byte) {
		if direction == "client" {
			clientFrames.Add(1)
		} else {
			serverFrames.Add(1)
		}
	}}
	if resolver != nil {
		proxy.NewSession = func(ctx context.Context, up, down bridge.Sender) bridge.Session {
			sessions.Add(1)
			n := bridge.NewNative(ctx, up, down, resolver)
			n.GateText = false
			n.ShouldListen = true
			if cfg.Progress.Enabled {
				n.ProgressAfter = time.Duration(cfg.Progress.AfterMS) * time.Millisecond
				n.ProgressInterval = time.Duration(cfg.Progress.IntervalMS) * time.Millisecond
			}
			return n
		}
	}
	if resolver != nil && cfg.Web.Enabled {
		dir := filepath.Dir(*path)
		password, err := web.LoadPassword(filepath.Join(dir, "web.password"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "web password:", err)
			os.Exit(1)
		}
		panel := &web.Server{Backend: resolver, SecretsPath: filepath.Join(dir, "secrets.env"), Password: password}
		go func() {
			srv := &http.Server{Addr: cfg.Web.Listen, Handler: panel.Handler(), ReadHeaderTimeout: 10 * time.Second}
			slog.Info("web_panel", "address", cfg.Web.Listen)
			if err := srv.ListenAndServe(); err != nil {
				slog.Error("web panel failed", "error", err)
			}
		}()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		current := cfg.Provider
		if resolver != nil {
			current = resolver.CurrentModel()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "mode": cfg.Mode, "provider": current, "client_frames": clientFrames.Load(), "server_frames": serverFrames.Load(), "sessions": sessions.Load()})
	})
	mux.Handle("/uni.ws", proxy)
	server := &http.Server{Addr: cfg.Listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	slog.Info("listening", "address", cfg.Listen, "mode", cfg.Mode, "provider", cfg.Provider)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("listener failed", "error", err)
		os.Exit(1)
	}
}
