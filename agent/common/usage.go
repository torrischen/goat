package common

type AgentUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func NewAgentUsage(promptTokens, cachedTokens, completionTokens int) *AgentUsage {
	if promptTokens == 0 && cachedTokens == 0 && completionTokens == 0 {
		return nil
	}
	return &AgentUsage{
		PromptTokens:     promptTokens,
		CachedTokens:     cachedTokens,
		CompletionTokens: completionTokens,
	}
}

func (u *AgentUsage) Clone() *AgentUsage {
	if u == nil {
		return nil
	}
	return &AgentUsage{
		PromptTokens:     u.PromptTokens,
		CachedTokens:     u.CachedTokens,
		CompletionTokens: u.CompletionTokens,
	}
}

func (u *AgentUsage) Add(other *AgentUsage) {
	if u == nil || other == nil {
		return
	}
	u.PromptTokens += other.PromptTokens
	u.CachedTokens += other.CachedTokens
	u.CompletionTokens += other.CompletionTokens
}
