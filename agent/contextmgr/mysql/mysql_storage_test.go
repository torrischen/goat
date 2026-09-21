package mysql

import (
	"reflect"
	"testing"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	"github.com/torrischen/goat/agent/message"
)

func TestModelTableNames(t *testing.T) {
	if (HeadModel{}).TableName() != "goat_context_heads" {
		t.Fatalf("head table name = %q", (HeadModel{}).TableName())
	}
	if (MessageModel{}).TableName() != "goat_context_messages" {
		t.Fatalf("message table name = %q", (MessageModel{}).TableName())
	}
}

func TestHeadAndMessageConversion(t *testing.T) {
	original := &contextmgr.Head{
		UID: "ctx-1", Generation: "gen-1", Version: 4,
		CommittedSeq: 12, PendingStart: 13, PendingSeq: 14, Finalized: true,
		Runs: map[common.RunUID]contextmgr.RunSnapshot{
			"run-1": {Outcome: contextmgr.RunOutcomeCompleted, CommittedSeq: 12},
		},
	}
	headModel, err := headToModel(original)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := modelToHead(headModel)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, restored) {
		t.Fatalf("head round trip = %+v, want %+v", restored, original)
	}

	row := contextmgr.MessageRow{
		UID: "ctx-1", Lane: contextmgr.LaneCommitted, Seq: 3,
		Message: &message.Message{Role: message.RoleUser, Blocks: []*message.ContentBlock{{Kind: message.BlockText, Text: &message.TextData{Text: "hello"}}}},
	}
	messageModel, err := messageToModel(row)
	if err != nil {
		t.Fatal(err)
	}
	restoredRow, err := modelToMessage(messageModel)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(row, *restoredRow) {
		t.Fatalf("message round trip = %+v, want %+v", *restoredRow, row)
	}
}

func TestHeadUpdateMapIncludesZeroValues(t *testing.T) {
	model := &HeadModel{Version: 2, RunsJSON: []byte("{}")}
	updates := headUpdateMap(model)
	for _, key := range []string{"generation", "version", "committed_seq", "pending_start", "pending_seq", "finalized", "runs_json"} {
		if _, ok := updates[key]; !ok {
			t.Fatalf("missing update field %q", key)
		}
	}
	if updates["finalized"] != false || updates["pending_start"] != uint64(0) {
		t.Fatal("zero-valued head fields were not preserved")
	}
}
