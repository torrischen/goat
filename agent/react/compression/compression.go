// Package compression provides context-compression strategies for the React agent.
package compression

import (
	"context"
	"fmt"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/llm"
	"github.com/torrischen/goat/agent/message"
)

const (
	defaultPreciseRecentMessages    = 8
	defaultAggressiveRecentMessages = 4
)

// Compress applies the configured strategy to a React agent conversation.
// Model-based strategies use client with tools disabled; discard_half does not use the model.
func Compress(
	ctx context.Context,
	client llm.Client,
	messages []*message.Message,
	options common.CompressionOptions,
	opts ...llm.CallOption,
) ([]*message.Message, int, int, int, error) {
	normalized := normalizeOptions(options)
	switch normalized.Strategy {
	case common.CompressionStrategyDiscardHalf:
		return compressDiscardHalf(messages)
	case common.CompressionStrategyPrecise:
		if client == nil {
			return messages, 0, 0, 0, fmt.Errorf("compression model is nil")
		}
		return compressPrecise(ctx, client, messages, normalized.RecentMessages, opts...)
	default:
		if client == nil {
			return messages, 0, 0, 0, fmt.Errorf("compression model is nil")
		}
		return compressAggressive(ctx, client, messages, normalized.RecentMessages, opts...)
	}
}

func normalizeOptions(options common.CompressionOptions) common.CompressionOptions {
	normalized := options
	switch normalized.Strategy {
	case common.CompressionStrategyPrecise:
		if normalized.RecentMessages <= 0 {
			normalized.RecentMessages = defaultPreciseRecentMessages
		}
	case common.CompressionStrategyDiscardHalf:
		normalized.RecentMessages = 0
	case common.CompressionStrategyAggressive, "":
		normalized.Strategy = common.CompressionStrategyAggressive
		if normalized.RecentMessages <= 0 {
			normalized.RecentMessages = defaultAggressiveRecentMessages
		}
	default:
		normalized.Strategy = common.CompressionStrategyAggressive
		if normalized.RecentMessages <= 0 {
			normalized.RecentMessages = defaultAggressiveRecentMessages
		}
	}
	return normalized
}
