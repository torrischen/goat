# Terminal Tool Sandbox Mode

## Overview

The Terminal tool now supports sandbox mode using [bubblewrap](https://github.com/containers/bubblewrap) to execute shell commands in an isolated environment, providing additional security guarantees.

## Quick Start

### Basic Usage

```go
// Default mode (no sandbox, backward compatible)
agent.AddTool(ctx, tools.Terminal())

// Enable sandbox (with secure defaults)
agent.AddTool(ctx, tools.TerminalSandboxed())
```

### Custom Configuration

```go
agent.AddTool(ctx, tools.TerminalWithSandbox(tools.SandboxConfig{
    NetworkAccess: false,                         // Disable network access
    AllowedPaths:  []string{"/home/user/project"}, // Additional writable paths
    ReadOnlyPaths: []string{"/etc/myconfig"},     // Additional read-only paths
    TmpfsSize:     "200M",                        // tmpfs size limit
    PreserveEnv:   []string{"USER", "LANG"},      // Preserved environment variables
}))
```

## Security Features

### Namespace Isolation
- **User namespace**: Prevent privilege escalation
- **IPC namespace**: Isolate inter-process communication
- **PID namespace**: Isolate process ID space
- **Network namespace**: Isolate network stack (network disabled by default)
- **UTS namespace**: Isolate hostname and domain name
- **Cgroup namespace**: Isolate cgroup view

### Filesystem Restrictions
The sandbox strictly limits filesystem access:

**Read-only system directories** (only bind existing directories):
- `/usr` - User programs and libraries
- `/lib` - System libraries (if exists)
- `/lib64` - 64-bit libraries (if exists)
- `/etc` - System configuration (read-only)
- `/bin`, `/sbin` - Bind as symlinks or directories

**Minimal device access**:
- `/dev/null`
- `/dev/zero`
- `/dev/random`
- `/dev/urandom`
- Does not bind `/dev/pts`, `/dev/shm`, etc.

**Temporary filesystem**:
- `/tmp` uses tmpfs with size limit (default 100M)
- Automatically cleaned up after sandbox exits

**Working directory**:
- Only explicitly specified `workdir` is bound (read-write)
- Additional paths require configuration via `AllowedPaths` or `ReadOnlyPaths`

### Environment Variable Restrictions
By default, environment variables in the sandbox are cleared, only setting:
- `PATH=/usr/local/bin:/usr/bin:/bin`
- `HOME=/tmp/sandbox-home`
- `TMPDIR=/tmp`
- Command history not saved (`HISTFILE` is unset)

You can selectively preserve needed environment variables via `PreserveEnv`.

### Network Isolation
By default, network access is completely disabled (`--unshare-net`). Even if `NetworkAccess` is enabled, it's only within the network namespace and does not affect the host network.

## Configuration Options

### SandboxConfig Structure

```go
type SandboxConfig struct {
    // Enabled determines whether to use bubblewrap sandbox
    Enabled bool
    
    // BwrapPath is the path to the bubblewrap binary
    // Default: "bwrap"
    BwrapPath string
    
    // AllowedPaths are additional paths to mount (read-write) into the sandbox
    // Example: []string{"/home/user/project", "/data"}
    AllowedPaths []string
    
    // ReadOnlyPaths are additional paths to mount (read-only) into the sandbox
    // Example: []string{"/etc/myapp"}
    ReadOnlyPaths []string
    
    // TmpfsSize is the size limit for /tmp in the sandbox
    // Default: "100M"
    // Format: "100M", "1G", etc.
    TmpfsSize string
    
    // NetworkAccess determines whether to allow network access
    // Default: false (network disabled)
    NetworkAccess bool
    
    // PreserveEnv is a list of environment variable names to preserve in the sandbox
    // Example: []string{"USER", "LANG", "LC_ALL"}
    PreserveEnv []string
}
```

## System Requirements

### Operating System
- **Linux only**: bubblewrap is a Linux-specific tool
- Using sandbox mode on non-Linux systems will return an error

### Dependency Installation

**Ubuntu/Debian**:
```bash
sudo apt install bubblewrap
```

**Fedora/RHEL/CentOS**:
```bash
sudo dnf install bubblewrap
```

**Arch Linux**:
```bash
sudo pacman -S bubblewrap
```

### Verify Installation
```bash
which bwrap
bwrap --version
```

## Use Cases

### Recommended Sandbox Scenarios
1. **Execute untrusted code**: Run scripts from unknown sources
2. **Limit filesystem access**: Prevent accidental modification of system files
3. **Network isolation**: Prevent unauthorized network access
4. **Multi-tenant environments**: Isolate execution environments for different users
5. **CI/CD pipelines**: Execute builds and tests in controlled environments

### Not Recommended Sandbox Scenarios
1. **Full system access required**: Such as system administration scripts
2. **Performance sensitive**: Sandbox has small performance overhead (~10ms startup delay)
3. **Complex dependencies**: Applications requiring access to many system paths
4. **Non-Linux systems**: bubblewrap is unavailable

## Performance Considerations

- **Startup overhead**: Adds approximately 10ms startup delay per command execution
- **Memory overhead**: Sandbox itself has minimal memory footprint (< 1MB)
- **I/O performance**: Bind mount performance impact is negligible
- **CPU overhead**: Namespace switching overhead is minimal

## Troubleshooting

### Error: "Sandbox mode is only supported on Linux systems"
**Cause**: Attempting to use sandbox mode on non-Linux system  
**Solution**: Use `Terminal()` instead of `TerminalSandboxed()`

### Error: "bubblewrap (bwrap) but it is not installed"
**Cause**: bubblewrap is not installed on the system  
**Solution**: Install bubblewrap (see installation instructions above)

### Error: "workdir does not exist"
**Cause**: Specified working directory does not exist  
**Solution**: Ensure working directory exists, or use absolute path

### Command cannot find files/directories
**Cause**: Required path not bound in sandbox  
**Solution**: 
```go
agent.AddTool(ctx, tools.TerminalWithSandbox(tools.SandboxConfig{
    AllowedPaths: []string{"/path/to/needed/directory"},
}))
```

### Network commands fail
**Cause**: Network access disabled by default  
**Solution**: 
```go
agent.AddTool(ctx, tools.TerminalWithSandbox(tools.SandboxConfig{
    NetworkAccess: true,
}))
```

## Examples

### Example 1: Basic Sandbox Execution
```go
tool := tools.TerminalSandboxed()
result := tool.Execute(ctx, map[string]any{
    "command": "ls -la",
    "workdir": "/home/user/project",
})
```

### Example 2: Allow Network Access
```go
tool := tools.TerminalWithSandbox(tools.SandboxConfig{
    NetworkAccess: true,
})
result := tool.Execute(ctx, map[string]any{
    "command": "curl https://api.example.com",
})
```

### Example 3: Multiple Path Access
```go
tool := tools.TerminalWithSandbox(tools.SandboxConfig{
    AllowedPaths: []string{
        "/home/user/project",
        "/var/data",
    },
    ReadOnlyPaths: []string{
        "/etc/myapp",
    },
})
result := tool.Execute(ctx, map[string]any{
    "command": "cat /etc/myapp/config.yaml",
    "workdir": "/home/user/project",
})
```

### Example 4: Preserve Environment Variables
```go
tool := tools.TerminalWithSandbox(tools.SandboxConfig{
    PreserveEnv: []string{"USER", "HOME", "LANG", "LC_ALL"},
})
result := tool.Execute(ctx, map[string]any{
    "command": "echo $USER",
})
```

## Security Recommendations

1. **Principle of least privilege**: Only bind necessary paths
2. **Prefer read-only**: Use `ReadOnlyPaths` over `AllowedPaths` when possible
3. **Disable network**: Keep `NetworkAccess: false` unless explicitly needed
4. **Limit tmpfs**: Set reasonable `TmpfsSize` based on needs
5. **Minimal environment**: Only preserve necessary environment variables
6. **Audit configuration**: Regularly review sandbox configuration for appropriateness

## Comparison with Other Tools

| Feature | bubblewrap | Docker | systemd-nspawn | chroot |
|---------|-----------|--------|----------------|--------|
| Startup speed | Very fast (~10ms) | Slow (~1s) | Medium (~100ms) | Fast |
| Isolation level | High | Very high | High | Low |
| Resource overhead | Minimal | Medium | Medium | Minimal |
| Configuration complexity | Low | High | Medium | Low |
| Use case | Single command isolation | Containerized apps | System containers | Simple isolation |

## Technical Details

### bubblewrap Arguments Example
```bash
bwrap \
  --unshare-all \
  --die-with-parent \
  --new-session \
  --unshare-net \
  --ro-bind /usr /usr \
  --ro-bind /lib /lib \
  --ro-bind /lib64 /lib64 \
  --ro-bind /etc /etc \
  --dev-bind /dev/null /dev/null \
  --proc /proc \
  --tmpfs /tmp \
  --bind /home/user/project /home/user/project \
  --chdir /home/user/project \
  --setenv PATH /usr/local/bin:/usr/bin:/bin \
  --setenv HOME /tmp/sandbox-home \
  -- /bin/sh -c "your command"
```

### Namespace Explanation
- `--unshare-all`: Create all new namespaces
- `--die-with-parent`: Automatically terminate sandbox when parent process exits
- `--new-session`: Create new session ID
- `--unshare-net`: Create isolated network namespace

## References

- [bubblewrap GitHub](https://github.com/containers/bubblewrap)
- [Linux Namespaces](https://man7.org/linux/man-pages/man7/namespaces.7.html)
- [Seccomp](https://man7.org/linux/man-pages/man2/seccomp.2.html)
- [Linux Capabilities](https://man7.org/linux/man-pages/man7/capabilities.7.html)
