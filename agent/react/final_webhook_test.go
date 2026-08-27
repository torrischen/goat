package react

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	"github.com/torrischen/goat/agent/message"
)

func TestFinalAnswerWebhookIncludesRunSignature(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	payloads := make(chan common.FinalAnswerWebhookPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload common.FinalAnswerWebhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		payloads <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	agent := NewAgent(&scriptedEventModel{responses: [][]*message.Message{{
		common.AssistantTextMessage("done"),
	}}}, 128, ram.NewRAMContextManager())
	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "answer directly"},
		FinalAnswerWebhook: &common.FinalAnswerWebhookConfig{
			URL: server.URL,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	readAllEvents(t, ctx, eventStream)

	select {
	case payload := <-payloads:
		if payload.ContextUID != signature.ContextUID || payload.RunUID != signature.RunUID {
			t.Fatalf("webhook signature = %s/%s, want %s/%s",
				payload.ContextUID, payload.RunUID, signature.ContextUID, signature.RunUID)
		}
	case <-ctx.Done():
		t.Fatalf("webhook payload was not received: %v", ctx.Err())
	}
}
