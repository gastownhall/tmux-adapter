# PLAN: Dynamic Session Filtering for tmux-adapter

## 1. Project Identity and Goals

### What
Replace the hardcoded `IsGastownSession()` filter in the shared agent registry with dynamic, per-client regex filtering over the WebSocket protocol. Make `--gt-dir` optional. Add lazy conversation tailing. Update both dashboards with filter UI.

### Why
The current tmux-adapter/converter only detects agents in Gastown-named tmux sessions. Users running Claude Code (or Gemini, Codex, etc.) in plain tmux sessions — outside Gastown — get no visibility. This change makes the same server work for both cases, controlled by client-side filters.

### Goals
1. **Universal agent detection**: The registry scans ALL tmux sessions for agent processes, not just GT-named ones
2. **Per-client filtering**: Each WebSocket client specifies its own include/exclude session-name regex
3. **Lazy tailing**: ConversationWatcher starts JSONL file tailing only when a client subscribes, stops when the last client leaves
4. **Dashboard filter UI**: Both adapter.html and converter.html get include/exclude regex input fields with presets
5. **Partial backward compatibility**: The `--work-dir` flag (aliased as `--gt-dir`) still filters by workdir when provided. The filter protocol extension is additive (old clients work without sending filters). However, removing `role`/`rig` from the Agent struct IS a breaking protocol change for existing adapter consumers, and changing the default scan scope (empty `--work-dir` = all sessions) changes default behavior. This is intentional — the deGasification is a clean break from GT-specific semantics.

