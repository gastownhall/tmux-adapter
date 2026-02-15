package agentio

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gastownhall/tmux-adapter/internal/agents"
	"github.com/gastownhall/tmux-adapter/internal/tmux"
)

// Prompter handles sending prompts and file uploads to agents via tmux.
// It owns per-agent mutexes for serializing sends.
type Prompter struct {
	Ctrl     *tmux.ControlMode
	Registry *agents.Registry
	locks    map[string]*sync.Mutex
	locksMu  sync.Mutex
}

// NewPrompter creates a new Prompter.
func NewPrompter(ctrl *tmux.ControlMode, registry *agents.Registry) *Prompter {
	return &Prompter{
		Ctrl:     ctrl,
		Registry: registry,
		locks:    make(map[string]*sync.Mutex),
	}
}

// GetLock returns the per-agent mutex for serializing sends.
func (p *Prompter) GetLock(agent string) *sync.Mutex {
	p.locksMu.Lock()
	defer p.locksMu.Unlock()
	if _, ok := p.locks[agent]; !ok {
		p.locks[agent] = &sync.Mutex{}
	}
	return p.locks[agent]
}

// SendPrompt sends a prompt to an agent using the nudge sequence:
// SendKeysLiteral → 500ms → Escape → 100ms → Enter (3x retry, 200ms) → SIGWINCH wake.
// The caller must hold the per-agent lock.
func (p *Prompter) SendPrompt(agentName, prompt string) error {
	agent, ok := p.Registry.GetAgent(agentName)
	if !ok {
		return fmt.Errorf("agent not found: %s", agentName)
	}

	session := agent.Name

	// 1. Send text in literal mode
	if err := p.Ctrl.SendKeysLiteral(session, prompt); err != nil {
		return fmt.Errorf("send literal: %w", err)
	}

	// 2. Wait 500ms for paste to complete
	time.Sleep(500 * time.Millisecond)

	// 3. Send Escape (for vim mode)
	if err := p.Ctrl.SendKeysRaw(session, "Escape"); err != nil {
		return fmt.Errorf("send Escape: %w", err)
	}
	time.Sleep(100 * time.Millisecond)

	// 4. Send Enter with 3x retry, 200ms backoff
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		if err := p.Ctrl.SendKeysRaw(session, "Enter"); err != nil {
			lastErr = err
			continue
		}

		// 5. Wake detached sessions via SIGWINCH resize dance
		if !agent.Attached {
			if err := p.Ctrl.ResizePane(session, "-1"); err != nil {
				log.Printf("send-prompt(%s): wake shrink resize failed: %v", session, err)
			}
			time.Sleep(50 * time.Millisecond)
			if err := p.Ctrl.ResizePane(session, "+1"); err != nil {
				log.Printf("send-prompt(%s): wake restore resize failed: %v", session, err)
			}
		}

		return nil
	}

	errMsg := "failed to send Enter after 3 attempts"
	if lastErr != nil {
		errMsg += ": " + lastErr.Error()
	}
	return fmt.Errorf("%s", errMsg)
}
