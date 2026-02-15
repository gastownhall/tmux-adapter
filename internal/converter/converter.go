package converter

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gastownhall/tmux-adapter/internal/agents"
	"github.com/gastownhall/tmux-adapter/internal/conv"
	"github.com/gastownhall/tmux-adapter/internal/tmux"
	"github.com/gastownhall/tmux-adapter/internal/wsconv"
)

// Converter is the structured conversation streaming service.
type Converter struct {
	ctrl          *tmux.ControlMode
	registry      *agents.Registry
	watcher       *conv.ConversationWatcher
	wsSrv         *wsconv.Server
	httpSrv       *http.Server
	gtDir         string
	listen        string
	debugServeDir string
}

// New creates a new Converter.
func New(gtDir, listen, debugServeDir string) *Converter {
	return &Converter{
		gtDir:         gtDir,
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

	c.registry = agents.NewRegistry(ctrl, c.gtDir, []string{"converter-monitor"})

	if err := c.registry.Start(); err != nil {
		ctrl.Close()
		return fmt.Errorf("start registry: %w", err)
	}
	log.Printf("converter: agent registry started (%d agents found)", len(c.registry.GetAgents()))

	// Set up conversation watcher with Claude discoverer/parser
	c.watcher = conv.NewConversationWatcher(c.registry, 100000)

	claudeRoot := filepath.Join(os.Getenv("HOME"), ".claude")
	c.watcher.RegisterRuntime("claude",
		conv.NewClaudeDiscoverer(claudeRoot),
		func(agentName, convID string) conv.Parser {
			return conv.NewClaudeParser(agentName, convID)
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
		data, _ := json.Marshal(convs)
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/ws", c.wsSrv.HandleWebSocket)

	if c.debugServeDir != "" {
		log.Printf("converter: serving static files from %s at /", c.debugServeDir)
		mux.Handle("/", http.FileServer(http.Dir(c.debugServeDir)))
	}

	c.httpSrv = &http.Server{
		Addr:    c.listen,
		Handler: mux,
	}

	go func() {
		log.Printf("converter listening on %s", c.listen)
		if err := c.httpSrv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("converter http server: %v", err)
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
