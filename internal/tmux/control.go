package tmux

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Notification represents a parsed tmux control mode event.
type Notification struct {
	Type string // "sessions-changed", "session-changed", "output", etc.
	Args string // raw arguments after the notification type
}

// commandResponse holds the result of a control mode command.
type commandResponse struct {
	output string
	err    error
}

const defaultExecuteTimeout = 10 * time.Second

// ControlMode manages a tmux control mode connection.
// Commands are serialized — only one Execute() call runs at a time.
type ControlMode struct {
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	notifications  chan Notification
	responseCh     chan commandResponse // single channel for current pending command
	execMu         sync.Mutex           // serializes Execute() calls
	done           chan struct{}
	closing        atomic.Bool
	session        string
	executeTimeout time.Duration
}

// NewControlMode creates and starts a tmux control mode connection.
// It creates a session with the given name if needed, then attaches in control mode.
func NewControlMode(sessionName string) (*ControlMode, error) {

	// Create monitor session if it doesn't exist
	create := exec.Command("tmux", "-u", "new-session", "-d", "-s", sessionName)
	if err := create.Run(); err != nil {
		// Session may already exist; this is non-fatal.
		log.Printf("tmux monitor session create (%s): %v", sessionName, err)
	}

	cm := &ControlMode{
		notifications:  make(chan Notification, 100),
		responseCh:     make(chan commandResponse, 1),
		done:           make(chan struct{}),
		session:        sessionName,
		executeTimeout: defaultExecuteTimeout,
	}

	cm.cmd = exec.Command("tmux", "-u", "-C", "attach", "-t", sessionName)
	var err error
	cm.stdin, err = cm.cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cm.cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cm.cmd.Start(); err != nil {
		return nil, fmt.Errorf("start tmux control mode: %w", err)
	}

	go cm.readLoop(stdout)

	// Wait for the initial attach response (command 0) to be consumed by readLoop
	// before accepting any Execute() calls. The readLoop handles this by dropping
	// responses when no command is pending.

	return cm, nil
}

// Execute sends a command through control mode and returns the response.
// Serialized: only one command in flight at a time.
func (cm *ControlMode) Execute(command string) (string, error) {
	cm.execMu.Lock()
	defer cm.execMu.Unlock()

	// Drain any stale response (shouldn't happen, but be safe)
	select {
	case <-cm.responseCh:
	default:
	}

	// Write command to stdin
	_, err := fmt.Fprintf(cm.stdin, "%s\n", command)
	if err != nil {
		return "", fmt.Errorf("write command: %w", err)
	}

	// Wait for response
	select {
	case resp := <-cm.responseCh:
		return resp.output, resp.err
	case <-time.After(cm.executeTimeout):
		return "", fmt.Errorf("tmux command timed out after %s: %s", cm.executeTimeout, command)
	case <-cm.done:
		return "", fmt.Errorf("tmux control mode closed")
	}
}

// Notifications returns the channel for receiving tmux events.
func (cm *ControlMode) Notifications() <-chan Notification {
	return cm.notifications
}

// Close shuts down the control mode connection and kills the monitor session.
func (cm *ControlMode) Close() {
	cm.closing.Store(true)
	if err := cm.stdin.Close(); err != nil {
		log.Printf("tmux control stdin close: %v", err)
	}
	if err := cm.cmd.Wait(); err != nil {
		log.Printf("tmux control wait: %v", err)
	}
	close(cm.done)

	// Kill the monitor session
	if err := exec.Command("tmux", "-u", "kill-session", "-t", cm.session).Run(); err != nil {
		log.Printf("tmux monitor session kill (%s): %v", cm.session, err)
	}
}

// readLoop reads stdout from the tmux control mode process and dispatches
// responses and notifications.
//
// tmux control mode protocol:
//
//	%begin TIME NUMBER FLAGS  — start of command response
//	...output lines...
//	%end TIME NUMBER FLAGS    — success
//	%error TIME NUMBER FLAGS  — failure
//
// NUMBER is a tmux server-global command counter (second field, not sequential
// per session). Since we serialize commands, we simply match each %begin/%end
// pair to the single pending Execute() call.
func (cm *ControlMode) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer for large outputs

	var currentCmdNum uint64
	var currentOutput strings.Builder
	inResponse := false
	cmdsSeen := 0 // track how many complete command responses we've seen

	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, "%begin "):
			// %begin TIME NUMBER FLAGS
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				if n, err := strconv.ParseUint(parts[2], 10, 64); err == nil {
					currentCmdNum = n
					currentOutput.Reset()
					inResponse = true
				}
			}

		case strings.HasPrefix(line, "%end "):
			if inResponse {
				parts := strings.Fields(line)
				if len(parts) >= 3 {
					if n, err := strconv.ParseUint(parts[2], 10, 64); err == nil && n == currentCmdNum {
						inResponse = false
						cmdsSeen++
						if cmdsSeen > 1 {
							// Skip initial attach response (cmdsSeen==1)
							cm.responseCh <- commandResponse{output: currentOutput.String()}
						}
					}
				}
			}

		case strings.HasPrefix(line, "%error "):
			if inResponse {
				parts := strings.Fields(line)
				if len(parts) >= 3 {
					if n, err := strconv.ParseUint(parts[2], 10, 64); err == nil && n == currentCmdNum {
						inResponse = false
						cmdsSeen++
						if cmdsSeen > 1 {
							errMsg := currentOutput.String()
							if errMsg == "" {
								errMsg = "command failed"
							}
							cm.responseCh <- commandResponse{err: fmt.Errorf("tmux: %s", strings.TrimSpace(errMsg))}
						}
					}
				}
			}

		case inResponse:
			if currentOutput.Len() > 0 {
				currentOutput.WriteByte('\n')
			}
			currentOutput.WriteString(line)

		case strings.HasPrefix(line, "%sessions-changed"):
			cm.notifications <- Notification{Type: "sessions-changed"}

		case strings.HasPrefix(line, "%session-changed"):
			cm.notifications <- Notification{Type: "session-changed", Args: strings.TrimPrefix(line, "%session-changed ")}

		case strings.HasPrefix(line, "%output"):
			cm.notifications <- Notification{Type: "output", Args: strings.TrimPrefix(line, "%output ")}

		case strings.HasPrefix(line, "%unlinked-window-renamed"):
			cm.notifications <- Notification{Type: "window-renamed", Args: strings.TrimPrefix(line, "%unlinked-window-renamed ")}

		case strings.HasPrefix(line, "%window-renamed"):
			cm.notifications <- Notification{Type: "window-renamed", Args: strings.TrimPrefix(line, "%window-renamed ")}

		case strings.HasPrefix(line, "%window-"):
			// Ignore other window events (add, close, pane-changed)

		case strings.HasPrefix(line, "%layout-change"):
			// Ignore layout changes

		case strings.HasPrefix(line, "%exit"):
			// Control mode is exiting

		default:
			if strings.HasPrefix(line, "%") {
				log.Printf("unhandled tmux notification: %s", line)
			}
		}
	}

	if err := scanner.Err(); err != nil && !cm.closing.Load() {
		log.Printf("tmux control mode read error: %v", err)
	}
}
