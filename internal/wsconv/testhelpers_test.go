package wsconv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"github.com/gastownhall/tmux-adapter/internal/agents"
	"github.com/gastownhall/tmux-adapter/internal/conv"
	"github.com/gastownhall/tmux-adapter/internal/tmux"
)

// testControl implements agents.ControlModeInterface for testing.
type testControl struct {
	sessions []tmux.SessionInfo
	panes    map[string]tmux.PaneInfo
	notifCh  chan tmux.Notification
}

func (tc *testControl) ListSessions() ([]tmux.SessionInfo, error) {
	return tc.sessions, nil
}

func (tc *testControl) GetPaneInfo(session string) (tmux.PaneInfo, error) {
	return tc.panes[session], nil
}

func (tc *testControl) Notifications() <-chan tmux.Notification {
	return tc.notifCh
}

// setupTestServer creates a Server backed by real Registry + ConversationWatcher
// with the given agent names. Each agent is a "claude" runtime with node process.
func setupTestServer(t *testing.T, agentNames ...string) (*Server, *httptest.Server) {
	t.Helper()

	ctrl := &testControl{
		panes:   make(map[string]tmux.PaneInfo),
		notifCh: make(chan tmux.Notification, 10),
	}

	for _, name := range agentNames {
		ctrl.sessions = append(ctrl.sessions, tmux.SessionInfo{Name: name, Attached: true})
		ctrl.panes[name] = tmux.PaneInfo{Command: "node", WorkDir: "/tmp/test"}
	}

	registry := agents.NewRegistry(ctrl, "", nil)
	if err := registry.Start(); err != nil {
		t.Fatalf("registry.Start: %v", err)
	}
	t.Cleanup(registry.Stop)

	// Drain registry events so the channel doesn't fill up
	for range agentNames {
		select {
		case <-registry.Events():
		case <-time.After(time.Second):
			t.Fatal("timeout draining registry events")
		}
	}

	watcher := conv.NewConversationWatcher(registry, 1000)
	watcher.Start()
	t.Cleanup(watcher.Stop)

	server := NewServer(watcher, "", []string{"*"}, nil, registry)

	ts := httptest.NewServer(http.HandlerFunc(server.HandleWebSocket))
	t.Cleanup(ts.Close)

	return server, ts
}

// testWSClient wraps a WebSocket connection for test assertions.
type testWSClient struct {
	conn *websocket.Conn
	ctx  context.Context
}

// dialTestServer connects a WebSocket client to the test server.
func dialTestServer(t *testing.T, ts *httptest.Server) *testWSClient {
	t.Helper()
	ctx := context.Background()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return &testWSClient{conn: conn, ctx: ctx}
}

// send marshals and sends a JSON message.
func (tc *testWSClient) send(t *testing.T, msg any) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := tc.conn.Write(tc.ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// recv reads and unmarshals a JSON response within a timeout.
func (tc *testWSClient) recv(t *testing.T) serverMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(tc.ctx, 5*time.Second)
	defer cancel()
	_, data, err := tc.conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msg serverMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal %q: %v", string(data), err)
	}
	return msg
}

// drainSnapshot consumes conversation-snapshot-chunk messages until
// conversation-snapshot-end, returning all accumulated events.
func (tc *testWSClient) drainSnapshot(t *testing.T) []conv.ConversationEvent {
	t.Helper()
	var events []conv.ConversationEvent
	for {
		msg := tc.recv(t)
		switch msg.Type {
		case "conversation-snapshot-chunk":
			events = append(events, msg.Events...)
		case "conversation-snapshot-end":
			return events
		default:
			t.Fatalf("drainSnapshot: unexpected message type %q", msg.Type)
		}
	}
}

// recvAfterSnapshot consumes any pending snapshot chunks + end messages,
// then returns the first non-snapshot message.
func (tc *testWSClient) recvAfterSnapshot(t *testing.T) serverMessage {
	t.Helper()
	for {
		msg := tc.recv(t)
		switch msg.Type {
		case "conversation-snapshot-chunk", "conversation-snapshot-end":
			continue
		default:
			return msg
		}
	}
}
