package tools

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/torrischen/goat/agent/common"
)

func TestTerminalRunsCommandInWorkdir(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	result := Terminal().Execute(common.NewAgentContext(context.Background()), map[string]any{
		"command": "printf 'hello'; pwd",
		"workdir": workdir,
	})

	if !strings.Contains(result.String(), "Process exited with code 0") {
		t.Fatalf("result = %q, want successful exit", result.String())
	}
	if !strings.Contains(result.String(), "hello"+workdir) &&
		!strings.Contains(result.String(), "hello\n"+workdir) {
		t.Fatalf("result = %q, want command output and workdir", result.String())
	}
}

func TestTerminalReturnsNonZeroExitAndStderr(t *testing.T) {
	t.Parallel()

	result := Terminal().Execute(common.NewAgentContext(context.Background()), map[string]any{
		"command": "printf 'failure' >&2; exit 7",
	})

	if !strings.Contains(result.String(), "Process exited with code 7") {
		t.Fatalf("result = %q, want exit code 7", result.String())
	}
	if !strings.Contains(result.String(), "failure") {
		t.Fatalf("result = %q, want stderr", result.String())
	}
}

func TestTerminalTimesOut(t *testing.T) {
	t.Parallel()

	result := Terminal().Execute(common.NewAgentContext(context.Background()), map[string]any{
		"command":    "sleep 1",
		"timeout_ms": 20,
	})

	if !strings.Contains(result.String(), "Process timed out after 20 ms") {
		t.Fatalf("result = %q, want timeout", result.String())
	}
}

func TestTerminalSchema(t *testing.T) {
	t.Parallel()

	tool := Terminal()
	if tool.Name() != InternalToolShellCommand {
		t.Fatalf("tool.Name() = %q, want %q", tool.Name(), InternalToolShellCommand)
	}
	properties, ok := tool.Parameters()["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v, want map", tool.Parameters()["properties"])
	}
	for _, name := range []string{"command", "workdir", "timeout_ms", "max_output_bytes"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("missing property %q", name)
		}
	}
}

func TestTerminalOutputTruncationKeepsHeadAndTail(t *testing.T) {
	t.Parallel()

	output := "HEAD" + strings.Repeat("x", 2_000) + "TAIL"
	buffer := newCommandOutputBuffer(1_000)
	if _, err := buffer.Write([]byte(output[:800])); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := buffer.Write([]byte(output[800:])); err != nil {
		t.Fatalf("second write: %v", err)
	}
	truncated := buffer.String()
	if !strings.HasPrefix(truncated, "HEAD") {
		t.Fatalf("truncated output lost head: %q", truncated[:20])
	}
	if !strings.HasSuffix(truncated, "TAIL") {
		t.Fatalf("truncated output lost tail: %q", truncated[len(truncated)-20:])
	}
	if !strings.Contains(truncated, "bytes truncated") {
		t.Fatalf("truncated output missing marker")
	}
	if len(truncated) > 1_000 {
		t.Fatalf("len(truncated) = %d, want at most 1000", len(truncated))
	}
}

func TestTerminalRejectsMissingCommand(t *testing.T) {
	t.Parallel()

	result := Terminal().Execute(nil, map[string]any{
		"workdir": filepath.Clean("."),
	})
	if result.String() != "command parameter is missing or invalid." {
		t.Fatalf("result = %q", result.String())
	}
}

// Sandbox tests

func TestTerminalSandboxedRequiresLinux(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "linux" {
		t.Skip("This test is for non-Linux platforms")
	}

	result := TerminalSandboxed().Execute(common.NewAgentContext(context.Background()), map[string]any{
		"command": "echo hello",
	})

	if !strings.Contains(result.String(), "Sandbox mode is only supported on Linux") {
		t.Fatalf("result = %q, want Linux-only error", result.String())
	}
}

func TestTerminalSandboxedRequiresBubblewrap(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Sandbox mode is Linux-only")
	}

	// Use a non-existent bwrap path
	config := SandboxConfig{
		Enabled:   true,
		BwrapPath: "/nonexistent/bwrap",
	}

	result := TerminalWithSandbox(config).Execute(common.NewAgentContext(context.Background()), map[string]any{
		"command": "echo hello",
	})

	if !strings.Contains(result.String(), "bubblewrap") || !strings.Contains(result.String(), "not installed") {
		t.Fatalf("result = %q, want bubblewrap not installed error", result.String())
	}
}

func TestTerminalSandboxedBasicCommand(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Sandbox mode is Linux-only")
	}

	// Check if bubblewrap is available
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap not installed")
	}

	workdir := t.TempDir()
	result := TerminalSandboxed().Execute(common.NewAgentContext(context.Background()), map[string]any{
		"command": "echo 'sandbox test'",
		"workdir": workdir,
	})

	if !strings.Contains(result.String(), "Process exited with code 0") {
		t.Fatalf("result = %q, want successful exit", result.String())
	}
	if !strings.Contains(result.String(), "sandbox test") {
		t.Fatalf("result = %q, want command output", result.String())
	}
}

