package react

import (
	"errors"
	"fmt"
	"io"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm"
	"github.com/torrischen/goat/streaming"
)

type thinkArgs struct {
	Messages []*message.Message
}

type thinkResult struct {
	RawResponse      *message.Message
	IsCompressed     bool
	ModelUsage       *common.AgentUsage
	CompressionUsage *common.AgentUsage
}

func (a *Agent) think(
	ctx *common.AgentContext,
	args *thinkArgs,
	events streaming.Stream[common.AgentEvent],
	opts ...llm.CallOption,
) (*thinkResult, error) {
	result := &thinkResult{}

	raw, err := a.streamModelResponse(ctx, args.Messages, events, opts...)
	if err != nil {
		return nil, fmt.Errorf("think model call: %w", err)
	}

	promptTokens, completionTokens, cachedTokens := messageTokens(raw)
	result.ModelUsage = common.NewAgentUsage(promptTokens, cachedTokens, completionTokens)
	result.RawResponse = raw

	return result, nil
}

func (a *Agent) streamModelResponse(
	ctx *common.AgentContext,
	messages []*message.Message,
	events streaming.Stream[common.AgentEvent],
	opts ...llm.CallOption,
) (*message.Message, error) {
	reader, err := a.llmClient.Stream(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	chunks := make([]*message.Message, 0)
	for {
		chunk, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		if chunk == nil {
			continue
		}

		chunks = append(chunks, chunk)
		if delta := messageReasoning(chunk); delta != "" {
			if err := events.WriteWithContext(ctx, common.ReasoningDeltaEvent{Delta: delta}); err != nil {
				return nil, err
			}
		}
		if delta := assistantText(chunk); delta != "" {
			if err := events.WriteWithContext(ctx, common.AssistantTextDeltaEvent{Delta: delta}); err != nil {
				return nil, err
			}
		}
	}

	if len(chunks) == 0 {
		return nil, fmt.Errorf("model stream returned no messages")
	}
	msg := message.Concat(chunks)
	return msg, nil
}

func assistantMessageFromResponse(resp *message.Message) *message.Message {
	return resp
}
