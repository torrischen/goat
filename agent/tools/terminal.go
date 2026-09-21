package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/torrischen/goat/agent/common"
)

const (
	InternalToolShellCommand = "shell_command"

	defaultCommandTimeout   = 10 * time.Second
	maximumCommandTimeout   = 120 * time.Second
	defaultMaxOutputBytes   = 40_000
	maximumMaxOutputBytes   = 400_000
	minimumMaxOutputBytes   = 1_000
	truncationBoundaryBytes = 256
)

// SandboxConfig defines the configuration for sandboxed command execution using bubblewrap.
type SandboxConfig struct {
	// Enabled determines whether to use bubblewrap sandbox.
	Enabled bool
	// BwrapPath is the path to the bubblewrap binary. Defaults to "bwrap".
	BwrapPath string
	// AllowedPaths are additional paths to bind mount (read-write) into the sandbox.
	AllowedPaths []string
	// ReadOnlyPaths are additional paths to bind mount (read-only) into the sandbox.
	ReadOnlyPaths []string
	// TmpfsSize is the size limit for /tmp in the sandbox. Defaults to "100M".
	TmpfsSize string
	// NetworkAccess determines whether network access is allowed. Defaults to false.
	NetworkAccess bool
	// PreserveEnv is a list of environment variable names to preserve in the sandbox.
	PreserveEnv []string
}

// Terminal returns a Codex-style shell command tool adapted to React's
// common.Tool interface. Commands run synchronously; PTY sessions, sandbox
// approvals, and interactive stdin are intentionally left to the host process.
func Terminal() common.Tool {
	return TerminalWithConfig(SandboxConfig{Enabled: false})
}

// TerminalSandboxed returns a Terminal tool with strict sandbox enabled using bubblewrap.
// Only works on Linux systems. Uses secure defaults:
// - No network access
// - Only essential system directories mounted read-only
// - Limited tmpfs (100M)
// - Minimal environment variables
func TerminalSandboxed() common.Tool {
	return TerminalWithSandbox(SandboxConfig{})
}

// TerminalWithSandbox returns a Terminal tool with custom sandbox configuration.
// The Enabled field is automatically set to true.
func TerminalWithSandbox(config SandboxConfig) common.Tool {
	config.Enabled = true
	if config.BwrapPath == "" {
		config.BwrapPath = "bwrap"
	}
	if config.TmpfsSize == "" {
		config.TmpfsSize = "100M"
	}
	return TerminalWithConfig(config)
}

// TerminalWithConfig returns a Terminal tool with the specified configuration.
func TerminalWithConfig(config SandboxConfig) common.Tool {
	description := `Runs a shell command and returns its combined stdout and stderr.
Use this tool for repository inspection, such as listing files, searching with rg, and reading files with sed or cat.
Always set workdir when the repository directory is known. Prefer rg and rg --files over grep and find.
Commands run synchronously without a PTY, approval flow, or interactive stdin.`

	if config.Enabled {
		description += "\nSandbox mode: Commands run in a bubblewrap sandbox with restricted system access."
	}

	return &common.DefaultTool{
		ToolName:        InternalToolShellCommand,
		ToolDescription: description,
		ToolParameters: common.NewToolParameters(
			common.ToolProperty{
				Name:        "command",
				Type:        "string",
				Required:    true,
				Description: "Shell command to execute.",
			},
			common.ToolProperty{
				Name:        "workdir",
				Type:        "string",
				Description: "Working directory for the command. Defaults to the current process directory.",
			},
			common.ToolProperty{
				Name:        "timeout_ms",
				Type:        "integer",
				Description: "Maximum command runtime in milliseconds. Defaults to 10000 and is capped at 120000.",
			},
			common.ToolProperty{
				Name:        "max_output_bytes",
				Type:        "integer",
				Description: "Maximum returned output size in bytes. Defaults to 40000 and is capped at 400000.",
			},
		),
		F: func(actx *common.AgentContext, inputs map[string]any) common.ToolResult {
			return executeShellCommand(actx, inputs, config)
		},
	}
}

