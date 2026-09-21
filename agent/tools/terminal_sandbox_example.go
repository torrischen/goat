package tools

// Example usage of Terminal with sandbox mode
//
// Basic usage with default sandbox settings:
//
//	agent.AddTool(ctx, tools.TerminalSandboxed())
//
// Custom sandbox configuration:
//
//	agent.AddTool(ctx, tools.TerminalWithSandbox(tools.SandboxConfig{
//		NetworkAccess: false,                         // No network access (default)
//		AllowedPaths:  []string{"/home/user/project"}, // Additional writable paths
//		ReadOnlyPaths: []string{"/etc/myconfig"},     // Additional read-only paths
//		TmpfsSize:     "200M",                        // Increase tmpfs size
//		PreserveEnv:   []string{"USER", "LANG"},      // Preserve specific env vars
//	}))
//
// Standard non-sandboxed mode (backward compatible):
//
//	agent.AddTool(ctx, tools.Terminal())
//
// Sandbox Security Features:
//
// - Namespace isolation: User, IPC, PID, Network, UTS, and Cgroup namespaces are unshared
// - Filesystem restrictions: Only essential system directories mounted read-only (/usr, /lib, /lib64, /etc)
// - Network isolation: Network access is disabled by default
// - Limited tmpfs: /tmp has a size limit (default 100M)
// - Minimal environment: Only PATH and HOME are set by default
// - No write access to system directories
// - Working directory and additional paths can be selectively allowed
//
// Requirements:
//
// - Linux operating system (bubblewrap is Linux-only)
// - bubblewrap (bwrap) must be installed on the system
//   Ubuntu/Debian: apt install bubblewrap
//   Fedora/RHEL: dnf install bubblewrap
//   Arch: pacman -S bubblewrap
//
// Note: Sandboxed commands may have limited access compared to regular Terminal().
// Ensure that workdir and any required paths are explicitly configured.
