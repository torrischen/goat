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
	opts ...llm.Option,
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
	opts ...llm.Option,
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
			if err := events.WriteWithContext(ctx, common.ContentDeltaEvent{
				Delta:       delta,
				ContentType: common.ContentTypeReasoning,
				Phase:       common.ContentPhaseReasoning,
			}); err != nil {
				return nil, err
			}
		}
		if err := writeTextDeltas(ctx, events, chunk); err != nil {
			return nil, err
		}
	}

	if len(chunks) == 0 {
		return nil, fmt.Errorf("model stream returned no messages")
	}
	msg := message.Concat(chunks)

	// Emit reasoning completed if there was reasoning content
	if reasoning := messageReasoning(msg); reasoning != "" {
		if err := events.WriteWithContext(ctx, common.ContentCompletedEvent{
			Content:     reasoning,
			ContentType: common.ContentTypeReasoning,
			Phase:       common.ContentPhaseReasoning,
		}); err != nil {
			return nil, err
		}
	}

	// Emit text completed for each text segment
	for _, segment := range msg.TextSegments() {
		// Map message.TextPhase to common.ContentPhase
		var phase common.ContentPhase
		switch segment.Phase {
		case message.TextPhaseFinalAnswer:
			phase = common.ContentPhaseFinalAnswer
		case message.TextPhaseCommentary:
			phase = common.ContentPhaseCommentary
		default:
			// TextSegments() always returns resolved phases, so this shouldn't happen
			phase = common.ContentPhaseFinalAnswer
		}
		if err := events.WriteWithContext(ctx, common.ContentCompletedEvent{
			Content:     segment.Text,
			ContentType: common.ContentTypeText,
			Phase:       phase,
		}); err != nil {
			return nil, err
		}
	}
	return msg, nil
}

// writeTextDeltas emits one delta per text block of a streamed chunk, carrying
// whatever phase the provider attached to it.
func writeTextDeltas(ctx *common.AgentContext, events streaming.Stream[common.AgentEvent], chunk *message.Message) error {
	for _, block := range chunk.Blocks {
		if block == nil || block.Kind != message.BlockText || block.Text == nil || block.Text.Text == "" {
			continue
		}
		phase := common.ContentPhase("")
		switch block.Text.Phase {
		case message.TextPhaseFinalAnswer:
			phase = common.ContentPhaseFinalAnswer
		case message.TextPhaseCommentary:
			phase = common.ContentPhaseCommentary
		}
		if err := events.WriteWithContext(ctx, common.ContentDeltaEvent{
			Delta:       block.Text.Text,
			ContentType: common.ContentTypeText,
			Phase:       phase,
		}); err != nil {
			return err
		}
	}
	return nil
}

func assistantMessageFromResponse(resp *message.Message) *message.Message {
	return resp
}
