// Package config defines the goatc build and runtime configuration.
package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

// CurrentVersion is the YAML schema version understood by goatc.
const CurrentVersion = "v1"

// AgentType selects the orchestration strategy used by the generated agent.
type AgentType string

const (
	AgentTypeReact       AgentType = "react"
	AgentTypePlanExecute AgentType = "plan_execute"
)

// ToolProvider selects how a configured tool source is loaded.
type ToolProvider string

const (
	ToolProviderGoPlugin ToolProvider = "go_plugin"
	ToolProviderGRPC     ToolProvider = "grpc"
	ToolProviderMCP      ToolProvider = "mcp"
	ToolProviderBuiltin  ToolProvider = "builtin"
)

const (
	BuiltinTerminal  = "terminal"
	BuiltinSubagents = "subagents"
)

// MCPTransport selects the transport used to connect to an MCP server.
type MCPTransport string

const (
	MCPTransportStdio          MCPTransport = "stdio"
	MCPTransportSSE            MCPTransport = "sse"
	MCPTransportStreamableHTTP MCPTransport = "streamable_http"
)

// Config is the complete goatc build and runtime configuration.
type Config struct {
	Version string  `yaml:"version"`
	Agent   Agent   `yaml:"agent"`
	Model   Model   `yaml:"model"`
	Context Context `yaml:"context,omitempty"`
	Tools   []Tool  `yaml:"tools"`
	Build   Build   `yaml:"build,omitempty"`
	TUI     TUI     `yaml:"tui,omitempty"`
}

// Agent configures the generated agent loop.
type Agent struct {
	Name                string      `yaml:"name"`
	Type                AgentType   `yaml:"type,omitempty"`
	ModelMaxTokensK     int         `yaml:"model_max_tokens_k,omitempty"`
	MaxSteps            int         `yaml:"max_steps,omitempty"`
	EnablePlanning      bool        `yaml:"enable_planning,omitempty"`
	ParallelTools       int         `yaml:"parallel_tools,omitempty"`
	Compress            bool        `yaml:"compress,omitempty"`
	SkillsDir           string      `yaml:"skills_dir,omitempty"`
	SpecialRequirements []string    `yaml:"special_requirements,omitempty"`
	Plan                *PlanConfig `yaml:"plan,omitempty"`
}

// PlanConfig configures plan creation and execution for a plan-and-execute agent.
type PlanConfig struct {
	MaxSteps         int `yaml:"max_steps,omitempty"`
	ExecutorMaxSteps int `yaml:"executor_max_steps,omitempty"`
	MaxReplans       int `yaml:"max_replans,omitempty"`
}

// Model configures the generated agent's model provider.
type Model struct {
	Provider        string `yaml:"provider"`
	Name            string `yaml:"name"`
	APIKeyEnv       string `yaml:"api_key_env"`
	BaseURL         string `yaml:"base_url,omitempty"`
	MaxOutputTokens int    `yaml:"max_output_tokens,omitempty"`
}

// Context configures conversation persistence.
type Context struct {
	Backend           string `yaml:"backend,omitempty"`
	Path              string `yaml:"path,omitempty"`
	URI               string `yaml:"uri,omitempty"`
	Database          string `yaml:"database,omitempty"`
	Collection        string `yaml:"collection,omitempty"`
	ContextCollection string `yaml:"context_collection,omitempty"`
	MessageCollection string `yaml:"message_collection,omitempty"`
	KeyPrefix         string `yaml:"key_prefix,omitempty"`
}

