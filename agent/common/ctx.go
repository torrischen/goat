package common

import (
	"context"
	"sync/atomic"

	"github.com/torrischen/goat/util"
)

type AgentDoMetaKey string

const (
	InternalToolPlanMetaKey AgentDoMetaKey = "current_plan"
)

func (admk AgentDoMetaKey) String() string {
	return string(admk)
}

type AgentContext struct {
	context.Context
	meta            *util.SafeMap[AgentDoMetaKey, any]
	interruptSignal atomic.Int32
}

func NewAgentContext(ctx context.Context) *AgentContext {
	return &AgentContext{
		Context: ctx,
		meta:    util.NewSafeMap[AgentDoMetaKey, any](),
	}
}

func (ac *AgentContext) SetMeta(key AgentDoMetaKey, value any) {
	ac.meta.Set(key, value)
}

func (ac *AgentContext) GetMeta(key AgentDoMetaKey) any {
	return ac.meta.Get(key)
}

func (ac *AgentContext) DeleteMeta(key AgentDoMetaKey) {
	ac.meta.Delete(key)
}

func (ac *AgentContext) GetAllMeta() map[AgentDoMetaKey]any {
	return ac.meta.Snapshot()
}

func (ac *AgentContext) signalInterrupt() {
	if ac == nil {
		return
	}
	ac.interruptSignal.Store(1)
}

// ConsumeInterruptSignal reports whether the current agent loop should stop
// before the next Think call. The signal is consumed when read.
func ConsumeInterruptSignal(ac *AgentContext) bool {
	if ac == nil {
		return false
	}
	return ac.interruptSignal.Swap(0) != 0
}
