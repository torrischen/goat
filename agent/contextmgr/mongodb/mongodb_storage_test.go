package mongodb

import (
	"reflect"
	"testing"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMessageIndexModel(t *testing.T) {
	model := mongoMessageIndexModel()
	want := bson.D{{Key: "uid", Value: 1}, {Key: "lane", Value: 1}, {Key: "seq", Value: 1}}
	if !reflect.DeepEqual(model.Keys, want) {
		t.Fatalf("message index keys = %v, want %v", model.Keys, want)
	}
}

func TestHeadUpdateFieldsExcludesID(t *testing.T) {
	head := &contextmgr.Head{
		UID:          "context-1",
		Generation:   "generation-1",
		Version:      3,
		CommittedSeq: 4,
		PendingStart: 5,
		PendingSeq:   6,
		Finalized:    true,
		Runs: map[common.RunUID]contextmgr.RunSnapshot{
			"run-1": {Outcome: contextmgr.RunOutcomeCompleted, CommittedSeq: 4},
		},
	}

	fields := headUpdateFields(head)
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		if field.Key == "_id" {
			t.Fatal("headUpdateFields() included immutable _id")
		}
		seen[field.Key] = true
	}
	for _, key := range []string{"generation", "version", "committed_seq", "pending_start", "pending_seq", "finalized", "runs"} {
		if !seen[key] {
			t.Errorf("headUpdateFields() omitted %q", key)
		}
	}
}

func TestResolveCollectionNames(t *testing.T) {
	tests := []struct {
		name         string
		config       Config
		wantContexts string
		wantMessages string
	}{
		{
			name:         "defaults",
			wantContexts: defaultContextCollection,
			wantMessages: defaultMessageCollection,
		},
		{
			name:         "legacy collection",
			config:       Config{Collection: "conversation_data"},
			wantContexts: "conversation_data",
			wantMessages: "conversation_data_messages",
		},
		{
			name: "explicit collections",
			config: Config{
				Collection:        "ignored",
				ContextCollection: "context_heads",
				MessageCollection: "message_log",
			},
			wantContexts: "context_heads",
			wantMessages: "message_log",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contexts, messages := resolveCollectionNames(test.config)
			if contexts != test.wantContexts || messages != test.wantMessages {
				t.Fatalf("resolveCollectionNames() = (%q, %q), want (%q, %q)", contexts, messages, test.wantContexts, test.wantMessages)
			}
		})
	}
}
