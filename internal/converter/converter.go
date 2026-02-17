package converter

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gastownhall/tmux-adapter/internal/agents"
	"github.com/gastownhall/tmux-adapter/internal/conv"
	"github.com/gastownhall/tmux-adapter/internal/tmux"
	"github.com/gastownhall/tmux-adapter/internal/wsbase"
	"github.com/gastownhall/tmux-adapter/internal/wsconv"
	"github.com/gastownhall/tmux-adapter/web"
)

// Converter is the structured conversation streaming service.
type Converter struct {
	ctrl          *tmux.ControlMode
	registry      *agents.Registry
	watcher       *conv.ConversationWatcher
	wsSrv         *wsconv.Server
	httpSrv       *http.Server
	workDirFilter string
	listen        string
	debugServeDir string
}

// New creates a new Converter.
func New(workDirFilter, listen, debugServeDir string) *Converter {
	return &Converter{
		workDirFilter: workDirFilter,
		listen:        listen,
		debugServeDir: debugServeDir,
	}
}

// Start initializes all components and starts the HTTP server.
func (c *Converter) Start() error {
	ctrl, err := tmux.NewControlMode("converter-monitor")
	if err != nil {
		return fmt.Errorf("tmux control mode: %w", err)
	}
	c.ctrl = ctrl
	log.Println("converter: connected to tmux control mode")

	c.registry = agents.NewRegistry(ctrl, c.workDirFilter, []string{"converter-monitor"})

	if err := c.registry.Start(); err != nil {
		ctrl.Close()
		return fmt.Errorf("start registry: %w", err)
	}
	log.Printf("converter: agent registry started (%d agents found)", len(c.registry.GetAgents()))

	// Set up conversation watcher with runtime-specific discoverers/parsers
	c.watcher = conv.NewConversationWatcher(c.registry, 100000)

	claudeRoot := filepath.Join(os.Getenv("HOME"), ".claude")
	c.watcher.RegisterRuntime("claude",
		conv.NewClaudeDiscoverer(claudeRoot),
		func(agentName, convID string) conv.Parser {
			return conv.NewClaudeParser(agentName, convID)
		},
	)
	codexRoot := filepath.Join(os.Getenv("HOME"), ".codex", "sessions")
	c.watcher.RegisterRuntime("codex",
		conv.NewCodexDiscoverer(codexRoot),
		func(agentName, convID string) conv.Parser {
			return conv.NewCodexParser(agentName, convID)
		},
	)

	c.watcher.Start()
	log.Println("converter: conversation watcher started")

	// Set up WebSocket server
	c.wsSrv = wsconv.NewServer(c.watcher, "", []string{"*"}, c.ctrl, c.registry)

	// Forward watcher events to WebSocket broadcast
	go func() {
		for event := range c.watcher.Events() {
			c.wsSrv.Broadcast(event)
		}
	}()

	// Set up HTTP endpoints
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/conversations", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		convs := c.watcher.ListConversations()
		data, err := json.Marshal(convs)
		if err != nil {
			http.Error(w, fmt.Sprintf("marshal error: %v", err), http.StatusInternalServerError)
			return
		}
		if _, err := w.Write(data); err != nil {
			log.Printf("converter: failed to write response: %v", err)
		}
	})
	mux.HandleFunc("/ws", c.wsSrv.HandleWebSocket)

	// Serve embedded converter web component files at /tmux-converter-web/
	converterFS, err := fs.Sub(web.Files, "tmux-converter-web")
	if err != nil {
		return fmt.Errorf("embedded converter assets: %w", err)
	}
	mux.Handle("/tmux-converter-web/", wsbase.CorsHandler(
		http.StripPrefix("/tmux-converter-web/", http.FileServer(http.FS(converterFS))),
	))

	// Serve shared dashboard files at /shared/
	sharedFS, err := fs.Sub(web.Files, "shared")
	if err != nil {
		return fmt.Errorf("embedded shared assets: %w", err)
	}
	mux.Handle("/shared/", wsbase.CorsHandler(
		http.StripPrefix("/shared/", http.FileServer(http.FS(sharedFS))),
	))

	if c.debugServeDir != "" {
		log.Printf("converter: serving static files from %s at /", c.debugServeDir)
		mux.Handle("/", http.FileServer(http.Dir(c.debugServeDir)))
	}

	ln, err := net.Listen("tcp", c.listen)
	if err != nil {
		return fmt.Errorf("converter listen: %w", err)
	}
	log.Printf("converter listening on %s", c.listen)

	c.httpSrv = &http.Server{
		Handler: mux,
	}

	go func() {
		if err := c.httpSrv.Serve(ln); err != http.ErrServerClosed {
			log.Printf("converter http server error: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the converter.
func (c *Converter) Stop() {
	log.Println("converter: shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.httpSrv.Shutdown(ctx); err != nil {
		log.Printf("converter http shutdown: %v", err)
	}

	c.watcher.Stop()
	c.registry.Stop()
	c.ctrl.Close()

	log.Println("converter: shutdown complete")
}
