package tmux

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SessionInfo holds basic tmux session information.
type SessionInfo struct {
	Name     string
	Attached bool
}

// PaneInfo holds tmux pane details.
type PaneInfo struct {
	PaneID  string
	Command string
	PID     string
	WorkDir string
}

// ListSessions returns all tmux sessions with their attached status.
func (cm *ControlMode) ListSessions() ([]SessionInfo, error) {
	out, err := cm.Execute("list-sessions -F '#{session_name}\t#{session_attached}'")
	if err != nil {
		return nil, err
	}

	var sessions []SessionInfo
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		sessions = append(sessions, SessionInfo{
			Name:     parts[0],
			Attached: parts[1] != "0",
		})
	}
	return sessions, nil
}

// ShowEnvironment reads a session environment variable.
// Returns empty string if the variable is not set.
func (cm *ControlMode) ShowEnvironment(session, key string) (string, error) {
	out, err := cm.Execute(fmt.Sprintf("show-environment -t '%s' %s", session, key))
	if err != nil {
		// Variable not set is not a fatal error
		return "", nil
	}

	// Output format: KEY=value
	out = strings.TrimSpace(out)
	if _, val, ok := strings.Cut(out, "="); ok {
		return val, nil
	}
	return "", nil
}

// GetPaneInfo returns pane details for the first pane in a session.
func (cm *ControlMode) GetPaneInfo(session string) (PaneInfo, error) {
	out, err := cm.Execute(fmt.Sprintf("list-panes -t '%s' -F '#{pane_id}\t#{pane_current_command}\t#{pane_pid}\t#{pane_current_path}'", session))
	if err != nil {
		return PaneInfo{}, err
	}

	// Take the first pane
	line := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	parts := strings.SplitN(line, "\t", 4)
	if len(parts) < 4 {
		return PaneInfo{}, fmt.Errorf("unexpected pane info format: %q", line)
	}

	return PaneInfo{
		PaneID:  parts[0],
		Command: parts[1],
		PID:     parts[2],
		WorkDir: parts[3],
	}, nil
}

// SendKeysLiteral sends text in literal mode (no key name interpretation).
func (cm *ControlMode) SendKeysLiteral(target, text string) error {
	_, err := cm.Execute(fmt.Sprintf("send-keys -t '%s' -l %s", target, shellQuote(text)))
	return err
}

// SendKeysBytes sends raw bytes exactly as keyboard input.
// Uses send-keys -H to avoid command parsing issues with control bytes.
func (cm *ControlMode) SendKeysBytes(target string, data []byte) error {
	if len(data) == 0 {
		return nil
	}

	// Older tmux versions may not support -H; fall back to literal mode.
	if err := cm.sendKeysHex(target, data); err != nil {
		if strings.Contains(err.Error(), "unknown flag -H") {
			return cm.SendKeysLiteral(target, string(data))
		}
		return err
	}

	return nil
}

// SendKeysRaw sends key names without literal mode.
func (cm *ControlMode) SendKeysRaw(target string, keys ...string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "send-keys -t '%s'", target)
	for _, key := range keys {
		b.WriteByte(' ')
		b.WriteString(key)
	}
	_, err := cm.Execute(b.String())
	return err
}

// LoadBufferFromFile loads bytes into tmux's paste buffer.
// Uses -w when available so tmux can propagate to the system clipboard.
func (cm *ControlMode) LoadBufferFromFile(path string) error {
	_, err := cm.Execute(fmt.Sprintf("load-buffer -w %s", shellQuote(path)))
	if err != nil && strings.Contains(err.Error(), "unknown flag -w") {
		_, err = cm.Execute(fmt.Sprintf("load-buffer %s", shellQuote(path)))
	}
	return err
}

// PasteBuffer pastes the current tmux buffer into the target pane/session.
func (cm *ControlMode) PasteBuffer(target string) error {
	_, err := cm.Execute(fmt.Sprintf("paste-buffer -d -t '%s'", target))
	return err
}