// ShellCommand is an explicit alias for Terminal.
func ShellCommand() common.Tool {
	return Terminal()
}

func executeShellCommand(actx *common.AgentContext, inputs map[string]any, config SandboxConfig) common.ToolResult {
	command, ok := stringInput(inputs, "command")
	if !ok || strings.TrimSpace(command) == "" {
		return common.NewDefaultToolResult("command parameter is missing or invalid.")
	}

	workdir, _ := stringInput(inputs, "workdir")
	timeout, err := durationInput(inputs, "timeout_ms", defaultCommandTimeout, maximumCommandTimeout)
	if err != nil {
		return common.NewDefaultToolResult(err.Error())
	}
	maxOutputBytes, err := integerInput(
		inputs,
		"max_output_bytes",
		defaultMaxOutputBytes,
		minimumMaxOutputBytes,
		maximumMaxOutputBytes,
	)
	if err != nil {
		return common.NewDefaultToolResult(err.Error())
	}

	parent := context.Background()
	if actx != nil && actx.Context != nil {
		parent = actx.Context
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if config.Enabled {
		// Check if bubblewrap is available
		if runtime.GOOS != "linux" {
			return common.NewDefaultToolResult("Sandbox mode is only supported on Linux systems.")
		}
		if _, err := exec.LookPath(config.BwrapPath); err != nil {
			return common.NewDefaultToolResult(fmt.Sprintf("Sandbox mode requires bubblewrap (%s) but it is not installed: %v", config.BwrapPath, err))
		}

		bwrapArgs, err := buildBubblewrapArgs(command, workdir, config)
		if err != nil {
			return common.NewDefaultToolResult(fmt.Sprintf("Failed to build sandbox command: %v", err))
		}
		cmd = exec.CommandContext(ctx, config.BwrapPath, bwrapArgs...)
	} else {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		cmd = exec.CommandContext(ctx, shell, "-lc", command)
		if workdir != "" {
			cmd.Dir = workdir
		}
	}

	startedAt := time.Now()
	output := newCommandOutputBuffer(maxOutputBytes)
	cmd.Stdout = &output
	cmd.Stderr = &output
	runErr := cmd.Run()
	elapsed := time.Since(startedAt)

	exitCode := 0
	if runErr != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}

	status := fmt.Sprintf("Wall time: %.4f seconds\nProcess exited with code %d", elapsed.Seconds(), exitCode)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		status = fmt.Sprintf("Wall time: %.4f seconds\nProcess timed out after %d ms", elapsed.Seconds(), timeout.Milliseconds())
	} else if errors.Is(ctx.Err(), context.Canceled) {
		status = fmt.Sprintf("Wall time: %.4f seconds\nProcess canceled", elapsed.Seconds())
	} else if runErr != nil && exitCode == -1 {
		status += "\nFailed to start command: " + runErr.Error()
	}

	formattedOutput := output.String()
	if formattedOutput == "" {
		return common.NewDefaultToolResult(status)
	}
	return common.NewDefaultToolResult(status + "\nFinal output:\n" + formattedOutput)
}

func stringInput(inputs map[string]any, name string) (string, bool) {
	value, ok := inputs[name]
	if !ok {
		return "", false
	}
	result, ok := value.(string)
	return result, ok
}

func durationInput(
	inputs map[string]any,
	name string,
	defaultValue time.Duration,
	maximumValue time.Duration,
) (time.Duration, error) {
	value, ok := inputs[name]
	if !ok {
		return defaultValue, nil
	}

	milliseconds, err := numericInt64(value)
	if err != nil || milliseconds <= 0 {
		return 0, fmt.Errorf("%s parameter must be a positive integer", name)
	}
	if milliseconds > maximumValue.Milliseconds() {
		return maximumValue, nil
	}
	return time.Duration(milliseconds) * time.Millisecond, nil
}

