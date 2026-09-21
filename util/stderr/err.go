package stderr

import "errors"

var (
	ErrFakeFunc   = errors.New("fake function")
	ErrDeprecated = errors.New("deprecated")

	ErrNoEmbeddings              = errors.New("no embeddings")
	ErrMilvusIDColumnType        = errors.New("id column type error")
	ErrMilvusTagColumnType       = errors.New("tag column type error")
	ErrMilvusEmbeddingColumnType = errors.New("embedding column type error")
	ErrMilvusTextColumnType      = errors.New("text column type error")
	ErrMilvusColumnNumber        = errors.New("column number error")
	ErrMilvusColumnNotFound      = errors.New("column not found")
	ErrNoReranker                = errors.New("no reranker")
	ErrNoRetriever               = errors.New("no retriever")
	ErrNoRetrieverWeightSum      = errors.New("retriever weight sum should be 1.0")

	ErrLLMEmptyResponse = errors.New("empty response")

	ErrUserRole = errors.New("user role error")

	ErrChatNotFound = errors.New("chat not found")
)
