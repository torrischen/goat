package compression

import "github.com/torrischen/goat/agent/message"

// mergeSameToolResultMessages coalesces result-only messages for the same
// tool into one user message without changing their result payloads. Each
// invocation remains a separate tool-result block, so its call ID and
// multimodal content are retained.
//
// Groups are emitted at the last result position. This keeps all corresponding
// tool calls before the merged results. A group never crosses a message that is
// protected by the compression policy (system/user/final-answer/skill
// messages), and protected skill results are never merged.
func mergeSameToolResultMessages(messages []*message.Message) []*message.Message {
	return mergeSameToolResultMessagesWithCallNames(messages, collectFunctionToolCallNames(messages))
}

func mergeSameToolResultMessagesWithCallNames(
	messages []*message.Message,
	callNames map[string]string,
) []*message.Message {
	if len(messages) < 2 {
		return messages
	}

	type groupKey struct {
		segment int
		name    string
	}
	type blockCandidate struct {
		key groupKey
		ok  bool
	}

	protectedSkillCallIDs := collectProtectedSkillCallIDs(messages)
	candidates := make([][]blockCandidate, len(messages))
	messageCounts := make(map[groupKey]int)
	lastIndexes := make(map[groupKey]int)
	segment := 0

	for messageIndex, msg := range messages {
		if !isDiscardableDetailedMessage(msg, protectedSkillCallIDs) {
			// Do not move ordinary tool results across durable conversation
			// boundaries while grouping them.
			segment++
			continue
		}
		if _, ok := functionToolResultsOnly(msg); !ok {
			continue
		}

		candidates[messageIndex] = make([]blockCandidate, len(msg.Blocks))
		keysInMessage := make(map[groupKey]struct{})
		for blockIndex, block := range msg.Blocks {
			if block == nil || block.Kind != message.BlockToolResult || block.ToolResult == nil {
				continue
			}
			result := block.ToolResult
			name := resolvedFunctionToolResultName(result, callNames)
			if name == "" || isProtectedSkillTool(name) {
				continue
			}
			if _, protected := protectedSkillCallIDs[result.CallID]; result.CallID != "" && protected {
				continue
			}

			key := groupKey{segment: segment, name: name}
			candidates[messageIndex][blockIndex] = blockCandidate{key: key, ok: true}
			keysInMessage[key] = struct{}{}
		}
		for key := range keysInMessage {
			messageCounts[key]++
			lastIndexes[key] = messageIndex
		}
	}

	hasMerge := false
	for _, count := range messageCounts {
		if count > 1 {
			hasMerge = true
			break
		}
	}
	if !hasMerge {
		return messages
	}

	groupedBlocks := make(map[groupKey][]*message.ContentBlock, len(messageCounts))
	for messageIndex, descriptors := range candidates {
		for blockIndex, descriptor := range descriptors {
			if !descriptor.ok || messageCounts[descriptor.key] < 2 {
				continue
			}
			groupedBlocks[descriptor.key] = append(
				groupedBlocks[descriptor.key],
				messages[messageIndex].Blocks[blockIndex],
			)
		}
	}

	mergedMessages := make([]*message.Message, 0, len(messages))
	for messageIndex, msg := range messages {
		descriptors := candidates[messageIndex]
		if len(descriptors) == 0 {
			mergedMessages = append(mergedMessages, msg)
			continue
		}

		changed := false
		emitted := make(map[groupKey]struct{})
		blocks := make([]*message.ContentBlock, 0, len(msg.Blocks))
		for blockIndex, block := range msg.Blocks {
			descriptor := descriptors[blockIndex]
			if !descriptor.ok || messageCounts[descriptor.key] < 2 {
				blocks = append(blocks, block)
				continue
			}

			changed = true
			if lastIndexes[descriptor.key] != messageIndex {
				continue
			}
			if _, alreadyEmitted := emitted[descriptor.key]; alreadyEmitted {
				continue
			}
			emitted[descriptor.key] = struct{}{}
			blocks = append(blocks, groupedBlocks[descriptor.key]...)
		}
		if !changed {
			mergedMessages = append(mergedMessages, msg)
			continue
		}
		if len(blocks) == 0 {
			continue
		}

		// Clone only the message container. The original content/result blocks
		// are immutable inputs and can safely be retained by pointer.
		merged := *msg
		merged.Blocks = blocks
		mergedMessages = append(mergedMessages, &merged)
	}
	return mergedMessages
}

func collectFunctionToolCallNames(messages []*message.Message) map[string]string {
	callNames := make(map[string]string)
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		for _, block := range msg.Blocks {
			if block == nil || block.Kind != message.BlockToolCall || block.ToolCall == nil {
				continue
			}
			call := block.ToolCall
			if call.CallID != "" && call.Name != "" {
				callNames[call.CallID] = call.Name
			}
		}
	}
	return callNames
}

func resolvedFunctionToolResultName(
	result *message.ToolResult,
	callNames map[string]string,
) string {
	if result == nil {
		return ""
	}
	if result.Name != "" {
		return result.Name
	}
	return callNames[result.CallID]
}

// functionToolResultsOnly returns all tool results when msg has no other
// non-nil content block. This avoids changing mixed user messages.
func functionToolResultsOnly(msg *message.Message) ([]*message.ToolResult, bool) {
	if msg == nil || msg.Role != message.RoleUser {
		return nil, false
	}

	results := make([]*message.ToolResult, 0, len(msg.Blocks))
	for _, block := range msg.Blocks {
		if block == nil {
			continue
		}
		if block.Kind != message.BlockToolResult || block.ToolResult == nil {
			return nil, false
		}
		results = append(results, block.ToolResult)
	}
	return results, len(results) > 0
}