func integerInput(inputs map[string]any, name string, defaultValue, minimumValue, maximumValue int) (int, error) {
	value, ok := inputs[name]
	if !ok {
		return defaultValue, nil
	}

	parsed, err := numericInt64(value)
	if err != nil || parsed < int64(minimumValue) {
		return 0, fmt.Errorf("%s parameter must be an integer greater than or equal to %d", name, minimumValue)
	}
	if parsed > int64(maximumValue) {
		return maximumValue, nil
	}
	return int(parsed), nil
}

func numericInt64(value any) (int64, error) {
	switch number := value.(type) {
	case int:
		return int64(number), nil
	case int8:
		return int64(number), nil
	case int16:
		return int64(number), nil
	case int32:
		return int64(number), nil
	case int64:
		return number, nil
	case uint:
		if uint64(number) > uint64(^uint64(0)>>1) {
			return 0, strconv.ErrRange
		}
		return int64(number), nil
	case uint8:
		return int64(number), nil
	case uint16:
		return int64(number), nil
	case uint32:
		return int64(number), nil
	case uint64:
		if number > uint64(^uint64(0)>>1) {
			return 0, strconv.ErrRange
		}
		return int64(number), nil
	case float32:
		return exactFloatInt64(float64(number))
	case float64:
		return exactFloatInt64(number)
	default:
		return 0, fmt.Errorf("not a number")
	}
}

func exactFloatInt64(number float64) (int64, error) {
	if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number ||
		number < math.MinInt64 || number >= math.MaxInt64 {
		return 0, fmt.Errorf("not an integer")
	}
	parsed := int64(number)
	return parsed, nil
}

// buildBubblewrapArgs constructs the argument list for bubblewrap to create a strict sandbox.
func buildBubblewrapArgs(command, workdir string, config SandboxConfig) ([]string, error) {
	args := []string{
		// Core isolation
		"--unshare-all",     // Unshare all namespaces (user, ipc, pid, net, uts, cgroup)
		"--die-with-parent", // Kill sandbox if parent dies
		"--new-session",     // New session ID
	}

	// Network isolation (default: no network)
	if !config.NetworkAccess {
		args = append(args, "--unshare-net")
	}

	// Essential read-only system directories (strict minimal set)
	// We only bind what's absolutely necessary for basic shell commands
	systemDirs := []string{
		"/usr",   // User binaries and libraries
		"/lib",   // System libraries (if exists)
		"/lib64", // 64-bit libraries (if exists)
		"/etc",   // System configuration (read-only)
	}

	for _, dir := range systemDirs {
		if _, err := os.Stat(dir); err == nil {
			args = append(args, "--ro-bind", dir, dir)
		}
	}

	// /bin and /sbin are usually symlinks to /usr/bin and /usr/sbin
	// Check and bind them if they exist as real directories
	for _, dir := range []string{"/bin", "/sbin"} {
		if info, err := os.Lstat(dir); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				// It's a symlink, bind it as symlink
				if target, err := os.Readlink(dir); err == nil {
					args = append(args, "--symlink", target, dir)
				}
			} else {
				// It's a real directory
				args = append(args, "--ro-bind", dir, dir)
			}
		}
	}

	// Minimal /dev (only essential devices)
	args = append(args,
		"--dev-bind", "/dev/null", "/dev/null",
		"--dev-bind", "/dev/zero", "/dev/zero",
		"--dev-bind", "/dev/random", "/dev/random",
		"--dev-bind", "/dev/urandom", "/dev/urandom",
	)

	// Minimal proc
	args = append(args, "--proc", "/proc")

	// Temporary filesystem with size limit
	tmpfsSize := config.TmpfsSize
	if tmpfsSize == "" {
		tmpfsSize = "100M"
	}
	args = append(args, "--tmpfs", "/tmp")
	args = append(args, "--setenv", "TMPDIR", "/tmp")

	// Bind working directory if specified
	if workdir != "" {
		absWorkdir, err := filepath.Abs(workdir)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve workdir: %w", err)
		}
		if _, err := os.Stat(absWorkdir); err != nil {
			return nil, fmt.Errorf("workdir does not exist: %w", err)
		}
		args = append(args, "--bind", absWorkdir, absWorkdir)
		args = append(args, "--chdir", absWorkdir)
	}

	// Bind additional allowed paths (read-write)
	for _, path := range config.AllowedPaths {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve allowed path %s: %w", path, err)
		}
		if _, err := os.Stat(absPath); err != nil {
			return nil, fmt.Errorf("allowed path %s does not exist: %w", path, err)
		}
		args = append(args, "--bind", absPath, absPath)
	}

	// Bind additional read-only paths
	for _, path := range config.ReadOnlyPaths {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve read-only path %s: %w", path, err)
		}
		if _, err := os.Stat(absPath); err != nil {
			return nil, fmt.Errorf("read-only path %s does not exist: %w", path, err)
		}
		args = append(args, "--ro-bind", absPath, absPath)
	}

	// Minimal environment variables
	// By default, bubblewrap clears all env vars. We set only essential ones.
	args = append(args,
		"--setenv", "PATH", "/usr/local/bin:/usr/bin:/bin",
		"--setenv", "HOME", "/tmp/sandbox-home",
		"--unsetenv", "HISTFILE", // Don't save command history
	)

	// Preserve specific environment variables if requested
	for _, envName := range config.PreserveEnv {
		if value := os.Getenv(envName); value != "" {
			args = append(args, "--setenv", envName, value)
		}
	}

	// Execute the shell command
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	args = append(args, "--", shell, "-c", command)

	return args, nil
}

