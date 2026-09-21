package main

import (
	"context"
	"fmt"
	"log"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/tools"
)

// This example demonstrates how to use the Terminal tool's sandbox mode

func main() {
	ctx := context.Background()
	agentCtx := common.NewAgentContext(ctx)

	// Example 1: Basic sandbox mode (using secure default settings)
	fmt.Println("=== Example 1: Basic Sandbox Mode ===")
	basicSandboxExample(agentCtx)

	// Example 2: Custom sandbox configuration
	fmt.Println("\n=== Example 2: Custom Sandbox Configuration ===")
	customSandboxExample(agentCtx)

	// Example 3: Sandbox with network access
	fmt.Println("\n=== Example 3: Network Access Enabled ===")
	networkSandboxExample(agentCtx)

	// Example 4: Multiple path access
	fmt.Println("\n=== Example 4: Multiple Path Access ===")
	multiPathExample(agentCtx)

	// Example 5: Normal mode vs Sandbox mode comparison
	fmt.Println("\n=== Example 5: Normal Mode vs Sandbox Mode ===")
	comparisonExample(agentCtx)
}

// basicSandboxExample demonstrates basic sandbox usage
func basicSandboxExample(ctx *common.AgentContext) {
	tool := tools.TerminalSandboxed()

	// Execute simple command in sandbox
	result := tool.Execute(ctx, map[string]any{
		"command": "echo 'Hello from sandbox!' && whoami && pwd",
	})

	fmt.Println("Result:", result.String())
}

// customSandboxExample demonstrates custom sandbox configuration
func customSandboxExample(ctx *common.AgentContext) {
	// Create temporary working directory (in real projects, use actual path)
	workdir := "/tmp/sandbox-test"

	tool := tools.TerminalWithSandbox(tools.SandboxConfig{
		NetworkAccess: false,                   // Disable network
		TmpfsSize:     "200M",                  // Increase tmpfs size
		PreserveEnv:   []string{"USER", "PWD"}, // Preserve specific environment variables
	})

	result := tool.Execute(ctx, map[string]any{
		"command": "echo $USER && df -h /tmp | tail -1",
		"workdir": workdir,
	})

	fmt.Println("Result:", result.String())
}

// networkSandboxExample demonstrates sandbox with network enabled
func networkSandboxExample(ctx *common.AgentContext) {
	tool := tools.TerminalWithSandbox(tools.SandboxConfig{
		NetworkAccess: true, // Enable network access
	})

	// Note: This may still fail in actual sandbox depending on system configuration
	result := tool.Execute(ctx, map[string]any{
		"command":    "curl -I https://www.google.com 2>&1 || echo 'Network not available'",
		"timeout_ms": 5000,
	})

	fmt.Println("Result:", result.String())
}

// multiPathExample demonstrates multi-path access configuration
func multiPathExample(ctx *common.AgentContext) {
	tool := tools.TerminalWithSandbox(tools.SandboxConfig{
		AllowedPaths: []string{
			"/tmp", // Writable access
		},
		ReadOnlyPaths: []string{
			"/etc/hosts", // Read-only access to specific file
		},
	})

	result := tool.Execute(ctx, map[string]any{
		"command": "cat /etc/hosts | head -3 && echo '---' && ls -la /tmp | head -5",
	})

	fmt.Println("Result:", result.String())
}

// comparisonExample compares normal mode and sandbox mode
func comparisonExample(ctx *common.AgentContext) {
	// Normal mode
	normalTool := tools.Terminal()
	fmt.Println("Normal mode:")
	result1 := normalTool.Execute(ctx, map[string]any{
		"command": "ls /home 2>&1 || echo 'Cannot access'",
	})
	fmt.Println("Access /home:", result1.String())

	// Sandbox mode (cannot access /home by default)
	sandboxTool := tools.TerminalSandboxed()
	fmt.Println("\nSandbox mode:")
	result2 := sandboxTool.Execute(ctx, map[string]any{
		"command": "ls /home 2>&1 || echo 'Cannot access /home (expected in sandbox)'",
	})
	fmt.Println("Access /home:", result2.String())

	// Sandbox mode but explicitly allow access to /home
	sandboxWithHomeTool := tools.TerminalWithSandbox(tools.SandboxConfig{
		ReadOnlyPaths: []string{"/home"},
	})
	fmt.Println("\nSandbox mode (with /home access):")
	result3 := sandboxWithHomeTool.Execute(ctx, map[string]any{
		"command": "ls /home 2>&1 | head -5",
	})
	fmt.Println("Access /home:", result3.String())
}

// Usage example in actual agent
func agentIntegrationExample() {
	// Assume you have an agent instance
	// agent := yourAgentCreationFunction()

	ctx := context.Background()

	// Option 1: Use sandbox for all commands (more secure)
	// agent.AddTool(ctx, tools.TerminalSandboxed())

	// Option 2: Provide two tools - let LLM choose
	// agent.AddTool(ctx, tools.Terminal())          // Normal mode
	// agent.AddTool(ctx, tools.TerminalSandboxed()) // Sandbox mode

	// Option 3: Choose based on scenario
	// If handling untrusted input
	// agent.AddTool(ctx, tools.TerminalSandboxed())
	// If full system access needed
	// agent.AddTool(ctx, tools.Terminal())

	log.Printf("Agent initialized with terminal tool, context: %v", ctx)
}

// Security best practices example
func securityBestPractices() {
	ctx := common.NewAgentContext(context.Background())

	// ✅ Good practice: Least privilege
	goodTool := tools.TerminalWithSandbox(tools.SandboxConfig{
		NetworkAccess: false, // Disable network unless needed
		AllowedPaths: []string{
			"/specific/project/path", // Only allow needed paths
		},
		TmpfsSize:   "100M",             // Limit temporary file size
		PreserveEnv: []string{"LC_ALL"}, // Only preserve necessary env vars
	})

	// ❌ Bad practice: Too permissive
	badTool := tools.TerminalWithSandbox(tools.SandboxConfig{
		NetworkAccess: true, // Unnecessary network access
		AllowedPaths: []string{
			"/",     // Too broad
			"/home", // Unnecessary access
			"/etc",  // May contain sensitive info
		},
		TmpfsSize: "10G", // Excessive temporary space
		PreserveEnv: []string{
			"HOME", "USER", "PATH", "SSH_AUTH_SOCK", // Too many env vars
		},
	})

	// Use good configuration
	result := goodTool.Execute(ctx, map[string]any{
		"command": "echo 'Secure execution'",
	})
	fmt.Println("Secure execution result:", result.String())

	// Avoid using insecure configuration
	_ = badTool // For demonstration only, should not configure this way in practice
}