// PasteBytes loads data into tmux's buffer and pastes it into the target.
func (cm *ControlMode) PasteBytes(target string, data []byte) error {
	if len(data) == 0 {
		return nil
	}

	f, err := os.CreateTemp("", "tmux-adapter-buffer-*")
	if err != nil {
		return fmt.Errorf("create temp buffer file: %w", err)
	}
	defer func() {
		if rmErr := os.Remove(f.Name()); rmErr != nil {
			log.Printf("PasteBytes(%s): cleanup temp file %s: %v", target, f.Name(), rmErr)
		}
	}()

	if _, err := f.Write(data); err != nil {
		if closeErr := f.Close(); closeErr != nil {
			log.Printf("PasteBytes(%s): close temp file after write error: %v", target, closeErr)
		}
		return fmt.Errorf("write temp buffer file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp buffer file: %w", err)
	}

	if err := cm.LoadBufferFromFile(f.Name()); err != nil {
		return err
	}
	return cm.PasteBuffer(target)
}

func (cm *ControlMode) sendKeysHex(target string, data []byte) error {
	const chunkSize = 128 // keep command length reasonable for large pastes

	for start := 0; start < len(data); start += chunkSize {
		end := start + chunkSize
		if end > len(data) {
			end = len(data)
		}

		var b strings.Builder
		fmt.Fprintf(&b, "send-keys -t '%s' -H", target)
		for _, by := range data[start:end] {
			fmt.Fprintf(&b, " %02x", by)
		}

		if _, err := cm.Execute(b.String()); err != nil {
			return err
		}
	}

	return nil
}

// CapturePaneAll captures the entire scrollback history of a session with ANSI escape codes.
func (cm *ControlMode) CapturePaneAll(session string) (string, error) {
	return cm.Execute(fmt.Sprintf("capture-pane -p -e -t '%s' -S -", session))
}

// CapturePaneHistory captures only the scrollback history (above the visible area).
// Returns empty string if there is no scrollback.
func (cm *ControlMode) CapturePaneHistory(session string) (string, error) {
	out, err := cm.Execute(fmt.Sprintf("capture-pane -p -e -t '%s' -S - -E -1", session))
	if err != nil {
		return "", nil // no history lines — not an error
	}
	return out, nil
}

// ForceRedraw triggers a SIGWINCH by briefly changing the window size.
// Uses resize-window (not resize-pane) because single-pane windows
// constrain the pane to the window size, making resize-pane a no-op.
func (cm *ControlMode) ForceRedraw(session string) {
	log.Printf("ForceRedraw(%s): starting", session)

	sizeStr, err := cm.DisplayMessage(session, "#{window_width}:#{window_height}")
	if err != nil {
		log.Printf("ForceRedraw(%s): display-message error: %v", session, err)
		cm.forceRedrawViaSIGWINCH(session)
		return
	}
	log.Printf("ForceRedraw(%s): window size = %s", session, sizeStr)

	parts := strings.SplitN(sizeStr, ":", 2)
	if len(parts) != 2 {
		log.Printf("ForceRedraw(%s): unexpected format: %q", session, sizeStr)
		cm.forceRedrawViaSIGWINCH(session)
		return
	}
	width, _ := strconv.Atoi(parts[0])
	height, _ := strconv.Atoi(parts[1])
	if width <= 0 || height <= 1 {
		log.Printf("ForceRedraw(%s): invalid dimensions %dx%d", session, width, height)
		cm.forceRedrawViaSIGWINCH(session)
		return
	}

	if err := cm.ResizeWindow(session, width, height-1); err != nil {
		log.Printf("ForceRedraw(%s): shrink window error: %v — trying SIGWINCH", session, err)
		cm.forceRedrawViaSIGWINCH(session)
		return
	}
	time.Sleep(50 * time.Millisecond)
	if err := cm.ResizeWindow(session, width, height); err != nil {
		log.Printf("ForceRedraw(%s): restore window error: %v", session, err)
	}
	log.Printf("ForceRedraw(%s): window resize dance complete", session)
}

