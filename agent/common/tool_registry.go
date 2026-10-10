package common

import "context"

// ToolRegistry manages a searchable pool of tools.
// Implementations can use different backends (in-memory, database, vector database, etc.)
// to store and search tools based on task descriptions.
type ToolRegistry interface {
	// Add adds tools to the registry
	Add(ctx context.Context, tools ...Tool) error

	// Search searches for relevant tools based on the task description.
	// Returns up to 'limit' tools that are most relevant to the task.
	// If limit <= 0, the implementation decides the default limit.
	Search(ctx context.Context, task string, limit int) ([]Tool, error)

	// Get retrieves a tool by its name
	Get(ctx context.Context, name string) (Tool, error)

	// List returns all tools in the registry
	List(ctx context.Context) ([]Tool, error)

	// Count returns the total number of tools in the registry
	Count(ctx context.Context) (int, error)
}
