package runtime

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/torrischen/goat/agent/react"
	agenttools "github.com/torrischen/goat/agent/tools"
	"github.com/torrischen/goat/goatc/config"
)

type toolProvider interface {
	Load(context.Context, *react.Agent, []config.Tool, fs.FS, string) ([]io.Closer, error)
}

type goPluginProvider struct{}
type grpcProvider struct{}
type mcpProvider struct{}
type builtinProvider struct{}

type closeFunc func() error

func (f closeFunc) Close() error { return f() }

var toolProviders = map[config.ToolProvider]toolProvider{
	config.ToolProviderGoPlugin: goPluginProvider{},
	config.ToolProviderGRPC:     grpcProvider{},
	config.ToolProviderMCP:      mcpProvider{},
	config.ToolProviderBuiltin:  builtinProvider{},
}

var toolProviderOrder = []config.ToolProvider{
	config.ToolProviderGoPlugin,
	config.ToolProviderGRPC,
	config.ToolProviderBuiltin,
	config.ToolProviderMCP,
}

func loadToolProviders(
	ctx context.Context,
	agent *react.Agent,
	cfg *config.Config,
	assets fs.FS,
) ([]io.Closer, error) {
	grouped := make(map[config.ToolProvider][]config.Tool)
	for _, tool := range cfg.Tools {
		grouped[tool.Provider] = append(grouped[tool.Provider], tool)
	}

	var resources []io.Closer
	for _, providerName := range toolProviderOrder {
		tools := grouped[providerName]
		if len(tools) == 0 {
			continue
		}
		provider, ok := toolProviders[providerName]
		if !ok {
			closeResources(resources)
			return nil, fmt.Errorf("tool provider %q is not registered", providerName)
		}
		loaded, err := provider.Load(ctx, agent, tools, assets, cfg.Agent.Name)
		if err != nil {
			closeResources(resources)
			return nil, fmt.Errorf("load %s tool provider: %w", providerName, err)
		}
		resources = append(resources, loaded...)
	}
	return resources, nil
}

func (goPluginProvider) Load(
	ctx context.Context,
	agent *react.Agent,
	_ []config.Tool,
	assets fs.FS,
	_ string,
) ([]io.Closer, error) {
	pluginDir, err := extractPlugins(assets)
	if err != nil {
		return nil, err
	}
	if err := agent.LoadSharedLibPluginTools(ctx, pluginDir); err != nil {
		os.RemoveAll(pluginDir)
		return nil, err
	}
	return []io.Closer{closeFunc(func() error { return os.RemoveAll(pluginDir) })}, nil
}

func (builtinProvider) Load(
	ctx context.Context,
	agent *react.Agent,
	configured []config.Tool,
	_ fs.FS,
	_ string,
) ([]io.Closer, error) {
	for _, configuredTool := range configured {
		switch configuredTool.Name {
		case config.BuiltinTerminal:
			if configuredTool.Sandbox == nil || !configuredTool.Sandbox.Enabled {
				agent.AddTool(ctx, agenttools.Terminal())
				continue
			}
			sandbox := configuredTool.Sandbox
			agent.AddTool(ctx, agenttools.TerminalWithSandbox(agenttools.SandboxConfig{
				BwrapPath:     sandbox.BwrapPath,
				AllowedPaths:  append([]string(nil), sandbox.WritablePaths...),
				ReadOnlyPaths: append([]string(nil), sandbox.ReadOnlyPaths...),
				TmpfsSize:     sandbox.TmpfsSize,
				NetworkAccess: sandbox.Network,
				PreserveEnv:   append([]string(nil), sandbox.PreserveEnv...),
			}))
		case config.BuiltinSubagents:
			agent.AddTools(ctx, agenttools.SpawnSubAgent(agent), agenttools.GetSubAgentStatus())
		default:
			return nil, fmt.Errorf("unsupported builtin tool %q", configuredTool.Name)
		}
	}
	return nil, nil
}

