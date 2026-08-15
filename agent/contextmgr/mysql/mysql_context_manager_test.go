package mysql

import (
	"strings"
	"testing"

	"github.com/torrischen/goat/agent/contextmgr"

	"github.com/cloudwego/eino/schema"
)

func TestBuildDSN(t *testing.T) {
	for _, test := range []struct {
		host, user, database string
		port                 int
	}{
		{"", "user", "db", 3306},
		{"localhost", "user", "db", 0},
		{"localhost", "", "db", 3306},
		{"localhost", "user", "", 3306},
	} {
		if _, err := buildDSN(test.host, test.port, test.user, "pass", test.database); err == nil {
			t.Fatalf("buildDSN(%q, %d, %q, db=%q) succeeded", test.host, test.port, test.user, test.database)
		}
	}
	dsn, err := buildDSN("::1", 3306, "user", "p@ss", "database")
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"user:p@ss@tcp([::1]:3306)/database", "charset=utf8mb4", "parseTime=true"} {
		if !strings.Contains(dsn, part) {
			t.Fatalf("DSN %q does not contain %q", dsn, part)
		}
	}
}

func TestModelsAndStateCodec(t *testing.T) {
	if (contextConversation{}).TableName() != "goat_context_conversations" ||
		(contextEvent{}).TableName() != "goat_context_events" ||
		(contextCheckpoint{}).TableName() != "goat_context_checkpoints" ||
		(contextMessage{}).TableName() != "goat_context_messages" ||
		(pendingMessage{}).TableName() != "goat_context_pending_messages" ||
		(runSnapshot{}).TableName() != "goat_context_run_snapshots" {
		t.Fatal("unexpected table names")
	}
	state := contextmgr.NewState([]*schema.AgenticMessage{schema.UserAgenticMessage("hello")})
	state.Revision = 4
	payload, err := encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeState(payload)
	if err != nil || decoded.Revision != 4 || len(decoded.Messages) != 1 {
		t.Fatalf("decoded = %+v, %v", decoded, err)
	}
	if _, err := decodeState("not-json"); err == nil {
		t.Fatal("invalid payload decoded")
	}
}

func TestNewMysqlContextManagerRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewMysqlContextManager("", 3306, "user", "pass", "db"); err == nil {
		t.Fatal("invalid configuration accepted")
	}
	if _, err := NewMysqlStore("localhost", 0, "user", "pass", "db"); err == nil {
		t.Fatal("invalid Store configuration accepted")
	}
}
