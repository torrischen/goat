package runtime

import (
	"context"
	"testing"

	"github.com/torrischen/goat/agent/planexecute"
	"github.com/torrischen/goat/agent/react"
	"github.com/torrischen/goat/goatc/config"
)

func TestNewAgentSelectsConfiguredType(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want any
	}{
		{
			name: "empty type defaults to React",
			cfg: &config.Config{
				Agent:   config.Agent{ModelMaxTokensK: 128},
				Context: config.Context{Backend: "ram"},
			},
			want: (*react.Agent)(nil),
		},
		{
			name: "React",
			cfg: &config.Config{
				Agent:   config.Agent{Type: config.AgentTypeReact, ModelMaxTokensK: 128},
				Context: config.Context{Backend: "ram"},
			},
			want: (*react.Agent)(nil),
		},
		{
			name: "plan and execute with defaults",
			cfg: &config.Config{
				Agent:   config.Agent{Type: config.AgentTypePlanExecute, ModelMaxTokensK: 128},
				Context: config.Context{Backend: "ram"},
			},
			want: (*planexecute.Agent)(nil),
		},
		{
			name: "plan and execute with configuration",
			cfg: &config.Config{
				Agent: config.Agent{
					Type:            config.AgentTypePlanExecute,
					ModelMaxTokensK: 128,
					Plan:            &config.PlanConfig{MaxSteps: 4, ExecutorMaxSteps: 3, MaxReplans: 1},
				},
				Context: config.Context{Backend: "ram"},
			},
			want: (*planexecute.Agent)(nil),
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
			switch test.want.(type) {
			case *react.Agent:
				if _, ok := agent.(*react.Agent); !ok {
					t.Fatalf("newAgent() type = %T, want *react.Agent", agent)
				}
			case *planexecute.Agent:
				if _, ok := agent.(*planexecute.Agent); !ok {
					t.Fatalf("newAgent() type = %T, want *planexecute.Agent", agent)
				}
			}
		})
	}
}
