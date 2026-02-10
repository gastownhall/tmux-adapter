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

// ControlMode manages a tmux control mode connection.
type ControlMode struct {
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	notifications chan Notification
	pending       map[uint64]chan commandResponse
	pendingMu     sync.Mutex
	cmdNum        atomic.Uint64
	done          chan struct{}
	session       string // monitor session name
}

// NewControlMode creates and starts a tmux control mode connection.
// It creates an "adapter-monitor" session if needed, then attaches in control mode.
func NewControlMode() (*ControlMode, error) {
	sessionName := "adapter-monitor"

	// Create monitor session if it doesn't exist
	create := exec.Command("tmux", "-u", "new-session", "-d", "-s", sessionName)
	create.Run() // ignore error — session may already exist

	cm := &ControlMode{
		notifications: make(chan Notification, 100),
		pending:       make(map[uint64]chan commandResponse),
		done:          make(chan struct{}),
		session:       sessionName,
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
	return cm, nil
}

// Execute sends a command through control mode and returns the response.
func (cm *ControlMode) Execute(command string) (string, error) {
	num := cm.cmdNum.Add(1)
	ch := make(chan commandResponse, 1)

	cm.pendingMu.Lock()
	cm.pending[num] = ch
	cm.pendingMu.Unlock()

	// Write command to stdin
	_, err := fmt.Fprintf(cm.stdin, "%s\n", command)
	if err != nil {
		cm.pendingMu.Lock()
		delete(cm.pending, num)
		cm.pendingMu.Unlock()
		return "", fmt.Errorf("write command: %w", err)
	}

	// Wait for response
	resp := <-ch
	return resp.output, resp.err
}

// Notifications returns the channel for receiving tmux events.
func (cm *ControlMode) Notifications() <-chan Notification {
	return cm.notifications
}

// Close shuts down the control mode connection and kills the monitor session.
func (cm *ControlMode) Close() {
	cm.stdin.Close()
	cm.cmd.Wait()
	close(cm.done)

	// Kill the monitor session
	exec.Command("tmux", "-u", "kill-session", "-t", cm.session).Run()
}

// readLoop reads stdout from the tmux control mode process and dispatches responses/notifications.
func (cm *ControlMode) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer for large outputs

	var currentCmdNum uint64
	var currentOutput strings.Builder
	inResponse := false

	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, "%begin "):
			// %begin TIMESTAMP FLAGS CMD_NUM
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				if n, err := strconv.ParseUint(parts[3], 10, 64); err == nil {
					currentCmdNum = n
					currentOutput.Reset()
					inResponse = true
				}
			}

		case strings.HasPrefix(line, "%end "):
			// %end TIMESTAMP FLAGS CMD_NUM
			if inResponse {
				parts := strings.Fields(line)
				if len(parts) >= 4 {
					if n, err := strconv.ParseUint(parts[3], 10, 64); err == nil && n == currentCmdNum {
						cm.pendingMu.Lock()
						ch, ok := cm.pending[currentCmdNum]
						if ok {
							delete(cm.pending, currentCmdNum)
						}
						cm.pendingMu.Unlock()

						if ok {
							ch <- commandResponse{output: currentOutput.String()}
						}
						inResponse = false
					}
				}
			}

		case strings.HasPrefix(line, "%error "):
			// %error TIMESTAMP FLAGS CMD_NUM
			if inResponse {
				parts := strings.Fields(line)
				if len(parts) >= 4 {
					if n, err := strconv.ParseUint(parts[3], 10, 64); err == nil && n == currentCmdNum {
						cm.pendingMu.Lock()
						ch, ok := cm.pending[currentCmdNum]
						if ok {
							delete(cm.pending, currentCmdNum)
						}
						cm.pendingMu.Unlock()

						if ok {
							errMsg := currentOutput.String()
							if errMsg == "" {
								errMsg = "command failed"
							}
							ch <- commandResponse{err: fmt.Errorf("tmux: %s", strings.TrimSpace(errMsg))}
						}
						inResponse = false
					}
				}
			}

		case inResponse:
			// Command output line
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

		case strings.HasPrefix(line, "%window-"):
			// Window events — ignore for now

		case strings.HasPrefix(line, "%layout-change"):
			// Layout changes — ignore

		default:
			if strings.HasPrefix(line, "%") {
				log.Printf("unhandled tmux notification: %s", line)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("tmux control mode read error: %v", err)
	}
}
