package compression

import "github.com/torrischen/goat/agent/message"

func messageTokens(msg *message.Message) (int, int, int) {
	return msg.Tokens()
}

func assistantText(msg *message.Message) string {
	return msg.Text()
}

func messagePlainText(msg *message.Message) string {
	return msg.PlainText()
}

func functionToolResultText(result *message.ToolResult) string {
	return result.Text()
}
