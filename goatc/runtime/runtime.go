// Package runtime starts an agent assembled by goatc.
package runtime

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/agenticclaude"
	"github.com/cloudwego/eino-ext/components/model/agenticgemini"
	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/components/model"
	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	filecontext "github.com/torrischen/goat/agent/contextmgr/file"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	sqlitecontext "github.com/torrischen/goat/agent/contextmgr/sqlite"
	"github.com/torrischen/goat/agent/planexecute"
	"github.com/torrischen/goat/agent/react"
	"github.com/torrischen/goat/goatc/config"
	"github.com/torrischen/goat/goatc/tui"
	"google.golang.org/genai"
)

// Run initializes and launches an agent from generated embedded assets.
func Run(ctx context.Context, assets fs.FS) error {
	data, err := fs.ReadFile(assets, "goatc.yaml")
	if err != nil {
		return fmt.Errorf("read embedded config: %w", err)
	}
	cfg, err := config.Parse(data)
	if err != nil {
		return err
	}
	return RunConfig(ctx, cfg, assets)
}

// RunConfig initializes and launches an already parsed configuration.
func RunConfig(ctx context.Context, cfg *config.Config, assets fs.FS) error {
	llm, err := newModel(ctx, cfg.Model)
	if err != nil {
		return err
	}
	agent, executor, err := newAgent(ctx, llm, cfg)
	if err != nil {
		return err
	}

	resources, err := loadToolProviders(ctx, executor, cfg, assets)
	if err != nil {
		return err
	}
	defer closeResources(resources)

	return tui.Run(ctx, agent, cfg)
}

func newAgent(ctx context.Context, llm model.AgenticModel, cfg *config.Config) (common.Agent, *react.Agent, error) {
	executorManager, err := newContextManager(cfg.Context)
	if err != nil {
		return nil, nil, err
	}
	executor := react.NewAgent(llm, cfg.Agent.ModelMaxTokensK, executorManager)
	if cfg.Agent.SkillsDir != "" {
		executor.EnableSkills()
	}

	switch cfg.Agent.Type {
	case "", config.AgentTypeReact:
		return executor, executor, nil
	case config.AgentTypePlanExecute:
		parentManager, err := newContextManager(cfg.Context)
		if err != nil {
			return nil, nil, err
		}
		var planCfg *planexecute.Config
		if cfg.Agent.Plan != nil {
			planCfg = &planexecute.Config{
				MaxPlanSteps:     cfg.Agent.Plan.MaxSteps,
				ExecutorMaxSteps: cfg.Agent.Plan.ExecutorMaxSteps,
				MaxReplans:       cfg.Agent.Plan.MaxReplans,
			}
		}
		agent := planexecute.NewAgent(llm, executor, parentManager, planCfg)
		return agent, executor, nil
	default:
		return nil, nil, fmt.Errorf("unsupported agent type %q", cfg.Agent.Type)
	}
}

// CheckRuntime verifies credentials, sandbox prerequisites, and remote tool providers.
func CheckRuntime(ctx context.Context, cfg *config.Config) error {
	if _, err := newModel(ctx, cfg.Model); err != nil {
		return err
	}
	for i, tool := range cfg.Tools {
		if tool.Provider != config.ToolProviderBuiltin || tool.Name != config.BuiltinTerminal || tool.Sandbox == nil || !tool.Sandbox.Enabled {
			continue
		}
		if goruntime.GOOS != "linux" {
			return fmt.Errorf("tools[%d]: terminal sandbox requires Linux", i)
		}
		path := tool.Sandbox.BwrapPath
		if path == "" {
			path = "bwrap"
		}
		if _, err := exec.LookPath(path); err != nil {
			return fmt.Errorf("tools[%d]: find bubblewrap %q: %w", i, path, err)
		}
		for _, path := range tool.Sandbox.ReadOnlyPaths {
			if _, err := os.Stat(os.ExpandEnv(path)); err != nil {
				return fmt.Errorf("tools[%d]: access read-only sandbox path %q: %w", i, path, err)
			}
		}
		for _, path := range tool.Sandbox.WritablePaths {
			if err := checkWritablePath(os.ExpandEnv(path)); err != nil {
				return fmt.Errorf("tools[%d]: access writable sandbox path %q: %w", i, path, err)
			}
		}
	}

	probe := react.NewAgent(nil, cfg.Agent.ModelMaxTokensK, nil)
	remote := *cfg
	remote.Tools = nil
	for _, tool := range cfg.Tools {
		if tool.Provider == config.ToolProviderGRPC || tool.Provider == config.ToolProviderMCP {
			remote.Tools = append(remote.Tools, tool)
		}
	}
	resources, err := loadToolProviders(ctx, probe, &remote, os.DirFS("."))
	if err != nil {
		return err
	}
	closeResources(resources)
	return nil
}

