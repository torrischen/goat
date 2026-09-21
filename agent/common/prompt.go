package common

import (
	"github.com/torrischen/goat/agent/message"
)

type AgentUserInput struct {
	Text   string
	Images []*message.ContentBlock
}

func (u AgentUserInput) String() string {
	return u.Text
}