// Tool describes one provider-backed tool source. A Go plugin entry contributes
// one tool, while gRPC and MCP entries may discover tools from remote services.
type Tool struct {
	Provider  ToolProvider      `yaml:"provider,omitempty"`
	Name      string            `yaml:"name,omitempty"`
	Source    string            `yaml:"source,omitempty"`
	Address   string            `yaml:"address,omitempty"`
	Transport MCPTransport      `yaml:"transport,omitempty"`
	Command   string            `yaml:"command,omitempty"`
	Args      []string          `yaml:"args,omitempty"`
	URL       string            `yaml:"url,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	Headers   map[string]string `yaml:"headers,omitempty"`
	Sandbox   *Sandbox          `yaml:"sandbox,omitempty"`
}

// Sandbox configures bubblewrap isolation for the builtin terminal tool.
type Sandbox struct {
	Enabled       bool     `yaml:"enabled,omitempty"`
	BwrapPath     string   `yaml:"bwrap_path,omitempty"`
	WritablePaths []string `yaml:"writable_paths,omitempty"`
	ReadOnlyPaths []string `yaml:"readonly_paths,omitempty"`
	TmpfsSize     string   `yaml:"tmpfs_size,omitempty"`
	Network       bool     `yaml:"network,omitempty"`
	PreserveEnv   []string `yaml:"preserve_env,omitempty"`
}

// Build configures the output artifact.
type Build struct {
	Output string   `yaml:"output,omitempty"`
	GOOS   string   `yaml:"goos,omitempty"`
	GOARCH string   `yaml:"goarch,omitempty"`
	Tags   []string `yaml:"tags,omitempty"`
}

// TUI configures the generated terminal interface.
type TUI struct {
	Welcome string `yaml:"welcome,omitempty"`
}

// Load reads and validates a YAML configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Parse decodes and validates a YAML configuration.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode config: multiple YAML documents are not allowed")
		}
		return nil, fmt.Errorf("decode config: %w", err)
	}
	cfg.setDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) setDefaults() {
	if c.Version == "" {
		c.Version = CurrentVersion
	}
	if c.Agent.Name == "" {
		c.Agent.Name = "goat-agent"
	}
	agentType := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(string(c.Agent.Type))), "-", "_")
	if agentType == "" {
		agentType = string(AgentTypeReact)
	}
	c.Agent.Type = AgentType(agentType)
	if c.Agent.ModelMaxTokensK <= 0 {
		c.Agent.ModelMaxTokensK = 128
	}
	if c.Agent.MaxSteps <= 0 {
		c.Agent.MaxSteps = 8
	}
	if c.Agent.Type == AgentTypePlanExecute {
		if c.Agent.Plan == nil {
			c.Agent.Plan = &PlanConfig{}
		}
		if c.Agent.Plan.MaxSteps <= 0 {
			c.Agent.Plan.MaxSteps = 8
		}
		if c.Agent.Plan.ExecutorMaxSteps <= 0 {
			c.Agent.Plan.ExecutorMaxSteps = c.Agent.MaxSteps
		}
		if c.Agent.Plan.MaxReplans == 0 {
			c.Agent.Plan.MaxReplans = 2
		}
	}
	c.Agent.SkillsDir = strings.TrimSpace(c.Agent.SkillsDir)
	if c.Model.APIKeyEnv == "" {
		switch strings.ToLower(c.Model.Provider) {
		case "openai":
			c.Model.APIKeyEnv = "OPENAI_API_KEY"
		case "claude", "anthropic":
			c.Model.APIKeyEnv = "ANTHROPIC_API_KEY"
		case "gemini":
			c.Model.APIKeyEnv = "GEMINI_API_KEY"
		}
	}
	if c.Context.Backend == "" {
		c.Context.Backend = "ram"
	}
	if c.Build.Output == "" {
		c.Build.Output = c.Agent.Name
	}
	if c.Build.GOOS == "" {
		c.Build.GOOS = runtime.GOOS
	}
	if c.Build.GOARCH == "" {
		c.Build.GOARCH = runtime.GOARCH
	}
	for i := range c.Tools {
		tool := &c.Tools[i]
		provider := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(string(tool.Provider))), "-", "_")
		if provider == "" && tool.Source != "" {
			provider = string(ToolProviderGoPlugin)
		}
		switch provider {
		case "go", "plugin", "shared_library":
			provider = string(ToolProviderGoPlugin)
		}
		tool.Provider = ToolProvider(provider)

		transport := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(string(tool.Transport))), "-", "_")
		switch transport {
		case "http", "streamablehttp":
			transport = string(MCPTransportStreamableHTTP)
		}
		tool.Transport = MCPTransport(transport)
	}
}

func (c *Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version %q", c.Version)
	}
	if strings.TrimSpace(c.Agent.Name) == "" {
		return fmt.Errorf("agent.name is required")
	}
	switch c.Agent.Type {
	case AgentTypeReact:
		if c.Agent.Plan != nil {
			return fmt.Errorf("agent.plan is only valid when agent.type is %q", AgentTypePlanExecute)
		}
	case AgentTypePlanExecute:
		if c.Agent.EnablePlanning {
			return fmt.Errorf("agent.enable_planning is only valid when agent.type is %q", AgentTypeReact)
		}
		if c.Agent.Plan == nil {
			return fmt.Errorf("agent.plan configuration is required for agent.type %q", AgentTypePlanExecute)
		}
		if c.Agent.Plan.MaxSteps <= 0 {
			return fmt.Errorf("agent.plan.max_steps must be positive")
		}
		if c.Agent.Plan.ExecutorMaxSteps <= 0 {
			return fmt.Errorf("agent.plan.executor_max_steps must be positive")
		}
		if c.Agent.Plan.MaxReplans < 0 {
			return fmt.Errorf("agent.plan.max_replans cannot be negative")
		}
	default:
		return fmt.Errorf("unsupported agent.type %q", c.Agent.Type)
	}
	if c.Model.Name == "" {
		return fmt.Errorf("model.name is required")
	}
	switch strings.ToLower(c.Model.Provider) {
	case "openai", "claude", "anthropic", "gemini":
	default:
		return fmt.Errorf("unsupported model.provider %q", c.Model.Provider)
	}
	if c.Model.APIKeyEnv == "" {
		return fmt.Errorf("model.api_key_env is required")
	}
	if c.Model.MaxOutputTokens < 0 {
		return fmt.Errorf("model.max_output_tokens cannot be negative")
	}
	if len(c.Tools) == 0 {
		return fmt.Errorf("at least one tool provider is required")
	}

	seenPluginNames := make(map[string]struct{}, len(c.Tools))
	seenGRPCAddresses := make(map[string]struct{}, len(c.Tools))
	seenBuiltins := make(map[string]struct{}, len(c.Tools))
	hasGoPlugins := false
	for i := range c.Tools {
		tool := &c.Tools[i]
		tool.Name = strings.TrimSpace(tool.Name)
		switch tool.Provider {
		case ToolProviderGoPlugin:
			hasGoPlugins = true
			tool.Source = strings.TrimSpace(tool.Source)
			if tool.Source == "" {
				return fmt.Errorf("tools[%d].source is required for provider %q", i, tool.Provider)
			}
			if tool.Name == "" {
				tool.Name = filepath.Base(filepath.Clean(tool.Source))
			}
			if !validFileName(tool.Name) {
				return fmt.Errorf("tools[%d].name %q is not a valid file name", i, tool.Name)
			}
			if _, ok := seenPluginNames[tool.Name]; ok {
				return fmt.Errorf("duplicate Go plugin name %q", tool.Name)
			}
			seenPluginNames[tool.Name] = struct{}{}
			if tool.Address != "" || tool.Transport != "" || tool.Command != "" || len(tool.Args) > 0 || tool.URL != "" || len(tool.Env) > 0 || len(tool.Headers) > 0 || tool.Sandbox != nil {
				return fmt.Errorf("tools[%d] contains options that are not valid for provider %q", i, tool.Provider)
			}
		case ToolProviderGRPC:
			tool.Address = strings.TrimSpace(tool.Address)
			if tool.Address == "" {
				return fmt.Errorf("tools[%d].address is required for provider %q", i, tool.Provider)
			}
			if _, ok := seenGRPCAddresses[tool.Address]; ok {
				return fmt.Errorf("duplicate gRPC tool address %q", tool.Address)
			}
			seenGRPCAddresses[tool.Address] = struct{}{}
			if tool.Source != "" || tool.Transport != "" || tool.Command != "" || len(tool.Args) > 0 || tool.URL != "" || len(tool.Env) > 0 || len(tool.Headers) > 0 || tool.Sandbox != nil {
				return fmt.Errorf("tools[%d] contains options that are not valid for provider %q", i, tool.Provider)
			}
		case ToolProviderMCP:
			if err := validateMCPTool(i, tool); err != nil {
				return err
			}
		case ToolProviderBuiltin:
			if err := validateBuiltinTool(i, tool); err != nil {
				return err
			}
			if _, exists := seenBuiltins[tool.Name]; exists {
				return fmt.Errorf("duplicate builtin tool %q", tool.Name)
			}
			seenBuiltins[tool.Name] = struct{}{}
		default:
			return fmt.Errorf("unsupported tools[%d].provider %q", i, tool.Provider)
		}
	}
	if c.Agent.ParallelTools < 0 {
		return fmt.Errorf("agent.parallel_tools cannot be negative")
	}
	switch strings.ToLower(c.Context.Backend) {
	case "ram", "mongodb":
	default:
		return fmt.Errorf("unsupported context.backend %q", c.Context.Backend)
	}
	if hasGoPlugins {
		switch runtime.GOOS {
		case "darwin", "freebsd", "linux":
		default:
			return fmt.Errorf("Go plugins are not supported on %s", runtime.GOOS)
		}
	}
	if c.Build.GOOS != runtime.GOOS || c.Build.GOARCH != runtime.GOARCH {
		return fmt.Errorf("goatc builds must be native: target is %s/%s, host is %s/%s", c.Build.GOOS, c.Build.GOARCH, runtime.GOOS, runtime.GOARCH)
	}
	return nil
}

func validateBuiltinTool(index int, tool *Tool) error {
	tool.Name = strings.ToLower(strings.TrimSpace(tool.Name))
	if tool.Name != BuiltinTerminal && tool.Name != BuiltinSubagents {
		return fmt.Errorf("unsupported builtin tool %q at tools[%d]", tool.Name, index)
	}
	if tool.Source != "" || tool.Address != "" || tool.Transport != "" || tool.Command != "" || len(tool.Args) > 0 || tool.URL != "" || len(tool.Env) > 0 || len(tool.Headers) > 0 {
		return fmt.Errorf("tools[%d] contains options that are not valid for builtin %q", index, tool.Name)
	}
	if tool.Name == BuiltinSubagents && tool.Sandbox != nil {
		return fmt.Errorf("tools[%d].sandbox is only valid for builtin terminal", index)
	}
	if tool.Sandbox != nil {
		for _, path := range append(append([]string{}, tool.Sandbox.WritablePaths...), tool.Sandbox.ReadOnlyPaths...) {
			if strings.TrimSpace(path) == "" {
				return fmt.Errorf("tools[%d].sandbox contains an empty path", index)
			}
		}
		for _, name := range tool.Sandbox.PreserveEnv {
			if strings.TrimSpace(name) == "" || strings.Contains(name, "=") {
				return fmt.Errorf("tools[%d].sandbox contains an invalid environment variable name %q", index, name)
			}
		}
	}
	return nil
}

func validateMCPTool(index int, tool *Tool) error {
	if tool.Source != "" || tool.Address != "" || tool.Sandbox != nil {
		return fmt.Errorf("tools[%d] contains options that are not valid for provider %q", index, tool.Provider)
	}
	for key := range tool.Env {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("tools[%d].env contains an empty variable name", index)
		}
	}
	for key := range tool.Headers {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("tools[%d].headers contains an empty header name", index)
		}
	}

	switch tool.Transport {
	case MCPTransportStdio:
		tool.Command = strings.TrimSpace(tool.Command)
		if tool.Command == "" {
			return fmt.Errorf("tools[%d].command is required for MCP stdio", index)
		}
		if tool.URL != "" || len(tool.Headers) > 0 {
			return fmt.Errorf("tools[%d] contains HTTP options for MCP stdio", index)
		}
	case MCPTransportSSE, MCPTransportStreamableHTTP:
		tool.URL = strings.TrimSpace(tool.URL)
		if tool.URL == "" {
			return fmt.Errorf("tools[%d].url is required for MCP %s", index, tool.Transport)
		}
		if tool.Command != "" || len(tool.Args) > 0 || len(tool.Env) > 0 {
			return fmt.Errorf("tools[%d] contains stdio options for MCP %s", index, tool.Transport)
		}
	default:
		return fmt.Errorf("unsupported tools[%d].transport %q for provider %q", index, tool.Transport, tool.Provider)
	}
	return nil
}

func validFileName(name string) bool {
	if name == "." || name == ".." || strings.TrimSpace(name) == "" {
		return false
	}
	return !strings.ContainsAny(name, `/\\\x00`)
}
