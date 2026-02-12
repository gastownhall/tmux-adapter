package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gastownhall/tmux-adapter/internal/agents"
	"github.com/gastownhall/tmux-adapter/internal/tmux"
	"github.com/gastownhall/tmux-adapter/internal/ws"
)

// Adapter wires together tmux control mode, agent registry, pipe-pane streaming,
// and the WebSocket server.
type Adapter struct {
	ctrl      *tmux.ControlMode
	registry  *agents.Registry
	pipeMgr   *tmux.PipePaneManager
	wsSrv     *ws.Server
	httpSrv   *http.Server
	gtDir     string
	port      int
	authToken string
}

// New creates a new Adapter.
func New(gtDir string, port int, authToken string) *Adapter {
	return &Adapter{
		gtDir:     gtDir,
		port:      port,
		authToken: authToken,
	}
}

// Start initializes all components and starts the HTTP/WebSocket server.
func (a *Adapter) Start() error {
	// 1. Connect to tmux in control mode
	ctrl, err := tmux.NewControlMode()
	if err != nil {
		return fmt.Errorf("tmux control mode: %w", err)
	}
	a.ctrl = ctrl
	log.Println("connected to tmux control mode")

	// 2. Create agent registry
	a.registry = agents.NewRegistry(ctrl, a.gtDir)

	// 3. Create pipe-pane manager
	a.pipeMgr = tmux.NewPipePaneManager(ctrl)

	// 4. Create WebSocket server
	a.wsSrv = ws.NewServer(a.registry, a.pipeMgr, ctrl, a.authToken)

	// 5. Start registry watching
	if err := a.registry.Start(); err != nil {
		ctrl.Close()
		return fmt.Errorf("start registry: %w", err)
	}
	log.Printf("agent registry started (%d agents found)", len(a.registry.GetAgents()))

	// 6. Forward registry events to WebSocket clients
	go a.forwardEvents()

	// 7. Start HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.handleHealth)
	mux.HandleFunc("/readyz", a.handleReady)
	mux.Handle("/ws", a.wsSrv)
	mux.Handle("/", http.FileServer(http.Dir("web")))

	a.httpSrv = &http.Server{
		Addr:    fmt.Sprintf(":%d", a.port),
		Handler: mux,
	}

	go func() {
		log.Printf("Dashboard at http://localhost:%d/", a.port)
		log.Printf("WebSocket server listening on ws://localhost:%d/ws", a.port)
		log.Printf("watching gastown at %s", a.gtDir)
		if err := a.httpSrv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down all components.
func (a *Adapter) Stop() {
	log.Println("shutting down...")

	// 1. Shutdown HTTP server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.httpSrv.Shutdown(ctx); err != nil {
		log.Printf("http shutdown: %v", err)
	}

	// 2. Close all WebSocket connections
	a.wsSrv.CloseAll()

	// 3. Stop registry
	a.registry.Stop()

	// 4. Stop all pipe-panes
	a.pipeMgr.StopAll()

	// 5. Close control mode (kills monitor session)
	a.ctrl.Close()

	log.Println("shutdown complete")
}

// forwardEvents reads agent lifecycle events from the registry and pushes them to
// subscribed WebSocket clients.
func (a *Adapter) forwardEvents() {
	for event := range a.registry.Events() {
		msg := ws.MakeAgentEvent(event.Type, event.Agent)
		a.wsSrv.BroadcastToAgentSubscribers(msg)
	}
}

func (a *Adapter) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *Adapter) handleReady(w http.ResponseWriter, _ *http.Request) {
	if a.ctrl == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":    false,
			"error": "tmux control mode not initialized",
		})
		return
	}
	if _, err := a.ctrl.ListSessions(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":    false,
			"error": "tmux control mode unavailable: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, status int, payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		log.Printf("write json response: %v", err)
	}
}
