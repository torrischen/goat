package mongodb

import (
	"github.com/torrischen/goat/agent/contextmgr"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// HeadDocument is the MongoDB representation of a context manager head.
// Additional fields may be added for persistence migrations without changing
// the contextmgr package contract.
type HeadDocument struct {
	ID           string                            `bson:"_id"`
	Generation   string                            `bson:"generation"`
	Version      uint64                            `bson:"version"`
	CommittedSeq uint64                            `bson:"committed_seq"`
	PendingStart uint64                            `bson:"pending_start"`
	PendingSeq   uint64                            `bson:"pending_seq"`
	Finalized    bool                              `bson:"finalized"`
	Runs         map[string]contextmgr.RunSnapshot `bson:"runs,omitempty"`
}

// MessageDocument is the MongoDB representation of a message log row.
type MessageDocument struct {
	ID      MessageDocumentID `bson:"_id"`
	UID     string            `bson:"uid"`
	Lane    string            `bson:"lane"`
	Seq     uint64            `bson:"seq"`
	Message bson.Raw          `bson:"message"`
}

// MessageDocumentID is the compound MongoDB key for a message log row.
type MessageDocumentID struct {
	UID  string `bson:"uid"`
	Lane string `bson:"lane"`
	Seq  uint64 `bson:"seq"`
}
