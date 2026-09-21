package common

// ContextUID uniquely identifies a managed conversation.
type ContextUID string

func (id ContextUID) String() string {
	return string(id)
}