func (grpcProvider) Load(
	ctx context.Context,
	agent *react.Agent,
	tools []config.Tool,
	_ fs.FS,
	_ string,
) ([]io.Closer, error) {
	addresses := make([]string, 0, len(tools))
	for _, tool := range tools {
		addresses = append(addresses, tool.Address)
	}
	resources, err := agent.LoadRPCPluginTools(ctx, addresses...)
	if err != nil {
		return nil, fmt.Errorf("connect to gRPC tool providers: %w", err)
	}
	return resources, nil
}

func (mcpProvider) Load(
	ctx context.Context,
	agent *react.Agent,
	tools []config.Tool,
	_ fs.FS,
	agentName string,
) ([]io.Closer, error) {
	resources := make([]io.Closer, 0, len(tools))
	for _, tool := range tools {
		client, err := newMCPClient(tool)
		if err != nil {
			closeResources(resources)
			return nil, fmt.Errorf("create %s: %w", providerLabel(tool), err)
		}
		if err := client.Start(ctx); err != nil {
			client.Close()
			closeResources(resources)
			return nil, fmt.Errorf("start %s: %w", providerLabel(tool), err)
		}
		if stderr, ok := mcpclient.GetStderr(client); ok {
			go func() { _, _ = io.Copy(io.Discard, stderr) }()
		}

		request := mcp.InitializeRequest{}
		request.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		request.Params.ClientInfo = mcp.Implementation{
			Name:    agentName + "-goatc",
			Version: config.CurrentVersion,
		}
		request.Params.Capabilities = mcp.ClientCapabilities{}
		if _, err := client.Initialize(ctx, request); err != nil {
			client.Close()
			closeResources(resources)
			return nil, fmt.Errorf("initialize %s: %w", providerLabel(tool), err)
		}
		if err := agent.RegisterMCPTools(ctx, client); err != nil {
			client.Close()
			closeResources(resources)
			return nil, fmt.Errorf("register tools from %s: %w", providerLabel(tool), err)
		}
		resources = append(resources, client)
	}
	return resources, nil
}

func newMCPClient(tool config.Tool) (*mcpclient.Client, error) {
	switch tool.Transport {
	case config.MCPTransportStdio:
		args := make([]string, len(tool.Args))
		for i, arg := range tool.Args {
			args[i] = os.ExpandEnv(arg)
		}
		transport := mcptransport.NewStdio(
			os.ExpandEnv(tool.Command),
			expandEnvironment(tool.Env),
			args...,
		)
		return mcpclient.NewClient(transport), nil
	case config.MCPTransportSSE:
		options := make([]mcptransport.ClientOption, 0, 1)
		if headers := expandValues(tool.Headers); len(headers) > 0 {
			options = append(options, mcptransport.WithHeaders(headers))
		}
		return mcpclient.NewSSEMCPClient(os.ExpandEnv(tool.URL), options...)
	case config.MCPTransportStreamableHTTP:
		options := make([]mcptransport.StreamableHTTPCOption, 0, 1)
		if headers := expandValues(tool.Headers); len(headers) > 0 {
			options = append(options, mcptransport.WithHTTPHeaders(headers))
		}
		return mcpclient.NewStreamableHttpClient(os.ExpandEnv(tool.URL), options...)
	default:
		return nil, fmt.Errorf("unsupported MCP transport %q", tool.Transport)
	}
}

func providerLabel(tool config.Tool) string {
	if tool.Name != "" {
		return tool.Name
	}
	switch tool.Provider {
	case config.ToolProviderGRPC:
		return tool.Address
	case config.ToolProviderMCP:
		if tool.Transport == config.MCPTransportStdio {
			return tool.Command
		}
		return tool.URL
	default:
		return string(tool.Provider)
	}
}

func expandEnvironment(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return nil
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+os.ExpandEnv(overrides[key]))
	}
	return result
}

func expandValues(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	expanded := make(map[string]string, len(values))
	for key, value := range values {
		expanded[key] = os.ExpandEnv(value)
	}
	return expanded
}

func closeResources(resources []io.Closer) {
	for i := len(resources) - 1; i >= 0; i-- {
		_ = resources[i].Close()
	}
}