func checkWritablePath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		probe, err := os.CreateTemp(path, ".goatc-write-check-*")
		if err != nil {
			return err
		}
		name := probe.Name()
		if err := probe.Close(); err != nil {
			_ = os.Remove(name)
			return err
		}
		return os.Remove(name)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	return file.Close()
}

func newModel(ctx context.Context, cfg config.Model) (model.AgenticModel, error) {
	apiKey := os.Getenv(cfg.APIKeyEnv)
	if apiKey == "" {
		return nil, fmt.Errorf("environment variable %s is required", cfg.APIKeyEnv)
	}
	maxTokens := cfg.MaxOutputTokens

	switch strings.ToLower(cfg.Provider) {
	case "openai":
		modelConfig := &agenticopenai.ResponsesConfig{
			APIKey:  apiKey,
			BaseURL: cfg.BaseURL,
			Model:   cfg.Name,
		}
		if maxTokens > 0 {
			modelConfig.MaxTokens = &maxTokens
		}
		return agenticopenai.NewResponsesModel(ctx, modelConfig)
	case "claude", "anthropic":
		if maxTokens <= 0 {
			maxTokens = 4096
		}
		return agenticclaude.New(ctx, &agenticclaude.Config{
			APIKey:    apiKey,
			BaseURL:   cfg.BaseURL,
			Model:     cfg.Name,
			MaxTokens: maxTokens,
		})
	case "gemini":
		clientConfig := &genai.ClientConfig{APIKey: apiKey}
		if cfg.BaseURL != "" {
			clientConfig.HTTPOptions.BaseURL = cfg.BaseURL
		}
		client, err := genai.NewClient(ctx, clientConfig)
		if err != nil {
			return nil, fmt.Errorf("create Gemini client: %w", err)
		}
		modelConfig := &agenticgemini.Config{Client: client, Model: cfg.Name}
		if maxTokens > 0 {
			modelConfig.MaxTokens = &maxTokens
		}
		return agenticgemini.New(ctx, modelConfig)
	default:
		return nil, fmt.Errorf("unsupported model provider %q", cfg.Provider)
	}
}

func newContextManager(cfg config.Context) (*contextmgr.Manager, error) {
	switch strings.ToLower(cfg.Backend) {
	case "ram":
		return ram.NewRAMContextManager(), nil
	case "file":
		return filecontext.NewFileContextManager(cfg.Path), nil
	case "sqlite":
		manager, err := sqlitecontext.NewSQLiteContextManager(cfg.Path)
		if err != nil {
			return nil, fmt.Errorf("create SQLite context manager: %w", err)
		}
		return manager, nil
	default:
		return nil, fmt.Errorf("unsupported context backend %q", cfg.Backend)
	}
}

func extractPlugins(assets fs.FS) (string, error) {
	dir, err := os.MkdirTemp("", "goatc-plugins-")
	if err != nil {
		return "", fmt.Errorf("create plugin cache: %w", err)
	}
	entries, err := fs.ReadDir(assets, "plugins")
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("read embedded plugins: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := fs.ReadFile(assets, "plugins/"+entry.Name())
		if err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("read plugin %s: %w", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dir, entry.Name()), data, 0o700); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("extract plugin %s: %w", entry.Name(), err)
		}
	}
	return dir, nil
}
