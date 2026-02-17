package conv

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func writeCodexSessionFile(t *testing.T, path, id, cwd string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	line := `{"timestamp":"2026-02-17T03:36:29.625Z","type":"session_meta","payload":{"id":"` + id + `","cwd":"` + cwd + `"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestNewCodexDiscoverer(t *testing.T) {
	disc := NewCodexDiscoverer("/custom/codex/sessions")
	if disc.Root != "/custom/codex/sessions" {
		t.Fatalf("Root = %q, want /custom/codex/sessions", disc.Root)
	}

	disc2 := NewCodexDiscoverer("")
	want := filepath.Join(os.Getenv("HOME"), ".codex", "sessions")
	if disc2.Root != want {
		t.Fatalf("Root = %q, want %q", disc2.Root, want)
	}
}

func TestCodexDiscovererFindConversationsByWorkDir(t *testing.T) {
	root := t.TempDir()
	workDir := "/Users/csells/Code/gastownhall/tmux-adapter"

	oldPath := filepath.Join(root, "2026", "02", "15", "rollout-old.jsonl")
	newPath := filepath.Join(root, "2026", "02", "16", "rollout-new.jsonl")
	otherPath := filepath.Join(root, "2026", "02", "16", "rollout-other.jsonl")

	writeCodexSessionFile(t, oldPath, "sess-old", workDir)
	writeCodexSessionFile(t, newPath, "sess-new", workDir)
	writeCodexSessionFile(t, otherPath, "sess-other", "/tmp/other")

	oldTime := mustParseTime(t, "2026-02-15T00:00:00Z")
	newTime := mustParseTime(t, "2026-02-16T00:00:00Z")
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	disc := NewCodexDiscoverer(root)
	result, err := disc.FindConversations("agent-313", workDir)
	if err != nil {
		t.Fatalf("FindConversations() error = %v", err)
	}

	if len(result.Files) != 2 {
		t.Fatalf("len(Files) = %d, want 2", len(result.Files))
	}
	if result.Files[0].NativeConversationID != "sess-new" {
		t.Fatalf("first file NativeConversationID = %q, want sess-new", result.Files[0].NativeConversationID)
	}
	if result.Files[0].ConversationID != "codex:agent-313:sess-new" {
		t.Fatalf("ConversationID = %q, want codex:agent-313:sess-new", result.Files[0].ConversationID)
	}
	if result.Files[1].NativeConversationID != "sess-old" {
		t.Fatalf("second file NativeConversationID = %q, want sess-old", result.Files[1].NativeConversationID)
	}
	if result.Files[0].Runtime != "codex" || result.Files[1].Runtime != "codex" {
		t.Fatalf("Runtime(s) = %q / %q, want codex", result.Files[0].Runtime, result.Files[1].Runtime)
	}

	newDir := filepath.Dir(newPath)
	oldDir := filepath.Dir(oldPath)
	if !slices.Contains(result.WatchDirs, newDir) || !slices.Contains(result.WatchDirs, oldDir) {
		t.Fatalf("WatchDirs = %v, want to contain %q and %q", result.WatchDirs, newDir, oldDir)
	}
}

func TestCodexDiscovererMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	disc := NewCodexDiscoverer(root)

	result, err := disc.FindConversations("agent-313", "/tmp/work")
	if err != nil {
		t.Fatalf("FindConversations() error = %v", err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("len(Files) = %d, want 0", len(result.Files))
	}
	if len(result.WatchDirs) == 0 {
		t.Fatal("WatchDirs should not be empty")
	}
}

func TestReadCodexSessionMeta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeCodexSessionFile(t, path, "sess-abc", "/tmp/repo")

	meta, ok := readCodexSessionMeta(path)
	if !ok {
		t.Fatal("readCodexSessionMeta() ok = false, want true")
	}
	if meta.Payload.ID != "sess-abc" {
		t.Fatalf("ID = %q, want sess-abc", meta.Payload.ID)
	}
	if meta.Payload.CWD != "/tmp/repo" {
		t.Fatalf("CWD = %q, want /tmp/repo", meta.Payload.CWD)
	}
}

func TestDefaultCodexWatchDirs(t *testing.T) {
	now := time.Date(2026, 2, 17, 12, 0, 0, 0, time.UTC)
	dirs := defaultCodexWatchDirs("/tmp/codex", now)
	want := []string{
		"/tmp/codex/2026/02/16",
		"/tmp/codex/2026/02/17",
		"/tmp/codex/2026/02/18",
	}
	if len(dirs) != len(want) {
		t.Fatalf("len(dirs) = %d, want %d (%v)", len(dirs), len(want), dirs)
	}
	for _, d := range want {
		if !slices.Contains(dirs, d) {
			t.Fatalf("dirs = %v, missing %q", dirs, d)
		}
	}
}