### Non-Goals (Exclusions)
- **No new binary**: This is a modification to existing adapter and converter, not a new service
- **No Gemini/Codex conversation parsers**: Only agent detection changes; conversation streaming remains Claude-only (parsers can be added later independently)
- **No server-side filter CLI flag**: Filtering is client-side only (server-side baseline can be added later as Idea #15)
- **No agent grouping UI**: Working-directory grouping (Idea #13) is a future enhancement
- **No protocol version bump for filter fields**: The filter extension to `subscribe-agents` is additive and backward-compatible. However, removing `role`/`rig` from agent payloads IS a breaking change for existing adapter API consumers — this is intentional (deGasification). The adapter API spec (`specs/adapter-api.md`) must be updated to reflect the new Agent model.

---

## 2. Architecture Overview

### Candidate Evaluation

**Candidate A: Pure client-side filtering (chosen)**
- Registry scans all sessions, clients filter their view via WebSocket parameters
- Pro: Per-client views, no server restart needed, same server serves GT and standalone
- Con: Registry does more work scanning non-agent sessions
- Verdict: **Selected** — the scanning overhead is negligible (one `list-sessions` + `pgrep` per scan)

**Candidate B: Server-side registry filter**
- Registry accepts a configurable regex, only tracks matching sessions
- Pro: Efficient — no wasted scanning
- Con: All clients share same view, requires restart to change
- Verdict: Rejected — doesn't support per-client filtering

**Candidate C: Hybrid (server baseline + client filter)**
- Server-side baseline regex via CLI flag, client-side filter on top
- Pro: Maximum flexibility
- Con: More complexity than needed for the initial use case
- Verdict: Deferred — can layer on later if needed (Idea #15)

### Component Change Map

```
CHANGES NEEDED:
├── internal/agents/detect.go          [MODIFY] Remove IsGastownSession(), improve standalone naming
├── internal/agents/registry.go        [MODIFY] Remove GT filter, make gtDir optional, add Count()
├── internal/conv/watcher.go           [MODIFY] Lazy tailing with ref-counting, Stop() cleanup
├── internal/wsconv/server.go          [MODIFY] Add session filter to subscribe-agents, pending conv subs
├── internal/wsadapter/server.go       [MODIFY] Add session filter to subscribe-agents
├── internal/wsadapter/handler.go      [MODIFY] Filter agent broadcasts per client
├── internal/wsadapter/client.go       [MODIFY] Add filter regex fields to client struct
├── internal/adapter/adapter.go        [MODIFY] Update forwardEvents to pass agentName to new BroadcastToAgentSubscribers signature
├── internal/converter/converter.go    [MODIFY] Rename gtDir → workDirFilter
├── main.go                            [MODIFY] Make --gt-dir optional
├── cmd/tmux-converter/main.go         [MODIFY] Make --gt-dir optional
├── samples/adapter.html               [MODIFY] Add filter UI
├── samples/converter.html             [MODIFY] Add filter UI
└── internal/agents/detect_test.go     [MODIFY] Update tests for removed IsGastownSession

NO CHANGES:
├── internal/tmux/                     (tmux control mode, commands — unchanged)
├── internal/conv/discovery.go         (file discovery — unchanged)
├── internal/conv/claude.go            (JSONL parser — unchanged)
├── internal/conv/tailer.go            (file tailer — unchanged)
├── internal/conv/buffer.go            (ring buffer — unchanged)
├── internal/conv/event.go             (event model — unchanged)
├── internal/wsbase/                   (auth, upgrader — unchanged)
└── web/                               (reusable components — unchanged)
```

---

## 3. Subsystem Design

### 3.1 Registry: Remove Hardcoded Gastown Filter

#### Current Code (registry.go scan(), lines 107-226)

The scan() function has four filters in sequence:
1. `IsGastownSession(sess.Name)` — **REMOVE THIS**
2. `shouldSkip(sess.Name)` — keep (skip monitor sessions)
3. Process alive check — keep (detect agent binary)
4. `workDirFilter` workdir prefix — **MAKE OPTIONAL**

#### Changes

**detect.go** — Remove ALL Gastown-specific code:
- Delete `IsGastownSession()` function (lines 194-198)
- Delete `ParseSessionName()` function (lines 113-167) — this is entirely GT naming convention logic
- Delete GT env var constants/usage (`GT_AGENT`, `GT_ROLE`, `GT_RIG`)
- **Replace `InferRuntime()` with `DetectRuntime()`** — the current `InferRuntime` has two critical bugs: (a) it returns `"claude"` as default (line 191), meaning *every* tmux session would be detected as an agent, and (b) it iterates a Go map with random order, causing non-deterministic runtime identification for overlapping process names like `node` (shared by claude and opencode). The new `DetectRuntime` returns `""` when no agent is found and uses an ordered priority list.
- **Add `runtimePriority` ordered slice** — defines the order in which runtimes are checked: `["claude", "gemini", "codex", "cursor", "auggie", "amp", "opencode"]`. More specific runtimes (claude) are checked before less specific ones (opencode) to resolve overlapping process names like `node`.
- Keep: `runtimeProcessNames`, `GetProcessNames()`, `IsAgentProcess()`, `IsShell()`, `CheckProcessBinary()`, `CheckDescendants()` — these are all generic agent detection

**registry.go scan()** — Simplify to runtime-only detection:
- Remove the `if !IsGastownSession(sess.Name) { continue }` block (line 117)
- Remove GT env var reading: `ShowEnvironment(sess.Name, "GT_AGENT")`, `"GT_ROLE"`, `"GT_RIG"` (lines 134-136)
- Remove `ParseSessionName()` call (line 170)
- The `shouldSkip()` check remains (skip monitor sessions)
- The `workDirFilter` check already handles empty: `if r.workDirFilter != "" && !strings.HasPrefix(...)` — no change needed
- Agent runtime: use `DetectRuntime(pane.Command, pane.PID)` directly — returns `""` for non-agents (no GT_AGENT fallback, no false-positive default)
- Agent name: session name (already the case)
- `ShowEnvironment` calls removed from scan() — the `ControlModeInterface` method can stay (used elsewhere) but registry no longer calls it

**New `DetectRuntime` function** (replaces `InferRuntime`):

The current `InferRuntime` has three critical bugs: (a) returns `"claude"` as default — every session becomes an agent, (b) iterates a Go map with non-deterministic order — overlapping names like `node` resolve randomly, (c) doesn't check descendants — shell-wrapped agents are missed.

```go
// runtimePriority defines the order in which runtimes are checked.
// More specific runtimes first: "claude" before "opencode" because
// both list "node" as a process name.
var runtimePriority = []string{
    "claude", "gemini", "codex", "cursor", "auggie", "amp", "opencode",
}

// DetectRuntime performs the full 3-tier agent detection across all
// known runtimes. Returns the runtime name or "" if no agent found.
func DetectRuntime(paneCommand, pid string) string {
    // Tier 1: Direct pane command match
    for _, runtime := range runtimePriority {
        if IsAgentProcess(paneCommand, runtimeProcessNames[runtime]) {
            return runtime
        }
    }

    // Tier 2: Shell wrapping → check descendants
    // OPTIMIZATION (all three R2 reviewers): Collect ALL descendant process
    // names in a single tree-collection phase (O(depth) pgrep calls), then
    // match against all runtimes in-memory. This reduces 7×depth pgrep
    // invocations to 1×depth per session.
    if IsShell(paneCommand) && pid != "" {
        descendants := CollectDescendantNames(pid)  // O(depth) pgrep calls
        for _, runtime := range runtimePriority {
            if matchesAny(descendants, runtimeProcessNames[runtime]) {
                return runtime
            }
        }
        return ""  // shell with no agent descendants
    }

    // Tier 3: Unrecognized command (e.g., version-as-argv[0] like "2.1.38")
    // Check binary path first, then descendants.
    // NOTE: Tier 3 descendant walking applies to ALL unrecognized commands,
    // which can cause transitive false positives (e.g., a Python process with
    // a Node.js child would match as "claude"). This is a known limitation.
    // Future: restrict Tier 3 descendants to version-as-argv[0] patterns only.
    if pid != "" {
        for _, runtime := range runtimePriority {
            if CheckProcessBinary(pid, runtimeProcessNames[runtime]) {
                return runtime
            }
        }
        descendants := CollectDescendantNames(pid)  // O(depth) pgrep calls
        for _, runtime := range runtimePriority {
            if matchesAny(descendants, runtimeProcessNames[runtime]) {
                return runtime
            }
        }
    }

    return ""  // no agent found — this session is NOT an agent
}
```

**Simplified scan() algorithm** (pseudocode):
```
for each tmux session:
    if shouldSkip(session.Name): continue
    pane = GetPaneInfo(session.Name)

    // Full 3-tier detection across all known runtimes
    runtime = DetectRuntime(pane.Command, pane.PID)
    if runtime == "":
        continue  // no known agent process found

    // Optional workdir filter
    if workDirFilter != "" && !strings.HasPrefix(pane.WorkDir, workDirFilter):
        continue

    agent = Agent{
        Name:     session.Name,
        Runtime:  runtime,
        WorkDir:  pane.WorkDir,
        Attached: session.Attached,
    }
    discovered[session.Name] = agent
```

**Key differences from `InferRuntime`**: (1) Returns `""` for non-agent sessions — the critical gatekeeper behavior. (2) Uses ordered `runtimePriority` slice — deterministic resolution of overlapping process names. (3) Includes descendant walking (Tier 2/3) — shell-wrapped agents are detected. (4) Subsumes the 3-tier alive check from the old scan() — no separate `alive` variable needed.

**Complexity analysis**: Let S = total tmux sessions, R = known runtimes (|runtimePriority| = 7).
```
Best case per session:   O(R) string comparisons (Tier 1 match on direct command)
Worst case per session:  O(depth) pgrep calls + O(R × |descendants|) in-memory matching (Tier 2/3)
Typical per scan:        O(S × depth) pgrep calls (one tree walk per shell session) + O(S × R) string comparisons
                         Most sessions are shells; pgrep returns fast for shells with no agents.
                         Typical depth ≤ 3 for shell → agent process trees.
External process cost:   At most S × depth pgrep invocations per scan cycle (was S × R × depth before optimization).
```

**New helper functions** for the optimized descendant detection:
```go
// CollectDescendantNames walks the process tree below pid, collecting all
// descendant process names. Internally makes O(depth) pgrep -P calls
// (one per tree level, max 10 levels), but collects names into a single
// slice so callers match against all runtimes in-memory without repeating
// the walk. The optimization vs the old CheckDescendants: we collect ONCE
// per session and match against all 7 runtimes in memory, instead of
// walking the tree 7 times (once per runtime). Savings: 7×depth → 1×depth
// pgrep calls per shell session.
func CollectDescendantNames(pid string) []string

// matchesAny checks if any name in descendants matches any name in processNames.
func matchesAny(descendants, processNames []string) bool
```

**Note on `node` false positives** (Codex review finding): A plain Node.js process (e.g., web server) running in tmux would match `claude` via Tier 1 direct command match. The existing code has the same limitation — `InferRuntime` also matches `node` to `claude`. Mitigating this fully requires checking command-line arguments (e.g., looking for `@anthropic-ai/claude-code` in the process args), which is a future enhancement. For now, the ordered priority list ensures deterministic resolution when `node` matches multiple runtimes.

#### Data Structures

**Simplified Agent struct** — remove GT-specific fields:
```go
type Agent struct {
    Name     string `json:"name"`     // tmux session name
    Runtime  string `json:"runtime"`  // detected runtime (claude, gemini, codex, etc.)
    WorkDir  string `json:"workDir"`  // pane working directory
    Attached bool   `json:"attached"` // session attached status
}
```

Removed fields:
- `Role` — this was GT-specific (witness, crew, refinery, etc.). In a generic agent detector, there's no concept of "role" — the runtime IS the identity.
- `Rig` — this was GT-specific (rig name). No equivalent in standalone mode.

**Impact on downstream consumers**: Both dashboards and WebSocket protocols reference `agent.Role` and `agent.Rig`. These need to be removed from:
- `wsadapter/server.go` — agent event JSON
- `wsconv/server.go` — agent info JSON
- `adapter.html` — role badges
- `converter.html` — agent display

The dashboards can show the runtime as a badge instead of the role. The session name is the primary identifier.

#### Edge Cases

1. **Multiple agent runtimes in one session**: A session with both a Claude Code process and a Gemini process (unlikely but possible with multiple panes). `DetectRuntime` returns the first match per the priority list. The registry tracks per-session, not per-pane, so only one agent is reported. If multi-pane support is needed later, the registry would need to key by `session:pane` instead of just `session`. **For now**: one agent per session is sufficient.

1a. **Multi-pane detection limitation** (Codex/Gemini finding): `GetPaneInfo` (`commands.go:76`) takes only the first pane returned by `list-panes`. A user with `vim` in Pane 0 and `claude` in Pane 1 won't have the agent detected. **Mitigation**: For now, document this limitation. Future enhancement: use `list-panes -a -F ...` to scan ALL panes across all sessions in a single tmux command, which also addresses the N+1 query pattern (one `list-panes` per session during scan).

1b. **Unsupported runtime follow behavior** (Codex finding #7): The converter only registers a Claude conversation discoverer/parser. When a user follows a Gemini or Codex agent, `EnsureTailing` calls `startWatching`, which checks `w.discoverers[agent.Runtime]` — if no discoverer is registered, it logs a warning and returns without starting any tailing. The follow succeeds (the agent exists), but no conversation events are ever emitted. **The spec must define this**: the `follow-agent` response should include a `conversationSupported: false` field when the agent's runtime has no registered parser, so the dashboard can show "Agent detected but conversation streaming not available for this runtime" instead of an indefinite spinner.

2. **Many non-agent sessions**: A user with 50 tmux sessions but only 2 running agents. The scan checks all 50 but `InferRuntime` quickly returns empty for non-agent sessions (pane command doesn't match any known process). Cost: O(S × R) string comparisons, completing in <1ms total. If descendant walking is needed (shell-wrapped agents), that's O(S × D) where D = process tree depth, but `pgrep` exits fast for shells with no agent descendants.

3. **Session name with regex-special characters**: Session names like `my.project` or `test[1]` are valid tmux names. These are agent *names*, not filter patterns — no regex escaping needed here.

3a. **Startup event channel pressure** (Codex finding #8): The Registry events channel has a 100-event buffer (`make(chan RegistryEvent, 100)`). With the IsGastownSession gate removed, the first scan may detect many more agents than before. If the scan emits >100 `added` events synchronously (before the ConversationWatcher's watchLoop drains them), `scan()` will block on channel send. **Mitigation**: The events are sent outside the scan lock (`registry.go:220-223`), so the watcher CAN drain while scan sends. However, if the watcher is slow (e.g., doing eager tailing — which we're removing), it could block. With lazy tailing, the watcher's event handler just calls `recordAgent()` (a map write), which is fast. The 100-event buffer is sufficient for typical workloads (users rarely have >100 agent sessions). If needed, increase to 500.

4. **Runtime change in same session** (Gemini finding): If a user kills `claude` and starts `gemini` in the same tmux session, the session name stays constant. The current diff logic only checks `Attached` status for updates. **Fix**: The diff in `scan()` must also compare `oldAgent.Runtime != newAgent.Runtime` (and `oldAgent.WorkDir != newAgent.WorkDir`) to emit an `updated` event when the runtime changes. This ensures the dashboard reflects the correct runtime badge.

4a. **DetectRuntime ambiguity**: A process named `node` could be Claude Code or OpenCode (both list `node` as a process name). `DetectRuntime` resolves this deterministically via `runtimePriority` — `claude` is checked before `opencode`, so `node` always matches `claude` first. If disambiguation is needed, `CheckProcessBinary` checks the actual binary path (e.g., `/path/to/claude` vs `/path/to/opencode`). For most real-world scenarios, the priority order is correct.

#### Acceptance Criteria

- [ ] `IsGastownSession()` is deleted from detect.go
- [ ] `ParseSessionName()` is deleted from detect.go
- [ ] GT env var reading (`GT_AGENT`, `GT_ROLE`, `GT_RIG`) is removed from registry.go
- [ ] `Role` and `Rig` fields removed from Agent struct
- [ ] `scan()` uses `DetectRuntime()` to detect agent runtime across all known runtimes (ordered, deterministic)
- [ ] `DetectRuntime()` returns `""` for non-agent sessions (no false-positive `"claude"` default)
- [ ] `scan()` diff detects runtime AND workdir changes in same session (emits "updated" event)
- [ ] Running the server without `--work-dir` scans all sessions
- [ ] Running the server with `--work-dir ~/gt` still filters by workdir
- [ ] Any tmux session running a known agent process appears in `list-agents`
- [ ] Existing tests updated to reflect removed functions and fields

---

### 3.2 CLI Flag Changes

#### main.go (adapter)

```go
// BEFORE:
gtDir := flag.String("gt-dir", filepath.Join(os.Getenv("HOME"), "gt"), "gastown town directory")

// AFTER:
workDir := flag.String("work-dir", "", "optional working directory filter — only track agents under this path (empty = all)")
```

Rename the flag from `--gt-dir` to `--work-dir` to reflect its general-purpose nature. It's not Gastown-specific anymore — it's just an optional workdir prefix filter. Keep `--gt-dir` as a hidden alias for backward compatibility.

#### cmd/tmux-converter/main.go (converter)

Same change — rename flag, default to `""`, hidden alias.

#### Internal naming

Rename `gtDir` field to `workDirFilter` throughout:
- `Registry.gtDir` → `Registry.workDirFilter`
- `Adapter.gtDir` → `Adapter.workDirFilter`
- `Converter.gtDir` → `Converter.workDirFilter`
- `converter.New(gtDir, ...)` → `converter.New(workDirFilter, ...)`
- `adapter.New(gtDir, ...)` → `adapter.New(workDirFilter, ...)`

#### Acceptance Criteria

- [ ] Flag renamed from `--gt-dir` to `--work-dir` with `--gt-dir` as hidden alias
- [ ] `--work-dir` defaults to empty string in both binaries
- [ ] Omitting `--work-dir` causes registry to scan all sessions
- [ ] Passing `--work-dir ~/gt` restricts to that workdir prefix (backward compatible)
- [ ] Internal field renamed from `gtDir` to `workDirFilter`

---

### 3.3 Lazy ConversationWatcher with Ref-Counted Tailing

#### Current Behavior (watcher.go)

```
agent added → startWatching(agent) → discoverAndTail(agent) → file watcher + JSONL tailer
```

This is **eager** — every detected agent gets file watchers immediately.

#### New Behavior

```
agent added → record agent exists (no tailing)
client follows agent → EnsureTailing(agentName) → discoverAndTail if not already active
client unfollows → ReleaseTailing(agentName) → stop tailing if ref count reaches 0
```

#### New Methods on ConversationWatcher

```go
// EnsureTailing starts tailing for an agent if not already active.
// Returns immediately if tailing is already running.
// Increments reference count.
func (w *ConversationWatcher) EnsureTailing(agentName string) error

// ReleaseTailing decrements the reference count for an agent.
// If count reaches 0, stops tailing after a grace period.
func (w *ConversationWatcher) ReleaseTailing(agentName string)
```

#### Internal State

```go
type tailingState struct {
    refCount    int
    cancelFunc  context.CancelFunc  // cancels the discoverAndTail goroutine
    graceTimer  *time.Timer         // grace period before cleanup
}

// Added to ConversationWatcher struct:
tailing    map[string]*tailingState  // agentName → tailing state
tailingMu  sync.Mutex
```

#### Algorithm: EnsureTailing

```
EnsureTailing(agentName):
    lock tailingMu
    if state exists for agentName:
        if graceTimer is running:
            stop graceTimer (cancel pending cleanup)
        state.refCount++
        unlock
        return nil

    // Agent must be known (in the registry)
    agent := lookup agent from registry
    if agent not found:
        unlock
        return error("agent not found")

    ctx, cancel := context.WithCancel(w.ctx)
    state := &tailingState{
        refCount:   1,
        cancelFunc: cancel,
    }
    w.tailing[agentName] = state
    unlock

    // Start discovery and tailing in background.
    // IMPORTANT (Claude R3 F49, F56): discoverAndTail MUST propagate this
    // per-agent ctx to startConversationStream and retryDiscovery, NOT use
    // w.ctx. When the tailing is cancelled (grace timer or agent removal),
    // this ctx cancels all streams and retry goroutines for this agent.
    go w.discoverAndTail(ctx, agent)
    return nil
```

#### Algorithm: ReleaseTailing

```
ReleaseTailing(agentName):
    lock tailingMu
    state := w.tailing[agentName]
    if state == nil:
        unlock
        return

    state.refCount--
    if state.refCount <= 0:
        // Start grace period (30 seconds)
        state.graceTimer = time.AfterFunc(30*time.Second, func() {
            w.tailingMu.Lock()
            // IMPORTANT: time.Timer.Stop() may return false if the callback
            // is already executing. This re-check of refCount is the actual
            // correctness guarantee, not the Stop() call in EnsureTailing.
            // (Claude R2 finding F2)
            if state.refCount > 0 {
                w.tailingMu.Unlock()
                return  // re-subscribed during grace period — bail out
            }
            state.cancelFunc()
            delete(w.tailing, agentName)
            w.tailingMu.Unlock()
            // Clean up streams OUTSIDE tailingMu to avoid lock-order
            // inversion with watcher.mu. Lock ordering invariant:
            //   watcher.mu → tailingMu (never reverse)
            // cleanupAgent acquires watcher.mu, so it MUST NOT be called
            // while holding tailingMu. (Codex R2 #5, Claude R2 F9)
            w.cleanupAgent(agentName)
        })
    unlock
```

#### Changes to watchLoop

```go
// BEFORE (in watchLoop):
case event := <-registryEvents:
    if event.Type == "added":
        w.startWatching(event.Agent)  // eager tailing
    }

// AFTER:
case event := <-registryEvents:
    if event.Type == "added":
        w.recordAgent(event.Agent)  // just store metadata, no tailing
    }
```

New `recordAgent` method just stores the agent info so `EnsureTailing` can look it up later. `recordAgent` also updates the agent info on "updated" events (e.g., runtime change) to keep the recorded state consistent with the registry (Codex R2 #9).

**Watcher Stop() must clean up tailing state** (Claude R2 F12): The current `Stop()` (watcher.go lines 142-159) acquires `w.mu` but does not touch the new `tailing` map. Grace timers scheduled via `time.AfterFunc` will fire after shutdown, potentially accessing cleaned-up streams. `Stop()` must:
```
Stop():
    // ... existing cleanup ...
    w.tailingMu.Lock()
    for agentName, state := range w.tailing {
        if state.graceTimer != nil {
            state.graceTimer.Stop()
        }
        state.cancelFunc()
        delete(w.tailing, agentName)
    }
    w.tailingMu.Unlock()
```

#### Wiring into WebSocket Server

**conv→agent mapping**: The ConversationWatcher needs a reverse lookup from conversationID to agentName. This already exists implicitly via `conversationStream.agent`, but we also need it for lazy tailing. Add `convToAgent map[string]string` (conversationID → agentName) to ConversationWatcher, populated in `startConversationStream` and cleaned up in `stopWatching`/`cleanupAgent`.

**Conversation switch cleanup** (Codex R2 #7): When a conversation rotates (agent starts a new JSONL file), the old conversationID's stream is stopped and a new one starts. The `convToAgent` entry for the old ID must be removed to avoid stale mappings. However, the primary lookup path now uses `extractAgentFromConvID()` (parsing the ID format) rather than the `convToAgent` map, so stale entries are less dangerous — they just waste memory. Still, `startConversationStream` should remove the old mapping when replacing a conversation for the same agent.

**Dead code: `GetProcessNames`** (Claude R2 F10): After deGasification removes GT_AGENT env var usage from `scan()`, the `GetProcessNames(agentName)` function is no longer called. Its fallback to `runtimeProcessNames["claude"]` for unknown agent names is the exact false-positive behavior we're eliminating. Mark for removal or change fallback to return `nil`.

In `wsconv/server.go`, the `follow-agent`, `subscribe-conversation`, and cleanup handlers call `EnsureTailing`/`ReleaseTailing`:

```go
// In handleFollowAgent (line ~383):
//
// CRITICAL: Release the old follow's tailing ref BEFORE acquiring a new one.
// The converter client re-sends follow-agent when filters change or the user
// clicks a different agent. Without releasing the old ref, refcounts drift
// upward and tailing never stops. (Codex R1 finding #2, corrected in R2)
//
// IMPORTANT: Only release the ref for the SAME agent being re-followed.
// Do NOT release refs for other agents — the protocol explicitly allows
// multiple simultaneous follow-agent subscriptions for different agents
// (specs/tmux-converter.md:721). Releasing other follows would under-count
// refs and prematurely stop tailing for still-active subscriptions.
// Use explicit `unsubscribe-agent` to stop following other agents.
//
if oldFollow, ok := c.follows[msg.Agent]; ok {
    // Same agent re-follow — release+reacquire to keep count accurate
    c.server.watcher.ReleaseTailing(oldFollow.agentName)
    // Also clean up old subscription state (cancel goroutine, unsubscribe buffer)
    if oldBuf := c.server.watcher.GetBuffer(oldFollow.conversationID); oldBuf != nil {
        oldBuf.Unsubscribe(oldFollow.bufSubID)
    }
    if oldFollow.cancel != nil {
        oldFollow.cancel()
    }
    delete(c.follows, msg.Agent)
}

// Call EnsureTailing BEFORE checking GetActiveConversation.
// EnsureTailing starts async discovery — the active conversation
// won't be available immediately. That's fine: the existing
// "pending follow" mechanism (lines 410-427) handles this.
// When discoverAndTail completes, it emits conversation-started,
// and deliverConversationStarted (lines 576-615) picks up
// the pending follow and wires up the subscription.
if err := c.server.watcher.EnsureTailing(msg.Agent); err != nil {
    c.sendJSON(serverMessage{ID: msg.ID, Type: "error", Error: err.Error()})
    return
}
// ... rest of existing follow-agent logic (unchanged) ...

// In handleUnsubscribe / handleUnsubscribeAgent:
// After existing cleanup, release the tailing ref:
c.server.watcher.ReleaseTailing(sub.agentName)

// In client disconnect cleanup (removeClient):
//
// CRITICAL (all three R3 reviewers): follow-agent subscriptions are stored
// in BOTH c.follows AND c.subs (server.go lines 462-463). Iterating both
// maps double-releases the tailing ref. Fix: iterate ONLY c.subs (which
// is the superset containing both follow-agent and subscribe-conversation
// subscriptions). Do NOT iterate c.follows for ReleaseTailing.
//
for _, sub := range c.subs {
    if sub.agentName != "" {
        c.server.watcher.ReleaseTailing(sub.agentName)
    }
}
// Clean up pending conv subs (never completed — no ref to release,
// but EnsureTailing was called, so release those refs too):
for _, pending := range c.pendingConvSubs {
    if pending.agentName != "" {
        c.server.watcher.ReleaseTailing(pending.agentName)
    }
}
```

**subscribe-conversation handler** (Codex R1 finding #3, expanded in R2):

`subscribe-conversation` takes only a `conversationId`, not an agent name. It needs lazy tailing integration, but has a **chicken-and-egg problem**: the `convToAgent` mapping is only populated when tailing starts (in `startConversationStream`), so it's empty for agents nobody is following yet.

**Solution**: Parse the conversationID directly. The ID format is `"runtime:agentName:nativeId"` (defined in `discovery.go:110`), so `strings.SplitN(convID, ":", 3)[1]` gives the agent name without needing the mapping. Fall back to `GetAgentForConversation` for non-standard IDs.

Additionally, since `EnsureTailing` is async (starts `discoverAndTail` in a goroutine), `GetBuffer(conversationID)` returns nil immediately. The handler needs a **pending subscription** mechanism similar to `follow-agent`'s pending-follow (lines 410-427):

```go
// In handleSubscribeConversation (line ~340):
// Extract agent name from conversationID format "runtime:agentName:nativeId"
agentName := extractAgentFromConvID(msg.ConversationID)
if agentName == "" {
    // Fallback: check convToAgent map (populated when tailing is active)
    agentName = c.server.watcher.GetAgentForConversation(msg.ConversationID)
}

if agentName != "" {
    if err := c.server.watcher.EnsureTailing(agentName); err != nil {
        c.sendJSON(serverMessage{ID: msg.ID, Type: "error", Error: err.Error()})
        return
    }
    // Track agent for cleanup on disconnect
    sub.agentName = agentName
}

buf := c.server.watcher.GetBuffer(msg.ConversationID)
if buf == nil && agentName != "" {
    // Reject duplicate pending requests for same conversationID (Codex R3 #2):
    // A second request overwrites the first, leaving the first unanswered.
    // Error the duplicate; the client can unsubscribe and re-subscribe.
    if _, exists := c.pendingConvSubs[msg.ConversationID]; exists {
        c.sendJSON(serverMessage{ID: msg.ID, Type: "error",
            Error: "already pending subscription for this conversation"})
        return
    }
    // Buffer not ready yet (EnsureTailing is async) — mark as pending.
    // When discoverAndTail completes and emits conversation-started,
    // deliverConversationStarted will check pending conv subscriptions
    // and wire them up, similar to how pending follows work.
    c.pendingConvSubs[msg.ConversationID] = &pendingConvSub{
        msgID:     msg.ID,
        agentName: agentName,
        filter:    msg.Filter,
        // Timeout: 30 seconds. If tailing doesn't produce this conversation
        // by then, send an error and clean up. (All three R3 reviewers)
        timer: time.AfterFunc(30*time.Second, func() {
            c.mu.Lock()
            pending, ok := c.pendingConvSubs[msg.ConversationID]
            if !ok {
                c.mu.Unlock()
                return  // already resolved
            }
            delete(c.pendingConvSubs, msg.ConversationID)
            c.mu.Unlock()
            c.server.watcher.ReleaseTailing(pending.agentName)
            c.sendJSON(serverMessage{ID: pending.msgID, Type: "error",
                Error: "conversation not found within timeout"})
        }),
    }
    return  // response sent when subscription binds (or times out)
}
// ... rest of existing subscribe-conversation logic (buf != nil case) ...

// In handleUnsubscribeConversation:
if sub.agentName != "" {
    c.server.watcher.ReleaseTailing(sub.agentName)
}
// Also clean up pending conv subs:
if pending, ok := c.pendingConvSubs[sub.conversationID]; ok {
    if pending.timer != nil {
        pending.timer.Stop()
    }
    delete(c.pendingConvSubs, sub.conversationID)
}
```

**`deliverConversationStarted` must check `pendingConvSubs`** (all three R3 reviewers):

The existing `deliverConversationStarted` (lines 576-615) only checks `c.follows` for pending follow-agent subscriptions. It must ALSO check `c.pendingConvSubs`:

```go
// In deliverConversationStarted, AFTER the existing c.follows check:
if pending, ok := c.pendingConvSubs[we.NewConvID]; ok {
    pending.timer.Stop()
    delete(c.pendingConvSubs, we.NewConvID)

    buf := c.server.watcher.GetBuffer(we.NewConvID)
    if buf == nil {
        c.sendJSON(serverMessage{ID: pending.msgID, Type: "error",
            Error: "conversation buffer not available"})
        c.server.watcher.ReleaseTailing(pending.agentName)
        return
    }

    filter := buildFilter(pending.filter)
    snapshot, bufSubID, live := buf.Subscribe(filter)
    sID := c.nextSubID()
    subCtx, subCancel := context.WithCancel(c.ctx)
    sub := &subscription{
        id:             sID,
        conversationID: we.NewConvID,
        agentName:      pending.agentName,
        bufSubID:       bufSubID,
        filter:         filter,
        live:           live,
        cancel:         subCancel,
    }
    c.subs[sID] = sub

    snapshot = capSnapshot(snapshot)
    cursor := makeCursor(we.NewConvID, snapshot)
    c.sendJSON(serverMessage{
        ID:             pending.msgID,
        Type:           "conversation-snapshot",
        SubscriptionID: sID,
        ConversationID: we.NewConvID,
        Events:         snapshot,
        Cursor:         cursor,
    })
    go c.streamLiveWithContext(sub, buf, subCtx)
}
```

**NOTE**: `pendingConvSubs` can only resolve for the **active** conversation (the most recent JSONL file). If the client requests a historical/inactive conversation ID, `deliverConversationStarted` fires with the active conversation's ID, which won't match the pending key. The pending sub will timeout after 30 seconds and the client receives an error. This is intentional — historical conversations require on-demand loading (future feature), not the lazy tailing mechanism.

**`GetAgentForConversation` method** (Claude R3 F57):
```go
// GetAgentForConversation returns the agent name for a conversation ID,
// or "" if the mapping is not known. Uses the convToAgent map populated
// during startConversationStream.
func (w *ConversationWatcher) GetAgentForConversation(convID string) string {
    w.mu.RLock()
    defer w.mu.RUnlock()
    return w.convToAgent[convID]
}
```

Helper function:
```go
// extractAgentFromConvID parses the conversation ID format "runtime:agentName:nativeId"
// (defined in discovery.go:110) to extract the agent name. Returns "" if the format
// is not recognized. Validates that parts[0] is a known runtime to avoid false
// positives on non-standard ID formats. (Claude R3 F44: robustness)
func extractAgentFromConvID(convID string) string {
    parts := strings.SplitN(convID, ":", 3)
    if len(parts) < 3 {
        return ""  // not the standard 3-part format
    }
    // Validate runtime component against known runtimes
    knownRuntimes := map[string]bool{
        "claude": true, "gemini": true, "codex": true,
        "cursor": true, "auggie": true, "amp": true, "opencode": true,
    }
    if !knownRuntimes[parts[0]] {
        return ""  // unknown runtime prefix — don't trust the parse
    }
    return parts[1]
}
```

**Key insight**: The existing pending-follow mechanism (`handleFollowAgent` lines 410-427 and `deliverConversationStarted` lines 576-615) already handles the async case where tailing hasn't started yet for `follow-agent`. For `subscribe-conversation`, a new `pendingConvSubs` map provides the same deferred-binding pattern. The critical additions are: (1) release-before-acquire on same-agent follow replacement only (not other agents), (2) pending subscription with 30-second timeout, (3) disconnect cleanup via `c.subs` only (not `c.follows` — avoids double-release), (4) `deliverConversationStarted` checks both `c.follows` and `c.pendingConvSubs`.

#### Edge Cases

1. **Agent removed while tailing**: Registry emits "removed" event. Watcher should cancel tailing regardless of ref count (agent is gone). Clients get `agent-removed` event. Any active subscriptions become invalid. **Important** (Codex finding #9): The existing `stopWatching()` only removes the active conversation for the agent via `activeByAgent`. It does NOT clean up subagent streams. The new `cleanupAgent(agentName)` must iterate ALL streams and stop any whose `stream.agent.Name == agentName`, including subagent conversations. Also clean up the `tailing` map entry and cancel any grace timer. The agent removal in `watchLoop` must call both `cleanupAgent` AND clear the tailing state:

    ```
    case "removed":
        w.cleanupAgent(event.Agent.Name)  // stops ALL streams for this agent
        w.cancelTailing(event.Agent.Name) // clears tailing state regardless of refcount
        w.emitEvent(WatcherEvent{Type: "agent-removed", Agent: &event.Agent})
    ```

2. **Rapid subscribe/unsubscribe**: Grace period (30 seconds) prevents thrashing. The timer is cancelled if a new subscriber arrives before it fires.

3. **Agent re-added after removal**: A session that stops and restarts an agent process. Registry emits "removed" then "added". Tailing state is cleaned up on removal. New tailing starts fresh if a client subscribes.

4. **Multiple conversations per agent**: `discoverAndTail` discovers all JSONL files. The "active" conversation (most recent) is tailed; inactive ones are noted but not tailed. Conversation rotation (new file created) is handled by the existing directory watcher within `discoverAndTail`.

5. **Client disconnect without clean unsubscribe**: The WebSocket server's client cleanup handler must call `ReleaseTailing` for all agents the client was following.

#### Acceptance Criteria

- [ ] New agent detection does NOT start file tailing
- [ ] `EnsureTailing` starts tailing on first subscriber
- [ ] `ReleaseTailing` stops tailing (after grace period) when last subscriber leaves
- [ ] Ref counting is correct: N subscribers → N releases needed before cleanup
- [ ] Grace period prevents re-scanning on rapid subscribe/unsubscribe cycles
- [ ] Agent removal cancels tailing regardless of ref count
- [ ] Client disconnect releases all tailing refs for that client
- [ ] Conversation events still stream correctly to subscribed clients
- [ ] Conversation rotation still works (new JSONL file detected and tailed)

---

### 3.4 WebSocket Protocol: Session Filter on subscribe-agents

#### Converter Protocol (wsconv/server.go)

**New fields on `subscribe-agents` request**:

```json
{
    "id": "msg-1",
    "type": "subscribe-agents",
    "includeSessionFilter": "^gt-",
    "excludeSessionFilter": ""
}
```

Both fields are optional. Empty string or absent = no filter (match all).

**Server behavior**:
1. Validate regex syntax with `regexp.Compile()`. On error, return:
   ```json
   {"id": "msg-1", "type": "subscribe-agents", "ok": false, "error": "invalid includeSessionFilter: ..."}
   ```
2. Store compiled `*regexp.Regexp` per client (on the client struct)
3. The initial `agents` array in the response is filtered
4. Subsequent `agent-added`/`agent-removed`/`agent-updated` broadcasts check the filter before sending to each client

**Filter logic** (pseudocode):
```
passesFilter(sessionName, includeRe, excludeRe):
    if includeRe != nil && !includeRe.MatchString(sessionName):
        return false
    if excludeRe != nil && excludeRe.MatchString(sessionName):
        return false
    return true
```

**Re-subscribing with new filter**:
- Client sends `subscribe-agents` again with new filter values
- Server replaces old filter, sends fresh `agents` array matching new filter
- Existing agent event subscriptions continue with new filter applied

**`list-agents` also filtered** (semantics clarified per Claude R2 F16):
- `list-agents` request accepts the same optional `includeSessionFilter`/`excludeSessionFilter` fields
- If absent, returns all agents (backward compatible)
- **IMPORTANT**: `list-agents` filters are **ephemeral** (per-request only). They do NOT update the persistent per-client filter used for broadcasts. Only `subscribe-agents` updates the stored broadcast filter. This prevents a `list-agents` call from silently changing what lifecycle events the client receives.

#### Adapter Protocol (wsadapter/server.go + handler.go)

Same extension — `subscribe-agents` and `list-agents` accept `includeSessionFilter`/`excludeSessionFilter`.

**Adapter broadcast layer change** (Codex finding #5): The current `BroadcastToAgentSubscribers(msg []byte)` sends pre-marshaled bytes to all subscribers — it cannot apply per-client filters because the agent name is embedded inside the marshaled JSON, not available as a parameter.

**Fix**: Change the signature to pass the agent name alongside the bytes:

```go
// BEFORE:
func (s *Server) BroadcastToAgentSubscribers(msg []byte)

// AFTER:
func (s *Server) BroadcastToAgentSubscribers(agentName string, msg []byte)
```

The broadcast loop then checks each client's filter before sending:

```go
for client := range s.clients {
    client.mu.Lock()
    subscribed := client.agentSub
    include := client.includeSessionFilter
    exclude := client.excludeSessionFilter
    client.mu.Unlock()

    if subscribed && passesFilter(agentName, include, exclude) {
        client.SendText(msg)
    }
}
```

The caller in `adapter.go` passes `event.Agent.Name` as the first argument. The `MakeAgentEvent` function in `handler.go` remains unchanged — it still produces `[]byte`. The filtering is done at the broadcast layer using just the agent name, not by re-parsing the JSON.

Similarly, `handleListAgents` and `handleSubscribeAgents` in `handler.go` need to filter the agent list per-client. The `Request` struct in `handler.go` gains the `IncludeSessionFilter`/`ExcludeSessionFilter` fields, and compiled regexes are stored on the `Client` struct (in `client.go`).

#### Data Structures

```go
// Added to converter client struct (wsconv/server.go):
type client struct {
    // ... existing fields ...
    includeSessionFilter *regexp.Regexp  // nil = no include filter
    excludeSessionFilter *regexp.Regexp  // nil = no exclude filter
}

// Added to adapter client struct (wsadapter/client.go):
// Same two fields
```

#### Edge Cases

1. **Invalid regex**: Return error response, don't update filter. Client keeps its previous filter (or no filter if first attempt).

2. **Empty string vs absent field**: Both mean "no filter". Guard code:
   ```go
   var compiled *regexp.Regexp
   if filter != "" {
       var err error
       compiled, err = regexp.Compile(filter)
       if err != nil { return err }
   }
   // compiled is nil when filter is empty → no filter, not regexp.MustCompile("")
   ```

3. **Backslash escaping in JSON**: Regex like `\d+` must be sent as `"\\d+"` in JSON. This is standard JSON string escaping — no special handling needed.

4. **Very expensive regex**: Go's `regexp` package uses RE2 (no backtracking), so catastrophic backtracking is impossible. No timeout needed.

5. **Concurrent filter update**: Client sends new `subscribe-agents` while events are being broadcast. **The filter fields must be protected by the client's existing mutex** (Codex R1 finding #6). In both `wsconv` and `wsadapter`, the client struct already has a `mu sync.Mutex`. Filter reads (in broadcast loops) and writes (in subscribe-agents handlers) must both hold this lock. In-flight broadcasts may use old or new filter — this is acceptable (eventual consistency within one event cycle).

   **NOTE** (Codex R2 #6): The current `subscribedAgents` flag in `wsconv` is written in request handling and read in `Broadcast` without consistently holding `c.mu`. The new filter fields MUST NOT copy this pattern. Both reads and writes of `includeSessionFilter`/`excludeSessionFilter` (and `subscribedAgents`) must hold `c.mu`. Verify this during implementation.

   **Lock ordering invariant** (Codex R2 #5, Claude R2 F9, updated R3): All locks must be acquired in this order to prevent deadlocks:
   ```
   server.mu → client.mu → watcher.mu → tailingMu
   ```
   **`registry.mu`** (Claude R3 F46/F54): The registry's `sync.RWMutex` is acquired independently by the registry's own scan loop and by `Registry.Count()`/`Registry.GetAgents()`. It is **not in the main chain** — it must never be held simultaneously with any of the above locks. All registry reads (`GetAgents`, `Count`, `GetAgent`) are standalone calls that acquire `registry.mu` atomically and release it before returning.

   Never acquire a lock that precedes a currently-held lock. Key paths verified:
   - Broadcast loop: holds `server.mu`, acquires `client.mu` for filter check — OK
   - Grace timer callback: acquires `tailingMu`, calls `cleanupAgent` which acquires `watcher.mu` — **VIOLATION** (tailingMu before watcher.mu). Fixed by releasing `tailingMu` before calling `cleanupAgent` (see ReleaseTailing pseudocode).
   - Handler code: holds `client.mu`, calls `EnsureTailing` which acquires `tailingMu` — OK (client.mu before tailingMu)
   - Follow re-subscribe (Codex R3 #4): holds `client.mu`, calls `ReleaseTailing` (acquires+releases `tailingMu`), then calls `GetBuffer` (acquires+releases `watcher.mu`). These are **sequential**, not nested — `tailingMu` is released before `watcher.mu` is acquired. Safe because the ordering invariant only constrains simultaneously-held locks. The only concern is the grace timer's AfterFunc, which is addressed above.

#### Acceptance Criteria

- [ ] `subscribe-agents` accepts optional `includeSessionFilter` and `excludeSessionFilter`
- [ ] Invalid regex returns error without changing filter
- [ ] Initial `agents` response is filtered
- [ ] `agent-added`/`agent-removed`/`agent-updated` broadcasts are filtered per client
- [ ] Absent or empty filter fields mean "no filter" (show all)
- [ ] Re-subscribing with new filter values replaces old filter
- [ ] `list-agents` also accepts filter fields
- [ ] Both converter and adapter protocols support this
- [ ] Existing clients (no filter fields) still work unchanged

---

### 3.5 Dashboard Filter UI

#### Layout

```
┌─────────────────────────────────────────┐
│ Include: [________________] [×]         │  ← Regex input + clear
│ Exclude: [________________] [×]         │  ← Regex input + clear
│ 5 of 12 agents                          │  ← Count (live via totalAgents)
├─────────────────────────────────────────┤
│ agent-1                                 │
│ agent-2                                 │
│ ...                                     │
└─────────────────────────────────────────┘
```

The filter bar sits above the agent list in the sidebar. It's always visible (not collapsible — the inputs are small enough). No preset buttons in the initial implementation — the include/exclude regex fields are sufficient. Presets can be added later if users commonly apply the same patterns.

#### Presets

No hardcoded presets in the core dashboards — the filter fields are generic. Users type their own regex patterns. Examples:

| Use Case | Include | Exclude |
|----------|---------|---------|
| All agents | (empty) | (empty) |
| Only GT sessions | `^(hq-\|gt-)` | (empty) |
| Non-GT sessions | (empty) | `^(hq-\|gt-)` |
| Specific project | `my-project` | (empty) |
| Only Claude | (empty) | (empty) + runtime filter (future) |

**No preset buttons in initial implementation** — the include/exclude regex fields are sufficient. Presets can be added later as a UI enhancement if users commonly apply the same patterns.

#### Behavior

1. **On input change** (debounced 300ms): Re-send `subscribe-agents` with new include/exclude values. On success, agent list updates. On regex error from server, show inline error below the offending input field (red text).

2. **On preset click**: Set include/exclude input values, immediately send `subscribe-agents` (no debounce for presets).

3. **On page load**: Read filter from `localStorage` key `tmux-adapter-filter` or `tmux-converter-filter` (JSON: `{include, exclude}`). If present, populate inputs and send filter with initial `subscribe-agents`.

4. **On filter change**: Write to `localStorage`.

5. **Agent count**: Show "N agents" (unfiltered) or "N of M agents" (filtered, where M = total on server). The server's `subscribe-agents` response includes `totalAgents` count alongside the filtered `agents` array.

#### Protocol Addition for Count

Add `totalAgents` field to `subscribe-agents` response:
```json
{
    "id": "msg-1",
    "type": "subscribe-agents",
    "ok": true,
    "agents": [...],
    "totalAgents": 12
}
```

This lets the dashboard show "5 of 12 agents" without a separate request.

**Live total updates** (Codex R1 finding #11, refined in R2): The `totalAgents` count becomes stale as agents come and go. Include `totalAgents` in every `agent-added` and `agent-removed` broadcast event so the dashboard can update the count in real-time without polling:
```json
{"type": "agent-added", "agent": {...}, "totalAgents": 13}
{"type": "agent-removed", "name": "old-session", "totalAgents": 11}
```

**CRITICAL: `totalAgents` is UNFILTERED** (Codex R2 #4, revised R3): The `totalAgents` count must always reflect the true total on the server, not the filtered count visible to the client. Since lifecycle broadcasts are filtered per-client (non-matching agents are skipped), a filtered client would never receive events for non-matching agents and could not update `M` in "N of M".

**Solution: Approach 2 — separate `agents-count` event** (Codex R3 #5, Claude R3 F48):

The original approach (1) — send ALL lifecycle events to all clients with `filtered: true` — was rejected because:
- It contradicts Section 3.4's filter-before-send semantics
- The adapter pre-marshals `msg []byte` and cannot vary `filtered` per client without per-client remarshal
- Sending full agent payloads to all clients negates the bandwidth savings of filtering

Instead, use approach (2): **send a tiny `agents-count` event to ALL clients whenever the total changes**, independent of the per-client filter. Lifecycle events (`agent-added`/`agent-removed`/`agent-updated`) remain filtered per-client:

```json
{"type": "agents-count", "totalAgents": 13}
```

The broadcast loop sends filtered lifecycle events per-client, plus an unconditional `agents-count` to everyone. The `agents-count` event is small (no agent payload) and cheap to marshal once.

For the adapter (pre-marshaled bytes), `agents-count` is marshaled once and sent to all clients without per-client variation. The existing per-client filter check applies only to lifecycle events.

To avoid the performance concern (Gemini R2 #4) of calling `registry.GetAgents()` (which allocates a full slice) inside the broadcast loop just to get a count, add a lightweight `Count()` method to `Registry`:
```go
func (r *Registry) Count() int {
    r.mu.RLock()
    defer r.mu.RUnlock()
    return len(r.agents)
}
```

#### CSS

The filter bar uses existing dashboard styling conventions:
- Same font, colors, spacing as the agent list
- Preset buttons: pill-shaped, same style as runtime badges
- Input fields: full-width, monospace font (regex), subtle border
- Error text: red, small, below the input
- Count: gray, small, right-aligned or below inputs

#### Edge Cases

1. **No agents match filter**: Show "0 agents" (or "0 of N agents") with a "No matching agents" message in the agent list area.

2. **Server disconnection**: Filter state preserved in UI. On reconnect, re-send `subscribe-agents` with current filter values.

3. **localStorage unavailable**: Graceful degradation — filter starts empty, no persistence. No error shown.

4. **Very long regex**: Input field scrolls horizontally. No max-length enforcement (server validates).

#### Acceptance Criteria

- [ ] Filter bar with include/exclude regex inputs appears in both dashboards
- [ ] Typing in inputs applies filter after 300ms debounce
- [ ] Invalid regex shows inline error from server response
- [ ] Agent count updates: "N agents" or "N of M agents"
- [ ] Filter persisted to localStorage, restored on page load
- [ ] Filter applied on reconnect after disconnection
- [ ] Clear (×) button resets individual input field

---

### 3.6 Agent Display in Dashboards (Post-deGasification)

With Role and Rig removed from the Agent struct, the dashboard display simplifies:

#### Dashboard Display

| Session Name | Runtime Badge | WorkDir |
|-------------|---------------|---------|
| `hq-mayor` | `claude` | `/Users/x/gt/...` |
| `gt-demo-witness` | `claude` | `/Users/x/gt/...` |
| `my-project` | `claude` | `/Users/x/code/my-project` |
| `research` | `gemini` | `/Users/x/code/research` |

Each agent card shows:
- **Session name** (primary text) — this is the agent's `Name`
- **Runtime badge** (colored pill) — `claude`, `gemini`, `codex`, etc.
- **Attached indicator** — if the tmux session is attached
- **WorkDir** (optional, in tooltip or secondary text) — useful for context

**Converter `agentInfo` struct update** (Claude R2 F11): The converter's `agentInfo` struct in `wsconv/server.go` (lines 769-773) currently only includes `Name`, `Runtime`, and `ConversationID`. It does NOT include `WorkDir` or `Attached`. For the dashboard to show attached status and working directory, `agentInfo` must be extended:
```go
type agentInfo struct {
    Name           string `json:"name"`
    Runtime        string `json:"runtime"`
    ConversationID string `json:"conversationId,omitempty"`
    WorkDir        string `json:"workDir"`
    Attached       bool   `json:"attached"`
}
```
Update `buildAgentList()` to populate these fields from the registry's `Agent` struct.

The adapter dashboard previously showed role badges (mayor, witness, crew, etc.). These are removed. The runtime badge replaces the role badge. GT-specific naming conventions are a Gastown concern, not an adapter concern — if Gastown wants richer metadata, it can add it at the application layer (e.g., a GT-specific dashboard that parses session names itself).

#### Acceptance Criteria

- [ ] Agent cards show session name + runtime badge
- [ ] No role or rig badges in dashboards
- [ ] Tooltip or secondary text shows working directory
- [ ] Attached status indicator remains

---

## 4. Implementation Phases

### Phase 1: deGasification (remove all GT-specific code)

**Deliverables**:
1. Delete `IsGastownSession()` from detect.go
2. Delete `ParseSessionName()` from detect.go
3. Remove GT env var reading (`GT_AGENT`, `GT_ROLE`, `GT_RIG`) from registry.go scan()
4. Remove `Role` and `Rig` fields from Agent struct
5. Replace `InferRuntime()` with `DetectRuntime()` — returns `""` for non-agents, uses ordered `runtimePriority`, includes descendant walking
6. Add `runtimePriority` ordered slice for deterministic runtime detection
7. Add `CollectDescendantNames()` and `matchesAny()` for optimized descendant detection (moved from Phase 2 — required by `DetectRuntime` Tier 2/3) (Claude R3 F50)
8. Update scan() diff to detect runtime changes (compare `oldAgent.Runtime != newAgent.Runtime`)
9. Rename `--gt-dir` to `--work-dir` (keep alias), default to empty
10. Rename `gtDir` → `workDirFilter` throughout internal packages
11. Update detect_test.go — remove Role/Rig from all Agent references, add DetectRuntime tests
12. Update wsadapter and wsconv JSON serialization (no more role/rig fields)
13. Update adapter.html and converter.html to not display role/rig badges
14. Update `specs/adapter-api.md` to reflect new Agent model (no role/rig)

**Acceptance**: `make check` passes. Running server without `--work-dir` shows all agent sessions. Agent list shows session name + runtime only.

### Phase 2: Lazy Tailing

**Deliverables**:
1. Add `EnsureTailing()` and `ReleaseTailing()` to ConversationWatcher
2. Add `tailingState` with ref counting and grace period
3. Change `watchLoop` to call `recordAgent()` instead of `startWatching()` on "added" events
4. Handle "removed" events by cancelling tailing regardless of ref count (fixes pre-existing subagent stream leak — Claude R2 F6)
5. Wire `EnsureTailing`/`ReleaseTailing` into wsconv/server.go follow/subscribe/unsubscribe handlers
6. Handle client disconnect cleanup — release refs via `c.subs` only (NOT `c.follows` — avoids double-release since follow subs are in both maps) (R3 fix)
7. Add `pendingConvSubs` map with 30-second timeout for deferred subscribe-conversation binding (R3 fix)
8. Update `deliverConversationStarted` to check `pendingConvSubs` in addition to `c.follows` (R3 fix)
9. Add `extractAgentFromConvID()` helper with runtime validation for parsing conversationID format (R3 fix)
10. Add `GetAgentForConversation()` method on ConversationWatcher (R3 fix)
11. Update watcher `Stop()` to clean up all grace timers and tailing state
12. Propagate per-agent context from `EnsureTailing` through `discoverAndTail` to `startConversationStream` and `retryDiscovery` — do NOT use `w.ctx` (R3 fix)
13. Remove or neutralize `GetProcessNames()` fallback (dead code after deGasification)

**Acceptance**: Agent list appears instantly. Conversation streaming starts only when clicking an agent. Unsubscribing from all agents for 30+ seconds stops tailing (verifiable via logs).

### Phase 3: WebSocket Protocol Filter

**Deliverables**:
1. Add `includeSessionFilter`/`excludeSessionFilter` to converter `subscribe-agents` handler
2. Add `includeSessionFilter`/`excludeSessionFilter` to adapter `subscribe-agents` handler
3. Add `includeSessionFilter`/`excludeSessionFilter` to `list-agents` handler (both)
4. Add `totalAgents` to subscribe-agents response
5. Add `agents-count` event broadcast to all clients on agent lifecycle changes (R3 fix — replaces `filtered: true` approach)
6. Filter agent event broadcasts per-client
7. Validate regex, return error on invalid patterns
8. Fix `subscribedAgents` race in wsconv: write must hold `c.mu` (Claude R3 F52, existing bug)

**Acceptance**: Sending `subscribe-agents` with `includeSessionFilter: "^gt-"` returns only GT agents. Sending without filters returns all agents. Invalid regex returns error. `agents-count` events update the total for all clients regardless of filter.

### Phase 4: Dashboard UI

**Deliverables**:
1. Filter bar HTML/CSS in both adapter.html and converter.html
2. Include/exclude input fields with clear buttons
3. Debounced filter application (300ms)
4. Agent count display ("N of M agents") with live updates via totalAgents in lifecycle events
5. localStorage persistence
6. Reconnect filter restoration
7. Inline error display for invalid regex

**Acceptance**: User can type regex, see filtered agent list. Filter survives page reload. Error feedback for bad regex.

---

## 5. Testing Strategy

### Unit Tests

**detect_test.go**:
- Remove tests for deleted `IsGastownSession()` and `ParseSessionName()` (both deleted in deGasification)
- Add test: `DetectRuntime` returns `""` for non-agent sessions (pane command is `bash`, no agent descendants)
- Add test: `DetectRuntime` returns `"claude"` for direct `claude` command match
- Add test: `DetectRuntime` returns `"claude"` for shell-wrapped Claude (Tier 2 descendant walk)
- Add test: `DetectRuntime` returns `"gemini"` for `gemini` command match
- Add test: `DetectRuntime` priority — `node` command resolves to `"claude"` not `"opencode"`
- Add test: `DetectRuntime` returns `"claude"` for plain `node` process via Tier 1 (known false positive — documents current behavior). Separately test that `node` with no agent descendants in Tier 2 (shell wrapping `node`) returns `"claude"` via descendant match. (Codex R2 #10: the R1 tests asked for both `node→claude` and `node→""` which contradicted; clarified: Tier 1 direct `node` match = `"claude"`, Tier 2 shell-with-no-descendants = `""`)
- Add test: Tier 2/3 descendant detection requires command-exec injection (mock `CollectDescendantNames`) since detection shells out to `pgrep`

**registry_test.go** (exists — update existing tests):
- Remove `TestScanEnvVarOverrides` (tests GT_AGENT/GT_ROLE/GT_RIG — deleted)
- Remove `TestScanNonGastownSessionsSkipped` (tests IsGastownSession — deleted)
- Update `TestGetAgent` — no more Role/Rig fields
- Add test: scan() with empty workDirFilter finds agents in any session
- Add test: scan() with workDirFilter restricts to matching workdirs
- Add test: scan() workDirFilter prefix collision — `/tmp/gt` does NOT match `/tmp/gt-other` (Claude R2 F14). Consider normalizing workDirFilter to end with `/` or using path-aware comparison
- Add test: scan() detects runtime change in same session (emits "updated")
- Add test: scan() handles many sessions without blocking (event channel capacity)

**watcher_test.go**:
- Test: EnsureTailing increments ref count
- Test: ReleaseTailing decrements ref count
- Test: ReleaseTailing with count=0 triggers cleanup after grace period
- Test: EnsureTailing during grace period cancels cleanup
- Test: Agent removal cancels tailing regardless of ref count

**wsconv filter tests** (NOTE: no wsconv tests exist today — these are all new):
- Test: subscribe-agents with includeFilter returns only matching agents
- Test: subscribe-agents with excludeFilter excludes matching agents
- Test: both filters together apply AND logic
- Test: invalid regex returns error without changing existing filter
- Test: empty/absent filters return all agents
- Test: re-subscribe replaces filter and returns fresh agent list
- Test: agent-added broadcast respects per-client filter
- Test: agent-removed broadcast respects per-client filter
- Test: totalAgents count is correct in subscribe response
- Test: agents-count event sent to all clients on lifecycle changes (R3)
- Test: follow-agent for unsupported runtime returns conversationSupported=false
- Test: subscribe-conversation with pending mechanism — subscription binds when tailing completes
- Test: subscribe-conversation for unfollowed agent — parses agent from convID, starts tailing
- Test: subscribe-conversation pending timeout — error after 30 seconds (R3)
- Test: subscribe-conversation duplicate pending for same convID — rejected with error (R3)
- Test: subscribe-conversation with historical/inactive convID — times out (R3)
- Test: client disconnect releases refs via c.subs only (not c.follows — avoids double-release) (R3)
- Test: extractAgentFromConvID rejects unknown runtime prefixes (R3)
- Test: subscribedAgents write holds c.mu (R3)

**wsadapter filter tests** (currently only key-sequence tests exist):
- Test: subscribe-agents with includeFilter returns filtered agent list
- Test: BroadcastToAgentSubscribers filters per-client
- Test: list-agents with filter returns filtered results

**Concurrency/race tests** (Codex finding #13):
- Test: 100 concurrent subscribe/unsubscribe to same agent — refcount stays correct
- Test: filter update concurrent with broadcast — no panic, no data race
- Test: agent removal during active tailing — cleanup completes without deadlock
- Test: follow-agent replacement rapid-fire — refcounts don't leak

### Integration Tests

- Start server without `--gt-dir`, verify standalone agents detected
- Start server with `--gt-dir`, verify only GT agents detected
- Connect two clients with different filters, verify each sees correct agents
- Subscribe to agent, verify conversation streaming starts
- Unsubscribe, wait 30+ seconds, verify tailing stops (check logs)

### Manual Testing

- Run `claude` in a non-GT tmux session
- Open dashboard, verify agent appears
- Apply Gastown filter, verify standalone agent disappears
- Apply Standalone filter, verify GT agents disappear
- Click All, verify both appear
- Refresh page, verify filter persists
- Type invalid regex, verify error shown

---

## 6. Formal Properties and Invariants

### Session Filter Correctness

The session filter predicate `P(name, includeRe, excludeRe)` must satisfy:

```
P(name, nil, nil) = true                                    ∀ name    (no filter = all pass)
P(name, R_i, nil) = R_i.Match(name)                         ∀ name    (include only)
P(name, nil, R_e) = ¬R_e.Match(name)                        ∀ name    (exclude only)
P(name, R_i, R_e) = R_i.Match(name) ∧ ¬R_e.Match(name)     ∀ name    (both)
```

Where `R_i` and `R_e` are compiled RE2 regular expressions (guaranteed O(n) matching, no backtracking).

### Ref-Counting Invariant for Lazy Tailing

For each agent `a`, let `refCount(a)` be the tailing reference count. The following invariants must hold:

```
refCount(a) ≥ 0                              ∀ a, ∀ t     (non-negative)
refCount(a) = |{c ∈ clients : c.subs_has_agent(a) ∨ c.pending_conv(a)}|  (count = subs + pending conv)
// NOTE: c.subs is the superset containing both follow-agent and subscribe-conversation subs.
// Do NOT double-count via c.follows — follows are already in c.subs. (R3 fix)
tailing_active(a) ⟺ refCount(a) > 0 ∨ grace_timer(a)      (tailing iff refs or grace period)
```

The grace period creates a brief window where `refCount(a) = 0` but tailing continues:

```
grace_timer(a) fires ∧ refCount(a) = 0 → stop_tailing(a)   (cleanup after grace)
EnsureTailing(a) ∧ grace_timer(a) active → cancel_timer(a)  (re-subscribe cancels cleanup)
follow_replace(c, a, a) → Release(a) ; Ensure(a)  (same-agent re-follow: release+reacquire)
// NOTE: follow(c, a_new) where a_new ≠ a_old does NOT release a_old.
// Multiple simultaneous follows for different agents are allowed (tmux-converter.md:721).
// Use explicit unsubscribe-agent to release a_old.
agent_removed(a) → cancel_tailing(a) ∧ refCount(a) := 0    (force cleanup on removal)
```

### Scan Complexity

Let S = total tmux sessions, R = known runtimes (|runtimePriority| = 7), A = detected agents.

```
Time complexity per scan:  O(S × R)  worst case (check all runtimes per session)
                           O(S)      typical  (direct command match exits early)
Space complexity:          O(A)      (one Agent struct per detected agent)
Event complexity per scan: O(A)      (at most one add/remove/update per agent)
```

### Filter Broadcast Complexity

Let C = connected clients, A = agents with events.

```
Broadcast cost per event: O(C)                     (check each client's filter)
Filter check per client:  O(|name|)                (RE2 match is linear in input length)
Total per scan cycle:     O(A × C × max|name|)     (all events × all clients × filter check)
```

---

## 7. Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Scanning many sessions is slow | Low | Low | DetectRuntime filters quickly; optimized single-pgrep-per-session |
| Lazy tailing race conditions | Medium | Medium | Mutex protection, grace period, lock ordering invariant documented |
| Regex DoS via WebSocket | Very Low | Low | Go RE2 has no backtracking; no timeout needed |
| Breaking existing adapter consumers | High | Medium | Intentional — deGasification removes role/rig. Update adapter-api.md. No version bump (clean break) |
| Client disconnect without cleanup | Medium | Low | Disconnect handler releases refs via c.subs only (not c.follows — avoids double-release) |
| Pending conv sub leak | Medium | Medium | 30-second timeout + duplicate rejection prevents indefinite resource pinning |
| `node` false positives | Medium | Low | Plain Node.js processes match as "claude". Future fix: check command-line args. For now, harmless (session shows up, no conversation events) |
| Tier 3 transitive false positives | Medium | Low | Non-agent processes (e.g., Python) with Node.js children match as "claude". Future: restrict Tier 3 descendants to version-as-argv[0] patterns |
| Startup event pressure with many sessions | Low | Low | Lazy tailing makes watcher fast to drain events; 100-event buffer sufficient |
| Refcount leak on follow replacement | Medium | High | Same-agent-only release in handleFollowAgent; explicit unsubscribe for others |
| Subagent stream leak on agent removal | Medium | Medium | cleanupAgent iterates ALL streams for agent (also fixes pre-existing bug) |
| Lock-order deadlock in cleanup path | Medium | High | Documented invariant: server.mu → client.mu → watcher.mu → tailingMu. Grace timer releases tailingMu before cleanupAgent |
| Watcher shutdown with pending grace timers | Low | Medium | Stop() iterates tailing map, stops all timers, cancels all contexts |

---

## 8. Multi-Model Review Findings

This spec was reviewed by three independent models across three rounds: Codex CLI (gpt-5.3-codex), Gemini CLI (gemini-2.5-pro), and a Claude sub-agent. All findings have been incorporated into the spec above. This section documents the provenance of each fix.

### Round 1 Findings

| # | Finding | Severity | Reviewer(s) | Resolution |
|---|---------|----------|-------------|------------|
| 1 | `InferRuntime` returns `"claude"` default — every session becomes an agent | Critical | All three | Replaced with `DetectRuntime` returning `""` (Section 3.1) |
| 2 | Go map iteration non-determinism in `InferRuntime` | Critical | Gemini, Codex | Added `runtimePriority` ordered slice (Section 3.1) |
| 3 | Lazy tailing refcount leak on follow-agent replacement | Critical | Codex | Release-before-acquire pattern (Section 3.3) |
| 4 | `subscribe-conversation` not covered by lazy tailing | Critical | Codex | Added conv→agent mapping and EnsureTailing in handler (Section 3.3) |
| 5 | Back-compat claim overstated (removing role/rig is breaking) | Critical | Codex | Updated Goals and Non-Goals to acknowledge breaking change (Sections 1, 2) |
| 6 | Adapter `BroadcastToAgentSubscribers` can't filter per-client | High | Claude, Codex | Changed signature to pass agentName alongside bytes (Section 3.4) |
| 7 | wsconv concurrent state race with filter fields | High | Codex | Filter fields protected by existing client mutex (Section 3.4) |
| 8 | Unsupported runtime follow behavior undefined | High | Codex | Added `conversationSupported` field to follow-agent response (Section 3.1) |
| 9 | Startup event channel pressure | High | Codex | Analyzed channel capacity, lazy tailing makes drain fast (Section 3.1) |
| 10 | Subagent stream cleanup on agent removal | Medium | Codex | `cleanupAgent` iterates ALL streams for agent (Section 3.3) |
| 11 | Multi-pane detection limitation | Medium | Gemini, Codex | Documented limitation, future enhancement via `list-panes -a` (Section 3.1) |
| 12 | `totalAgents` stale after lifecycle events | Medium | Codex | Include `totalAgents` in lifecycle broadcast events (Section 3.5) |
| 13 | Spec contradictions (ParseSessionName tests, preset buttons) | Medium | Codex, Claude | Fixed test references, removed preset button contradiction (Sections 3.5, 5) |
| 14 | Test plan not aligned with risk surface | Medium | Codex | Added wsconv/wsadapter/concurrency test sections (Section 5) |
| 15 | Runtime change in same session not detected | Medium | Gemini | scan() diff now compares Runtime field (Section 3.1) |
| 16 | `node` false positives | Medium | Gemini | Documented limitation, future fix via command-line args (Section 3.1) |

### Round 2 Findings

Round 2 reviews found that Round 1 fixes introduced new issues and identified additional gaps. The most critical: the R1 release-before-acquire pattern over-released OTHER agents' follows (confirmed by all three reviewers), and subscribe-conversation's lazy tailing had a chicken-and-egg bootstrapping problem.

| # | Finding | Severity | Reviewer(s) | Resolution |
|---|---------|----------|-------------|------------|
| 17 | R1 fix #3 over-releases: loop releases ALL other follows, but protocol allows multi-agent follows | Critical | All three | Removed "release others" loop — only release same-agent re-follow (Section 3.3) |
| 18 | subscribe-conversation async failure: EnsureTailing async + GetBuffer nil + convToAgent empty | Critical | All three | Added pending conv sub mechanism + extractAgentFromConvID parser (Section 3.3) |
| 19 | Disconnect cleanup misses subscribe-conversation refs in c.subs | High | Codex, Claude | Added c.subs iteration in removeClient (Section 3.3) |
| 20 | Lock-order deadlock: grace timer holds tailingMu → cleanupAgent acquires watcher.mu | High | Codex, Claude | Release tailingMu before cleanupAgent; documented lock ordering invariant (Section 3.4) |
| 21 | totalAgents stale under filtering: filtered clients miss non-matching lifecycle events | High | Codex, Gemini | Send all events with `filtered` flag; add Registry.Count() (Section 3.5) |
| 22 | Grace timer Stop() is best-effort; re-check is the real safety net | High | Claude | Added explicit documentation in ReleaseTailing pseudocode (Section 3.3) |
| 23 | adapter.go and converter.go missing from Change Map | High | Claude | Added both files to Component Change Map (Section 2) |
| 24 | Watcher Stop() doesn't clean up grace timers or tailing state | High | Claude | Added tailing cleanup to Stop() (Section 3.3) |
| 25 | workDirFilter rename typo: spec renames to itself | Medium | Claude | Fixed line 275 and acceptance criterion (Section 3.2) |
| 26 | pgrep performance: 7 calls per shell session instead of 1 | Medium | All three | Refactored to CollectDescendantNames + matchesAny (Section 3.1) |
| 27 | Tier 3 transitive false positives from non-agent processes | Medium | Claude | Documented limitation, future: restrict to version-as-argv[0] (Section 3.1) |
| 28 | Follow replacement releases refs but doesn't clean up subscription state | Medium | Claude | Added buffer unsubscribe + cancel + delete in re-follow (Section 3.3) |
| 29 | convToAgent cleanup underspecified on conversation switch | Medium | Codex | Noted in conv→agent mapping section (Section 3.3) |
| 30 | GetProcessNames becomes dead code with claude fallback | Medium | Claude | Marked for removal (Section 3.3) |
| 31 | Converter agentInfo missing WorkDir/Attached for dashboard | Medium | Claude | Extended agentInfo struct (Section 3.6) |
| 32 | EnsureTailing agent source inconsistent (registry vs recordAgent) | Medium | Codex | recordAgent also handles "updated" events (Section 3.3) |
| 33 | subscribedAgents flag lacks consistent locking | Medium | Codex | Noted: new filter fields must use c.mu consistently (Section 3.4) |
| 34 | DetectRuntime test contradictions (node→claude vs node→"") | Medium | Codex | Clarified: Tier 1 node=claude, Tier 2 shell-no-descendants="" (Section 5) |
| 35 | WorkDir update not in acceptance criteria or Phase 1 bullets | Low | Codex | Fixed: acceptance says "runtime AND workdir changes" (Section 3.1) |
| 36 | workDirFilter prefix collision (e.g., /tmp/gt matches /tmp/gt-other) | Low | Claude | Added test case; consider trailing-slash normalization (Section 5) |
| 37 | list-agents filter semantics: ephemeral vs persistent unclear | Low | Claude | Clarified: list-agents is ephemeral, subscribe-agents is persistent (Section 3.4) |
| 38 | Empty string filter guard code not shown | Low | Claude | Added guard snippet (Section 3.4) |
| 39 | IsAgentProcess exact match brittle against future binary renames | Low | Claude | Documented as known limitation (Section 3.1) |
| 40 | Pre-existing subagent leak in stopWatching not flagged | Low | Claude | Phase 2 deliverable #4 now notes this fixes pre-existing bug (Section 4) |
| 41 | Server.mu → client.mu lock ordering needs explicit invariant | Medium | Claude | Added 4-level lock ordering documentation (Section 3.4) |

### Round 3 Findings

Round 3 reviews found that the R2 disconnect cleanup fix introduced a double-release bug (confirmed by all three reviewers), the pendingConvSubs mechanism was incomplete (no timeout, no deliverConversationStarted changes, no duplicate handling), and the `filtered: true` approach contradicted the filter-before-send semantics. The `CollectDescendantNames` description was also internally contradictory.

| # | Finding | Severity | Reviewer(s) | Resolution |
|---|---------|----------|-------------|------------|
| 42 | Disconnect cleanup double-releases: follow subs in BOTH c.follows AND c.subs | Critical | All three | Iterate only c.subs (superset); removed c.follows loop (Section 3.3) |
| 43 | pendingConvSubs has no timeout; deliverConversationStarted not updated | Critical | All three | Added 30s timeout, showed deliverConversationStarted changes, added duplicate rejection (Section 3.3) |
| 44 | extractAgentFromConvID fragile against future ID format changes | High | Claude | Requires 3-part format, validates runtime prefix against known runtimes (Section 3.3) |
| 45 | CollectDescendantNames "single pgrep" contradicts "recursive walk" | Medium | All three | Clarified: single tree-collection phase with O(depth) pgrep calls; savings from per-runtime dimension (Sections 3.1, helpers) |
| 46 | Lock ordering invariant missing registry.mu | High | Claude | Added registry.mu as independent lock not in main chain (Section 3.4) |
| 47 | EnsureTailing agent lookup source ambiguous (registry vs recordAgent) | Medium | Claude, Gemini | Clarified: EnsureTailing looks up from registry (Section 3.3) |
| 48 | `filtered: true` approach sends all events to all clients, negating bandwidth savings | Medium | Claude, Codex | Switched to approach 2: separate `agents-count` event (Section 3.5) |
| 49 | startConversationStream derives context from w.ctx, not per-agent context | Medium | Claude | Specified: propagate per-agent ctx through discoverAndTail (Section 3.3) |
| 50 | CollectDescendantNames in Phase 2 but required by Phase 1's DetectRuntime | Medium | Claude | Moved to Phase 1 deliverable #7 (Section 4) |
| 51 | subscribedAgents race not in any phase deliverable | Medium | Claude | Added to Phase 3 deliverable #8 (Section 4) |
| 52 | Follow re-subscribe path: ReleaseTailing → GetBuffer under client.mu | High | Codex, Claude | Analyzed: locks are sequential not nested — safe. Documented in lock ordering (Section 3.4) |
| 53 | pendingConvSubs not resolved by conversation-switched events; historical IDs can't match | Medium | Claude, Gemini | Documented: pending only works for active conversation; historical times out (Section 3.3) |
| 54 | `filtered: true` contradicts Section 3.4 filter-before-send + adapter pre-marshaled bytes | High | Codex | Switched to agents-count approach (Section 3.5) |
| 55 | retryDiscovery uses w.ctx instead of per-agent context | Low | Claude | Included in context propagation fix (Section 3.3) |
| 56 | GetAgentForConversation referenced but never defined | Low | Claude | Added method definition (Section 3.3) |
| 57 | pendingConvSubs keyed by convID: duplicate requests overwrite, leaking refs | Critical | Codex | Added duplicate rejection — error on second pending for same convID (Section 3.3) |
| 58 | Test coverage gaps: no tests for duplicate pending, malformed ID timeout, double-release dedupe | Medium | Codex | Added 6 new test cases (Section 5) |
| 59 | Legacy clients see all agents (bypass filter via ignored `filtered` flag) | Low | Gemini | Moot — switched to agents-count approach; legacy clients ignore count events (Section 3.5) |
| 60 | subscribe-conversation with historical/inactive conv IDs fails silently | Low | Gemini | Pending timeout sends explicit error after 30s (Section 3.3) |
| 61 | Converter Broadcast reads subscribedAgents without c.mu | High | Claude | Fixed: must hold c.mu in broadcast loop (Section 3.4, Phase 3) |
| 62 | CheckDescendants/checkDescendantsDepth become dead code after CollectDescendantNames | Low | Claude | Noted: remove alongside InferRuntime during Phase 1 (Section 4) |
