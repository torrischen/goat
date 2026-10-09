package runtime

import (
	"context"
	"testing"

	"github.com/torrischen/goat/agent/react"
	"github.com/torrischen/goat/goatc/config"
)

func TestNewAgentSelectsConfiguredType(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{
			name: "empty type defaults to React",
			cfg: &config.Config{
				Agent:   config.Agent{ModelMaxTokensK: 128},
				Context: config.Context{Backend: "ram"},
			},
		},
		{
			name: "React",
			cfg: &config.Config{
				Agent:   config.Agent{Type: config.AgentTypeReact, ModelMaxTokensK: 128},
				Context: config.Context{Backend: "ram"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent, executor, err := newAgent(context.Background(), nil, test.cfg)
			if err != nil {
				t.Fatalf("newAgent() error = %v", err)
			}
			if executor == nil {
				t.Fatal("newAgent() executor = nil")
			}
			if _, ok := agent.(*react.Agent); !ok {
				t.Fatalf("newAgent() type = %T, want *react.Agent", agent)
			}
		})
	}
}
