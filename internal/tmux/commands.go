package tmux

import (
	"fmt"
	"strings"
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

// CapturePaneAll captures the entire scrollback history of a session.
func (cm *ControlMode) CapturePaneAll(session string) (string, error) {
	return cm.Execute(fmt.Sprintf("capture-pane -p -t '%s' -S -", session))
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
