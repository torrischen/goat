package compression

import (
	"strings"

	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/agent/tools"
)

const (
	compressionCheckpointPrefix        = "[Context checkpoint v1]\n"
	aggressiveCompressionSummaryPrefix = "[Previous conversation summary]: "
)

func splitSystemMessage(messages []*message.Message) (*message.Message, []*message.Message) {
	if len(messages) > 0 && messages[0] != nil && messages[0].Role == message.RoleSystem {
		return messages[0], messages[1:]
	}
	return nil, messages
}

func partitionCompressionMessages(
	messages []*message.Message,
	recentMessages int,
) (toCompress, toKeep []*message.Message) {
	if recentMessages < 0 {
		recentMessages = 0
	}
	recentStart := len(messages) - recentMessages
	if recentStart < 0 {
		recentStart = 0
	}

	protectedSkillCallIDs := collectProtectedSkillCallIDs(messages)
	toCompress = make([]*message.Message, 0, recentStart)
	toKeep = make([]*message.Message, 0, len(messages))
	compress := make([]bool, len(messages))
	for index, msg := range messages {
		compress[index] = index < recentStart && isDiscardableDetailedMessage(msg, protectedSkillCallIDs)
	}

	// Responses requires every function_call_output to have its function_call
	// in the same replayed input. If the recency boundary splits a pair, keep
	// both messages instead of sending an orphaned item.
	for _, indexes := range toolPairMessageIndexes(messages) {
		if len(indexes) < 2 {
			continue
		}
		compressPair := true
		for _, index := range indexes {
			if !compress[index] {
				compressPair = false
				break
			}
		}
		if !compressPair {
			for _, index := range indexes {
				compress[index] = false
			}
		}
	}

	for index, msg := range messages {
		if compress[index] {
			toCompress = append(toCompress, msg)
		} else {
			toKeep = append(toKeep, msg)
		}
	}
	return toCompress, toKeep
}

func toolPairMessageIndexes(messages []*message.Message) [][]int {
	byCallID := make(map[string][]int)
	for index, msg := range messages {
		if msg == nil {
			continue
		}
		seen := make(map[string]struct{})
		for _, block := range msg.Blocks {
			if block == nil {
				continue
			}
			var callID string
			switch block.Kind {
			case message.BlockToolCall:
				if block.ToolCall != nil {
					callID = block.ToolCall.CallID
				}
			case message.BlockToolResult:
				if block.ToolResult != nil {
					callID = block.ToolResult.CallID
				}
			}
			if callID != "" {
				seen[callID] = struct{}{}
			}
		}
		for callID := range seen {
			byCallID[callID] = append(byCallID[callID], index)
		}
	}

	pairs := make([][]int, 0, len(byCallID))
	for _, indexes := range byCallID {
		if len(indexes) > 1 {
			pairs = append(pairs, indexes)
		}
	}
	return pairs
}

func collectProtectedSkillCallIDs(messages []*message.Message) map[string]struct{} {
	callIDs := make(map[string]struct{})
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		for _, block := range msg.Blocks {
			if block == nil {
				continue
			}
			if call := block.ToolCall; block.Kind == message.BlockToolCall && call != nil && isProtectedSkillTool(call.Name) && call.CallID != "" {
				callIDs[call.CallID] = struct{}{}
			}
			if result := block.ToolResult; block.Kind == message.BlockToolResult && result != nil && isProtectedSkillTool(result.Name) && result.CallID != "" {
				callIDs[result.CallID] = struct{}{}
			}
		}
	}
	return callIDs
}

func isDiscardableDetailedMessage(msg *message.Message, protectedSkillCallIDs map[string]struct{}) bool {
	if msg == nil || msg.Role == message.RoleSystem {
		return false
	}
	if containsProtectedSkillOperation(msg, protectedSkillCallIDs) {
		return false
	}
	if isUserInputMessage(msg) || isFinalAnswerMessage(msg) {
		return false
	}
	return true
}

func containsProtectedSkillOperation(msg *message.Message, protectedSkillCallIDs map[string]struct{}) bool {
	if msg == nil {
		return false
	}
	for _, block := range msg.Blocks {
		if block == nil {
			continue
		}
		if call := block.ToolCall; block.Kind == message.BlockToolCall && call != nil {
			if isProtectedSkillTool(call.Name) {
				return true
			}
			if _, ok := protectedSkillCallIDs[call.CallID]; call.CallID != "" && ok {
				return true
			}
		}
		if result := block.ToolResult; block.Kind == message.BlockToolResult && result != nil {
			if isProtectedSkillTool(result.Name) {
				return true
			}
			if _, ok := protectedSkillCallIDs[result.CallID]; result.CallID != "" && ok {
				return true
			}
		}
	}
	return false
}

func isProtectedSkillTool(name string) bool {
	return name == tools.InternalToolLoadSkills || name == tools.InternalToolReadSpecifiedFileInSkill
}

func isUserInputMessage(msg *message.Message) bool {
	if msg == nil || msg.Role != message.RoleUser {
		return false
	}
	// Tool results also use the user role, so role alone is insufficient to
	// identify an actual user input.
	for _, block := range msg.Blocks {
		if block != nil && block.Kind == message.BlockToolResult {
			return false
		}
	}
	return true
}

func isFinalAnswerMessage(msg *message.Message) bool {
	if msg == nil || msg.Role != message.RoleAssistant {
		return false
	}
	if isCompressionArtifactMessage(msg) {
		return false
	}
	// In react, intermediate assistant messages contain tool calls. An
	// assistant message without a tool call is the answer returned to the user.
	for _, block := range msg.Blocks {
		if block != nil && block.Kind == message.BlockToolCall {
			return false
		}
	}
	return true
}

func isCompressionArtifactMessage(msg *message.Message) bool {
	text := assistantText(msg)
	return strings.HasPrefix(text, compressionCheckpointPrefix) ||
		strings.HasPrefix(text, aggressiveCompressionSummaryPrefix)
}
