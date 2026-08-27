package react

import (
	"fmt"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm"
	"github.com/torrischen/goat/streaming"
)

func (a *Agent) generateFinalAnswer(
	ctx *common.AgentContext,
	messages []*message.Message,
	specialRequirements []string,
	events streaming.Stream[common.AgentEvent],
	opts ...llm.CallOption,
) (*message.Message, *common.AgentUsage, error) {
	var promptText string
	if len(specialRequirements) > 0 {
		promptText = "Please provide a final answer to the user's question. Special requirements:\n"
		for i, requirement := range specialRequirements {
			promptText += fmt.Sprintf("%d. %s\n", i+1, requirement)
		}
	} else {
		promptText = "Please provide a final answer to the user's question based on the conversation history."
	}

	finalMessages := message.Clone(messages)
	finalMessages = append(finalMessages, message.UserMessage(promptText))
	finalOpts := append([]llm.CallOption{}, opts...)
	finalOpts = append(finalOpts, llm.WithToolChoiceNone())

	raw, err := a.streamModelResponse(ctx, finalMessages, events, finalOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("final model call: %w", err)
	}

	promptTokens, completionTokens, cachedTokens := messageTokens(raw)
	usage := common.NewAgentUsage(promptTokens, cachedTokens, completionTokens)
	return raw, usage, nil
}