// forceRedrawViaSIGWINCH sends SIGWINCH directly to the pane's process group.
func (cm *ControlMode) forceRedrawViaSIGWINCH(session string) {
	info, err := cm.GetPaneInfo(session)
	if err != nil {
		log.Printf("forceRedrawViaSIGWINCH(%s): get pane info: %v", session, err)
		return
	}
	pid, err := strconv.Atoi(info.PID)
	if err != nil {
		log.Printf("forceRedrawViaSIGWINCH(%s): parse PID %q: %v", session, info.PID, err)
		return
	}
	// Send to process group (negative PID)
	if err := syscall.Kill(-pid, syscall.SIGWINCH); err != nil {
		log.Printf("forceRedrawViaSIGWINCH(%s): kill -%d: %v", session, pid, err)
		// Try positive PID as fallback
		if err := syscall.Kill(pid, syscall.SIGWINCH); err != nil {
			log.Printf("forceRedrawViaSIGWINCH(%s): kill %d: %v", session, pid, err)
		}
	}
	log.Printf("forceRedrawViaSIGWINCH(%s): sent SIGWINCH to pid %d", session, pid)
}

// ResizePane adjusts the pane height by delta (e.g., "-1" or "+1").
func (cm *ControlMode) ResizePane(target, delta string) error {
	_, err := cm.Execute(fmt.Sprintf("resize-pane -t '%s' -y %s", target, delta))
	return err
}

// DisplayMessage queries a session variable using display-message.
func (cm *ControlMode) DisplayMessage(session, format string) (string, error) {
	out, err := cm.Execute(fmt.Sprintf("display-message -t '%s' -p '%s'", session, format))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// PipePaneStart activates pipe-pane for output-only streaming to a command.
func (cm *ControlMode) PipePaneStart(session, command string) error {
	_, err := cm.Execute(fmt.Sprintf("pipe-pane -o -t '%s' '%s'", session, command))
	return err
}

// PipePaneStop deactivates pipe-pane for a session.
func (cm *ControlMode) PipePaneStop(session string) error {
	_, err := cm.Execute(fmt.Sprintf("pipe-pane -t '%s'", session))
	return err
}

// ResizePaneTo sets the pane (and its window) to an exact size.
// Uses resize-window because single-pane windows constrain the pane to window size.
func (cm *ControlMode) ResizePaneTo(target string, cols, rows int) error {
	return cm.ResizeWindow(target, cols, rows)
}

// ResizeWindow sets a session's window to an exact size.
func (cm *ControlMode) ResizeWindow(target string, cols, rows int) error {
	_, err := cm.Execute(fmt.Sprintf("resize-window -t '%s' -x %d -y %d", target, cols, rows))
	return err
}

// KillSession destroys a tmux session.
func (cm *ControlMode) KillSession(session string) error {
	_, err := cm.Execute(fmt.Sprintf("kill-session -t '%s'", session))
	return err
}

// HasSession checks if a session exists using exact matching.
func (cm *ControlMode) HasSession(session string) (bool, error) {
	_, err := cm.Execute(fmt.Sprintf("has-session -t '=%s'", session))
	if err != nil {
		return false, nil
	}
	return true, nil
}

// IsSessionAttached checks if a human is attached to the session.
func (cm *ControlMode) IsSessionAttached(session string) (bool, error) {
	out, err := cm.DisplayMessage(session, "#{session_attached}")
	if err != nil {
		return false, err
	}
	return out != "0", nil
}

// shellQuote wraps a string for safe passing through tmux send-keys -l.
func shellQuote(s string) string {
	// Use double quotes with escaped internals
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "$", "\\$")
	return "\"" + s + "\""
}