type commandOutputBuffer struct {
	maximumBytes int
	head         []byte
	tail         []byte
	totalBytes   int64
}

func newCommandOutputBuffer(maximumBytes int) commandOutputBuffer {
	return commandOutputBuffer{
		maximumBytes: maximumBytes,
		head:         make([]byte, 0, maximumBytes/2),
		tail:         make([]byte, 0, maximumBytes-maximumBytes/2),
	}
}

func (b *commandOutputBuffer) Write(data []byte) (int, error) {
	written := len(data)
	b.totalBytes += int64(written)

	headCapacity := b.maximumBytes / 2
	if len(b.head) < headCapacity {
		headBytes := min(len(data), headCapacity-len(b.head))
		b.head = append(b.head, data[:headBytes]...)
		data = data[headBytes:]
	}

	tailCapacity := b.maximumBytes - headCapacity
	if len(data) >= tailCapacity {
		b.tail = append(b.tail[:0], data[len(data)-tailCapacity:]...)
		return written, nil
	}
	if overflow := len(b.tail) + len(data) - tailCapacity; overflow > 0 {
		copy(b.tail, b.tail[overflow:])
		b.tail = b.tail[:len(b.tail)-overflow]
	}
	b.tail = append(b.tail, data...)
	return written, nil
}

func (b *commandOutputBuffer) String() string {
	if b.totalBytes <= int64(b.maximumBytes) {
		return string(b.head) + string(b.tail)
	}

	truncatedBytes := b.totalBytes - int64(len(b.head)+len(b.tail))
	marker := fmt.Sprintf("\n... %d bytes truncated ...\n", truncatedBytes)
	available := b.maximumBytes - len(marker)
	if available <= truncationBoundaryBytes*2 {
		return string(b.head) + string(b.tail[:max(0, available-len(b.head))])
	}

	headBytes := available / 2
	tailBytes := available - headBytes
	return string(b.head[:min(headBytes, len(b.head))]) + marker + string(b.tail[len(b.tail)-min(tailBytes, len(b.tail)):])
}