func TestTerminalSandboxedWorkdirAccess(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Sandbox mode is Linux-only")
	}

	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap not installed")
	}

	workdir := t.TempDir()
	result := TerminalSandboxed().Execute(common.NewAgentContext(context.Background()), map[string]any{
		"command": "pwd",
		"workdir": workdir,
	})

	if !strings.Contains(result.String(), "Process exited with code 0") {
		t.Fatalf("result = %q, want successful exit", result.String())
	}
	if !strings.Contains(result.String(), workdir) {
		t.Fatalf("result = %q, want workdir in output", result.String())
	}
}

func TestTerminalSandboxedNetworkIsolation(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Sandbox mode is Linux-only")
	}

	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap not installed")
	}

	// Without network access, ping should fail
	result := TerminalSandboxed().Execute(common.NewAgentContext(context.Background()), map[string]any{
		"command": "ping -c 1 8.8.8.8 || echo 'network blocked'",
	})

	// Should either fail or show network blocked message
	output := result.String()
	if !strings.Contains(output, "network blocked") && !strings.Contains(output, "Network is unreachable") {
		// Network might be blocked in a different way, just check it didn't succeed
		if strings.Contains(output, "1 received") {
			t.Fatalf("result = %q, network should be blocked", output)
		}
	}
}

func TestTerminalSandboxedCustomConfig(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Sandbox mode is Linux-only")
	}

	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap not installed")
	}

	workdir := t.TempDir()
	config := SandboxConfig{
		Enabled:       true,
		BwrapPath:     "bwrap",
		NetworkAccess: false,
		PreserveEnv:   []string{"USER"},
	}

	result := TerminalWithSandbox(config).Execute(common.NewAgentContext(context.Background()), map[string]any{
		"command": "echo test",
		"workdir": workdir,
	})

	if !strings.Contains(result.String(), "Process exited with code 0") {
		t.Fatalf("result = %q, want successful exit", result.String())
	}
}

func TestTerminalWithConfigDisabled(t *testing.T) {
	t.Parallel()

	// Config with Enabled=false should behave like Terminal()
	config := SandboxConfig{
		Enabled: false,
	}

	workdir := t.TempDir()
	result := TerminalWithConfig(config).Execute(common.NewAgentContext(context.Background()), map[string]any{
		"command": "echo 'no sandbox'",
		"workdir": workdir,
	})

	if !strings.Contains(result.String(), "Process exited with code 0") {
		t.Fatalf("result = %q, want successful exit", result.String())
	}
	if !strings.Contains(result.String(), "no sandbox") {
		t.Fatalf("result = %q, want command output", result.String())
	}
}

func TestBuildBubblewrapArgsBasic(t *testing.T) {
	t.Parallel()

	config := SandboxConfig{
		Enabled:   true,
		BwrapPath: "bwrap",
		TmpfsSize: "100M",
	}

	args, err := buildBubblewrapArgs("echo hello", "", config)
	if err != nil {
		t.Fatalf("buildBubblewrapArgs failed: %v", err)
	}

	// Check for essential arguments
	if !containsArg(args, "--unshare-all") {
		t.Fatalf("args = %v, want --unshare-all", args)
	}
	if !containsArg(args, "--die-with-parent") {
		t.Fatalf("args = %v, want --die-with-parent", args)
	}
	if !containsArg(args, "--unshare-net") {
		t.Fatalf("args = %v, want --unshare-net (network isolation)", args)
	}
	if !containsArg(args, "--") {
		t.Fatalf("args = %v, want -- separator", args)
	}
	if !containsArg(args, "echo hello") {
		t.Fatalf("args = %v, want command", args)
	}
}

func TestBuildBubblewrapArgsWithWorkdir(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	config := SandboxConfig{
		Enabled:   true,
		BwrapPath: "bwrap",
	}

	args, err := buildBubblewrapArgs("pwd", workdir, config)
	if err != nil {
		t.Fatalf("buildBubblewrapArgs failed: %v", err)
	}

	// Check workdir is bound
	if !containsArgPair(args, "--bind", workdir) {
		t.Fatalf("args = %v, want --bind %s", args, workdir)
	}
	if !containsArgPair(args, "--chdir", workdir) {
		t.Fatalf("args = %v, want --chdir %s", args, workdir)
	}
}

func TestBuildBubblewrapArgsInvalidWorkdir(t *testing.T) {
	t.Parallel()

	config := SandboxConfig{
		Enabled:   true,
		BwrapPath: "bwrap",
	}

	_, err := buildBubblewrapArgs("pwd", "/nonexistent/directory", config)
	if err == nil {
		t.Fatalf("buildBubblewrapArgs should fail with nonexistent workdir")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error = %v, want 'does not exist' error", err)
	}
}

// Helper functions for sandbox tests

func containsArg(slice []string, value string) bool {
	for _, item := range slice {
		if item == value {
			return true
		}
	}
	return false
}

func containsArgPair(slice []string, key, value string) bool {
	for i := 0; i < len(slice)-1; i++ {
		if slice[i] == key && slice[i+1] == value {
			return true
		}
	}
	return false
}
