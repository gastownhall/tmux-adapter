package agents

import "testing"

func TestIsAgentProcess(t *testing.T) {
	tests := []struct {
		command string
		names   []string
		want    bool
	}{
		{"node", []string{"node", "claude"}, true},
		{"claude", []string{"node", "claude"}, true},
		{"codex-aarch64-a", []string{"codex"}, true},
		{"python", []string{"node", "claude"}, false},
		{"", []string{"node", "claude"}, false},
	}

	for _, tt := range tests {
		got := IsAgentProcess(tt.command, tt.names)
		if got != tt.want {
			t.Fatalf("IsAgentProcess(%q, %v) = %v, want %v",
				tt.command, tt.names, got, tt.want)
		}
	}
}

func TestIsShell(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"bash", true},
		{"zsh", true},
		{"sh", true},
		{"fish", true},
		{"tcsh", true},
		{"ksh", true},
		{"node", false},
		{"claude", false},
		{"", false},
	}

	for _, tt := range tests {
		got := IsShell(tt.command)
		if got != tt.want {
			t.Fatalf("IsShell(%q) = %v, want %v", tt.command, got, tt.want)
		}
	}
}

func TestDetectRuntime_Tier1_DirectMatch(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{"claude", "claude"},
		{"node", "claude"},
		{"gemini", "gemini"},
		{"codex", "codex"},
		{"codex-aarch64-a", "codex"},
		{"cursor-agent", "cursor"},
		{"auggie", "auggie"},
		{"amp", "amp"},
		{"opencode", "opencode"},
		{"bun", "opencode"},
	}

	for _, tt := range tests {
		got := DetectRuntime(tt.command, "")
		if got != tt.want {
			t.Fatalf("DetectRuntime(%q, \"\") = %q, want %q",
				tt.command, got, tt.want)
		}
	}
}

func TestDetectRuntime_NonAgent(t *testing.T) {
	tests := []struct {
		command string
	}{
		{"python"},
		{"vim"},
		{"htop"},
		{""},
	}

	for _, tt := range tests {
		got := DetectRuntime(tt.command, "")
		if got != "" {
			t.Fatalf("DetectRuntime(%q, \"\") = %q, want \"\"",
				tt.command, got)
		}
	}
}

func TestDetectRuntime_ShellWithoutPID(t *testing.T) {
	got := DetectRuntime("bash", "")
	if got != "" {
		t.Fatalf("DetectRuntime(\"bash\", \"\") = %q, want \"\"", got)
	}
}

func TestDetectRuntime_Priority(t *testing.T) {
	// "node" resolves to "claude" not "opencode" due to priority ordering
	got := DetectRuntime("node", "")
	if got != "claude" {
		t.Fatalf("DetectRuntime(\"node\", \"\") = %q, want \"claude\" (priority)", got)
	}
}

func TestMatchesAny(t *testing.T) {
	tests := []struct {
		descendants  []string
		processNames []string
		want         bool
	}{
		{[]string{"node", "python"}, []string{"node", "claude"}, true},
		{[]string{"python", "vim"}, []string{"node", "claude"}, false},
		{[]string{}, []string{"node", "claude"}, false},
		{[]string{"node"}, []string{}, false},
	}

	for _, tt := range tests {
		got := matchesAny(tt.descendants, tt.processNames)
		if got != tt.want {
			t.Fatalf("matchesAny(%v, %v) = %v, want %v",
				tt.descendants, tt.processNames, got, tt.want)
		}
	}
}

