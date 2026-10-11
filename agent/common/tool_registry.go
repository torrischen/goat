package common

import (
	"context"
	"sync"
)

// StableToolSet manages stable tools that are always available to the agent.
// These tools are exposed to the LLM and do not change during execution.
type StableToolSet struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewStableToolSet creates a new stable tool set
func NewStableToolSet() *StableToolSet {
	return &StableToolSet{
		tools: make(map[string]Tool),
	}
}

// Add adds tools to the stable set
func (s *StableToolSet) Add(tools ...Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, tool := range tools {
		if tool != nil {
			s.tools[tool.Name()] = tool
		}
	}
}

// Get retrieves a tool by name
func (s *StableToolSet) Get(name string) (Tool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tool, ok := s.tools[name]
	return tool, ok
}

// List returns all stable tools
func (s *StableToolSet) List() []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Tool, 0, len(s.tools))
	for _, tool := range s.tools {
		result = append(result, tool)
	}
	return result
}

// Has checks if a tool exists
func (s *StableToolSet) Has(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, ok := s.tools[name]
	return ok
}

// Remove removes a tool
func (s *StableToolSet) Remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.tools, name)
}

// Count returns the number of tools
func (s *StableToolSet) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.tools)
}

// DynamicToolRegistry is the interface for dynamic tool search and retrieval.
// Implementations can use vector databases, semantic search, or any other backend.
type DynamicToolRegistry interface {
	// Search finds tools matching the query
	Search(ctx context.Context, query string, limit int) ([]Tool, error)

	// Get retrieves a specific tool by name
	Get(ctx context.Context, name string) (Tool, error)

	// Add adds tools to the registry (for custom implementations)
	Add(ctx context.Context, tools ...Tool) error

	// Remove removes tools from the registry
	Remove(ctx context.Context, names ...string) error

	// List returns all available tools
	List(ctx context.Context) ([]Tool, error)

	// Count returns the total number of tools
	Count(ctx context.Context) (int, error)
}
