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
		if strings.Contains(err.Error(), "unknown variable") {
			return "", nil
		}
		return "", err
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

// PasteBytes loads data into tmux's buffer and pastes it into the target.
// Uses a uniquely named buffer to avoid races when multiple control-mode
// connections share the same tmux server.
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

	// Use a unique buffer name so concurrent control-mode connections
	// (e.g. adapter + converter) don't clobber each other's paste buffers.
	bufName := fmt.Sprintf("ta-%d", time.Now().UnixNano())

	if err := cm.loadBufferNamed(f.Name(), bufName); err != nil {
		return err
	}
	return cm.pasteBufferNamed(target, bufName)
}

func (cm *ControlMode) loadBufferNamed(path, bufName string) error {
	_, err := cm.Execute(fmt.Sprintf("load-buffer -w -b %s %s", bufName, shellQuote(path)))
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		_, err = cm.Execute(fmt.Sprintf("load-buffer -b %s %s", bufName, shellQuote(path)))
	}
	return err
}

func (cm *ControlMode) pasteBufferNamed(target, bufName string) error {
	_, err := cm.Execute(fmt.Sprintf("paste-buffer -d -b %s -t '%s'", bufName, target))
	return err
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

// CapturePaneVisible captures only the currently visible terminal screen.
// The -a flag prefers the alternate screen buffer when present (full-screen TUIs).
func (cm *ControlMode) CapturePaneVisible(session string) (string, error) {
	out, err := cm.Execute(fmt.Sprintf("capture-pane -p -e -a -t '%s'", session))
	if err != nil && strings.Contains(err.Error(), "no alternate screen") {
		return cm.Execute(fmt.Sprintf("capture-pane -p -e -t '%s'", session))
	}
	return out, err
}

// CapturePaneHistory captures only the scrollback history (above the visible area).
// Returns empty string if there is no scrollback.
func (cm *ControlMode) CapturePaneHistory(session string) (string, error) {
	out, err := cm.Execute(fmt.Sprintf("capture-pane -p -e -t '%s' -S - -E -1", session))
	if err != nil {
		if strings.Contains(err.Error(), "nothing to capture") {
			return "", nil
		}
		return "", err
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

// ResizePaneTo sets the pane to an exact size.
// For pane targets (containing ":"), uses resize-pane directly.
// For session targets, uses resize-window because single-pane windows constrain the pane.
func (cm *ControlMode) ResizePaneTo(target string, cols, rows int) error {
	if strings.Contains(target, ":") {
		_, err := cm.Execute(fmt.Sprintf("resize-pane -t '%s' -x %d -y %d", target, cols, rows))
		return err
	}
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
		if strings.Contains(err.Error(), "can't find session") {
			return false, nil
		}
		return false, err
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

// TmuxPane represents a single pane in the tmux hierarchy.
type TmuxPane struct {
	Index   int    `json:"index"`
	ID      string `json:"id"`
	Target  string `json:"target"`
	Command string `json:"command"`
	PID     string `json:"pid"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Active  bool   `json:"active"`
	WorkDir string `json:"workDir"`
}

// TmuxWindow represents a window containing one or more panes.
type TmuxWindow struct {
	Index  int        `json:"index"`
	Name   string     `json:"name"`
	Active bool       `json:"active"`
	Panes  []TmuxPane `json:"panes"`
}

// TmuxSessionNode represents a tmux session in the full tree.
type TmuxSessionNode struct {
	Name     string       `json:"name"`
	Attached bool         `json:"attached"`
	Windows  []TmuxWindow `json:"windows"`
}

// GetTmuxTree returns the full tmux session/window/pane hierarchy.
func (cm *ControlMode) GetTmuxTree() ([]TmuxSessionNode, error) {
	// Get sessions for attached status
	sessions, err := cm.ListSessions()
	if err != nil {
		return nil, err
	}

	sessionAttached := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		sessionAttached[s.Name] = s.Attached
	}

	// Get all panes across all sessions in a single command
	out, err := cm.Execute("list-panes -a -F '#{session_name}\t#{window_index}\t#{window_name}\t#{window_active}\t#{pane_index}\t#{pane_id}\t#{pane_current_command}\t#{pane_pid}\t#{pane_width}\t#{pane_height}\t#{pane_active}\t#{pane_current_path}'")
	if err != nil {
		return nil, err
	}

	// Track ordering
	type windowKey struct {
		session string
		index   int
	}

	var sessionOrder []string
	sessionSeen := make(map[string]bool)
	windowOrder := make(map[string][]int)
	windowSeen := make(map[windowKey]bool)
	windowData := make(map[windowKey]*TmuxWindow)

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 12)
		if len(parts) < 12 {
			continue
		}

		sessName := parts[0]
		winIdx, _ := strconv.Atoi(parts[1])
		winName := parts[2]
		winActive := parts[3] == "1"
		paneIdx, _ := strconv.Atoi(parts[4])
		paneID := parts[5]
		paneCmd := parts[6]
		panePID := parts[7]
		paneW, _ := strconv.Atoi(parts[8])
		paneH, _ := strconv.Atoi(parts[9])
		paneActive := parts[10] == "1"
		paneWorkDir := parts[11]

		if !sessionSeen[sessName] {
			sessionSeen[sessName] = true
			sessionOrder = append(sessionOrder, sessName)
		}

		wk := windowKey{sessName, winIdx}
		if !windowSeen[wk] {
			windowSeen[wk] = true
			windowOrder[sessName] = append(windowOrder[sessName], winIdx)
			windowData[wk] = &TmuxWindow{
				Index:  winIdx,
				Name:   winName,
				Active: winActive,
			}
		}

		target := fmt.Sprintf("%s:%d.%d", sessName, winIdx, paneIdx)
		windowData[wk].Panes = append(windowData[wk].Panes, TmuxPane{
			Index:   paneIdx,
			ID:      paneID,
			Target:  target,
			Command: paneCmd,
			PID:     panePID,
			Width:   paneW,
			Height:  paneH,
			Active:  paneActive,
			WorkDir: paneWorkDir,
		})
	}

	result := make([]TmuxSessionNode, 0, len(sessionOrder))
	for _, sessName := range sessionOrder {
		sess := TmuxSessionNode{
			Name:     sessName,
			Attached: sessionAttached[sessName],
		}
		for _, winIdx := range windowOrder[sessName] {
			wk := windowKey{sessName, winIdx}
			if w, ok := windowData[wk]; ok {
				sess.Windows = append(sess.Windows, *w)
			}
		}
		result = append(result, sess)
	}

	return result, nil
}

// shellQuote wraps a string for safe passing through tmux send-keys -l.
func shellQuote(s string) string {
	// Use double quotes with escaped internals
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "$", "\\$")
	return "\"" + s + "\""
}