func TestRuntimePriority_CoversAllRuntimes(t *testing.T) {
	for runtime := range runtimeProcessNames {
		found := false
		for _, p := range RuntimePriority {
			if p == runtime {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("runtime %q is in runtimeProcessNames but not in RuntimePriority", runtime)
		}
	}

	for _, p := range RuntimePriority {
		if _, ok := runtimeProcessNames[p]; !ok {
			t.Fatalf("runtime %q is in RuntimePriority but not in runtimeProcessNames", p)
		}
	}
}

func TestDetectRuntime_Tier2_ShellWrappedClaude(t *testing.T) {
	old := collectDescendantNamesFunc
	collectDescendantNamesFunc = func(pid string) []string { return []string{"node"} }
	defer func() { collectDescendantNamesFunc = old }()

	got := DetectRuntime("bash", "12345")
	if got != "claude" {
		t.Fatalf("DetectRuntime(\"bash\", \"12345\") = %q, want \"claude\"", got)
	}
}

func TestDetectRuntime_Tier2_ShellWrappedGemini(t *testing.T) {
	old := collectDescendantNamesFunc
	collectDescendantNamesFunc = func(pid string) []string { return []string{"gemini"} }
	defer func() { collectDescendantNamesFunc = old }()

	got := DetectRuntime("bash", "12345")
	if got != "gemini" {
		t.Fatalf("DetectRuntime(\"bash\", \"12345\") = %q, want \"gemini\"", got)
	}
}

func TestDetectRuntime_Tier3_UnknownWithAgentDescendant(t *testing.T) {
	old := collectDescendantNamesFunc
	collectDescendantNamesFunc = func(pid string) []string { return []string{"claude"} }
	defer func() { collectDescendantNamesFunc = old }()

	got := DetectRuntime("2.1.38", "12345")
	if got != "claude" {
		t.Fatalf("DetectRuntime(\"2.1.38\", \"12345\") = %q, want \"claude\"", got)
	}
}

func TestDetectRuntime_Tier2_ShellNoAgentDescendants(t *testing.T) {
	old := collectDescendantNamesFunc
	collectDescendantNamesFunc = func(pid string) []string { return []string{"python", "vim"} }
	defer func() { collectDescendantNamesFunc = old }()

	got := DetectRuntime("bash", "12345")
	if got != "" {
		t.Fatalf("DetectRuntime(\"bash\", \"12345\") = %q, want \"\"", got)
	}
}

func TestCheckProcessBinaryNotFound(t *testing.T) {
	// PID 999999999 almost certainly doesn't exist
	got := CheckProcessBinary("999999999", []string{"node", "claude"})
	if got {
		t.Fatal("CheckProcessBinary with non-existent PID should return false")
	}
}

func TestCheckProcessBinaryEmptyPID(t *testing.T) {
	got := CheckProcessBinary("", []string{"node", "claude"})
	if got {
		t.Fatal("CheckProcessBinary with empty PID should return false")
	}
}

func TestDetectRuntimeUnknownProcess(t *testing.T) {
	// Mock descendants to return non-agent processes
	old := collectDescendantNamesFunc
	collectDescendantNamesFunc = func(pid string) []string { return []string{"python", "vim"} }
	defer func() { collectDescendantNamesFunc = old }()

	// "mystery-proc" is not a shell and not a known agent.
	// Tier 1: no match. Tier 2: not a shell. Tier 3: CheckProcessBinary
	// fails for non-existent PID, descendants don't match.
	got := DetectRuntime("mystery-proc", "999999999")
	if got != "" {
		t.Fatalf("DetectRuntime(\"mystery-proc\", \"999999999\") = %q, want \"\"", got)
	}
}

func TestDetectRuntime_NodeWrappedGeminiByArgs(t *testing.T) {
	oldRead := readProcessArgsFunc
	oldList := listChildPIDsFunc
	defer func() {
		readProcessArgsFunc = oldRead
		listChildPIDsFunc = oldList
	}()

	readProcessArgsFunc = func(pid string) (string, error) {
		switch pid {
		case "10":
			return "zsh -zsh", nil
		case "11":
			return "node /Users/csells/.nvm/versions/node/v22.21.1/bin/gemini", nil
		default:
			return "", nil
		}
	}
	listChildPIDsFunc = func(pid string) ([]string, error) {
		switch pid {
		case "10":
			return []string{"11"}, nil
		default:
			return nil, nil
		}
	}

	got := DetectRuntime("node", "10")
	if got != "gemini" {
		t.Fatalf("DetectRuntime(\"node\", \"10\") = %q, want \"gemini\"", got)
	}
}

func TestDetectRuntime_NodeWrappedClaudeByArgs(t *testing.T) {
	oldRead := readProcessArgsFunc
	oldList := listChildPIDsFunc
	defer func() {
		readProcessArgsFunc = oldRead
		listChildPIDsFunc = oldList
	}()

	readProcessArgsFunc = func(pid string) (string, error) {
		switch pid {
		case "20":
			return "zsh -zsh", nil
		case "21":
			return "node /usr/local/bin/claude", nil
		default:
			return "", nil
		}
	}
	listChildPIDsFunc = func(pid string) ([]string, error) {
		switch pid {
		case "20":
			return []string{"21"}, nil
		default:
			return nil, nil
		}
	}

	got := DetectRuntime("node", "20")
	if got != "claude" {
		t.Fatalf("DetectRuntime(\"node\", \"20\") = %q, want \"claude\"", got)
	}
}
