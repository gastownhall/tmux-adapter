package conv

import (
	"fmt"
	"reflect"
	"testing"
)

func TestFindFlagValue(t *testing.T) {
	tests := []struct {
		name string
		args []string
		flag string
		want string
	}{
		{
			name: "space separated",
			args: []string{"claude --resume 26a96967-588d-4c9b-a1b2-5b4eb8af29fd"},
			flag: "--resume",
			want: "26a96967-588d-4c9b-a1b2-5b4eb8af29fd",
		},
		{
			name: "equals separated",
			args: []string{"claude --resume=abc123"},
			flag: "--resume",
			want: "abc123",
		},
		{
			name: "missing",
			args: []string{"claude"},
			flag: "--resume",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findFlagValue(tt.args, tt.flag)
			if got != tt.want {
				t.Fatalf("findFlagValue(%v, %q) = %q, want %q", tt.args, tt.flag, got, tt.want)
			}
		})
	}
}

func TestFindCodexResumeID(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"codex resume sess-123 --yolo"}, "sess-123"},
		{[]string{"/opt/homebrew/bin/codex resume conv-777"}, "conv-777"},
		{[]string{"codex --yolo"}, ""},
	}

	for _, tt := range tests {
		got := findCodexResumeID(tt.args)
		if got != tt.want {
			t.Fatalf("findCodexResumeID(%v) = %q, want %q", tt.args, got, tt.want)
		}
	}
}

func TestCollectProcessTreeArgs(t *testing.T) {
	oldRead := readProcessArgsFunc
	oldList := listChildPIDsFunc
	defer func() {
		readProcessArgsFunc = oldRead
		listChildPIDsFunc = oldList
	}()

	argsByPID := map[string]string{
		"1": "zsh",
		"2": "claude --resume a1",
		"3": "node helper.js",
	}
	childrenByPID := map[string][]string{
		"1": {"2", "3"},
		"2": nil,
		"3": nil,
	}

	readProcessArgsFunc = func(pid string) (string, error) {
		if args, ok := argsByPID[pid]; ok {
			return args, nil
		}
		return "", fmt.Errorf("missing pid %s", pid)
	}
	listChildPIDsFunc = func(pid string) ([]string, error) {
		if children, ok := childrenByPID[pid]; ok {
			return children, nil
		}
		return nil, nil
	}

	got := collectProcessTreeArgs("1", 10)
	want := []string{"zsh", "claude --resume a1", "node helper.js"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectProcessTreeArgs() = %v, want %v", got, want)
	}
}

func TestResolveRuntimeSessionID(t *testing.T) {
	oldRead := readProcessArgsFunc
	oldList := listChildPIDsFunc
	defer func() {
		readProcessArgsFunc = oldRead
		listChildPIDsFunc = oldList
	}()

	readProcessArgsFunc = func(pid string) (string, error) {
		switch pid {
		case "10":
			return "zsh", nil
		case "11":
			return "claude --resume 26a96967-588d-4c9b-a1b2-5b4eb8af29fd", nil
		case "20":
			return "codex resume codex-sess-1 --yolo", nil
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

	if got := resolveRuntimeSessionID("claude", "10"); got != "26a96967-588d-4c9b-a1b2-5b4eb8af29fd" {
		t.Fatalf("resolveRuntimeSessionID(claude) = %q", got)
	}
	if got := resolveRuntimeSessionID("codex", "20"); got != "codex-sess-1" {
		t.Fatalf("resolveRuntimeSessionID(codex) = %q", got)
	}
	if got := resolveRuntimeSessionID("gemini", "10"); got != "" {
		t.Fatalf("resolveRuntimeSessionID(gemini) = %q, want empty", got)
	}
}
