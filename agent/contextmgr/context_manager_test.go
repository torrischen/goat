package contextmgr_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	"github.com/torrischen/goat/agent/message"
)

func mustJSONBytes(value any) json.RawMessage {
	encoded, _ := sonic.Marshal(value)
	return encoded
}

type managerFactory struct {
	name string
	new  func(*testing.T) *contextmgr.Manager
}

func managerFactories() []managerFactory {
	return []managerFactory{
		{
			name: "ram",
			new: func(*testing.T) *contextmgr.Manager {
				return ram.NewRAMContextManager()
			},
		},
	}
}

func TestManagerCreateWithUIDIsAtomic(t *testing.T) {
	manager := ram.NewRAMContextManager()
	ctx := context.Background()
	const uid = common.ContextUID("fixed-context")
	const contenders = 12
	var wg sync.WaitGroup
	results := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- manager.CreateWithUID(ctx, uid, []*message.Message{message.UserMessage("initial")})
		}()
	}
	wg.Wait()
	close(results)
	var successes int
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("CreateWithUID() error = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("CreateWithUID() successes = %d, want 1", successes)
	}
}

func TestManagerCreateLoadAppendIsolation(t *testing.T) {
	for _, factory := range managerFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			ctx := context.Background()
			manager := factory.new(t)

			initial := []*message.Message{message.UserMessage("initial")}
			contextUID, err := manager.Create(ctx, initial)
			if err != nil || contextUID == "" {
				t.Fatalf("Create() = %q, %v", contextUID, err)
			}
			// Mutating the caller's slice must not affect stored state.
			initial[0].Blocks[0].Text.Text = "mutated input"

			loaded := mustLoad(t, manager, contextUID)
			if len(loaded) != 1 || messageText(loaded[0]) != "initial" {
				t.Fatalf("Load() = %+v", loaded)
			}
			// Mutating the loaded slice must not affect stored state.
			loaded[0].Blocks[0].Text.Text = "mutated load"
			reloaded := mustLoad(t, manager, contextUID)
			if messageText(reloaded[0]) != "initial" {
				t.Fatal("Load exposed stored state")
			}

			if err := manager.Append(ctx, contextUID, message.UserMessage("next")); err != nil {
				t.Fatalf("Append() error = %v", err)
			}
			assertTexts(t, mustLoad(t, manager, contextUID), []string{"initial", "next"})

			if err := manager.Append(ctx, contextUID, nil); !errors.Is(err, contextmgr.ErrInvalidMessage) {
				t.Fatalf("Append(nil) error = %v", err)
			}
			if _, err := manager.Load(ctx, "missing"); !errors.Is(err, contextmgr.ErrContextNotFound) {
				t.Fatalf("Load(missing) error = %v", err)
			}

			if err := manager.Delete(ctx, contextUID); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Load(ctx, contextUID); !errors.Is(err, contextmgr.ErrContextNotFound) {
				t.Fatalf("Load(deleted) error = %v", err)
			}
			if err := manager.Delete(ctx, contextUID); err != nil {
				t.Fatalf("Delete was not idempotent: %v", err)
			}
		})
	}
}

func TestManagerDoesNotReuseConsumedPendingMessages(t *testing.T) {
	ctx := context.Background()
	manager := ram.NewRAMContextManager()
	contextUID, err := manager.Create(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, text := range []string{"first pending", "second pending"} {
		if err := manager.Enqueue(ctx, contextUID, []*message.Message{message.UserMessage(text)}); err != nil {
			t.Fatal(err)
		}
		result, err := manager.CommitTurn(ctx, contextUID, nil)
		if err != nil {
			t.Fatal(err)
		}
		assertTexts(t, result.AppliedPendingMessages, []string{text})
	}

	assertTexts(t, mustLoad(t, manager, contextUID), []string{"first pending", "second pending"})
}

func TestManagerPreservesGeminiThoughtSignatureType(t *testing.T) {
	const signatureKey = "_eino_ext_agentic_gemini_thought_signature"
	want := []byte{0x00, 0x01, 0x7f, 0x80, 0xff}

	for _, factory := range managerFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			ctx := context.Background()
			manager := factory.new(t)
			block := common.ReasoningBlock("thinking")
			block.Provider = map[string]json.RawMessage{signatureKey: mustJSONBytes(want)}
			contextUID, err := manager.Create(ctx, []*message.Message{{
				Role:   message.RoleAssistant,
				Blocks: []*message.ContentBlock{block},
			}})
			if err != nil {
				t.Fatal(err)
			}

			loaded := mustLoad(t, manager, contextUID)
			got := loaded[0].Blocks[0].Provider[signatureKey]
			if !bytes.Equal(got, mustJSONBytes(want)) {
				t.Fatalf("thought signature = %v, want %v", got, want)
			}
		})
	}
}

func TestManagerPendingAndReplaceContract(t *testing.T) {
	for _, factory := range managerFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			ctx := context.Background()
			manager := factory.new(t)
			contextUID, err := manager.Create(ctx, []*message.Message{
				message.SystemMessage("system"),
			})
			if err != nil {
				t.Fatal(err)
			}

			steering := []*message.Message{
				message.UserMessage("steer one"),
				message.UserMessage("steer two"),
			}
			if err := manager.Enqueue(ctx, contextUID, steering); err != nil {
				t.Fatal(err)
			}
			steering[0] = nil
			assertTexts(t, mustLoad(t, manager, contextUID), []string{"system"})

			if err := manager.Replace(ctx, contextUID, []*message.Message{
				message.SystemMessage("replacement"),
			}); err != nil {
				t.Fatal(err)
			}
			result, err := manager.CommitTurn(ctx, contextUID, []*message.Message{
				common.AssistantTextMessage("turn"),
			})
			if err != nil {
				t.Fatal(err)
			}
			assertTexts(t, result.AppliedPendingMessages, []string{"steer one", "steer two"})
			result.AppliedPendingMessages[0] = nil
			assertTexts(t, mustLoad(t, manager, contextUID), []string{
				"replacement", "turn", "steer one", "steer two",
			})

			result, err = manager.CommitTurn(ctx, contextUID, nil)
			if err != nil || len(result.AppliedPendingMessages) != 0 {
				t.Fatalf("second CommitTurn() = %+v, %v", result, err)
			}
			if err := manager.Enqueue(ctx, contextUID, []*message.Message{
				common.AssistantTextMessage("invalid"),
			}); !errors.Is(err, contextmgr.ErrInvalidPendingMessage) {
				t.Fatalf("Enqueue(non-user) error = %v", err)
			}
		})
	}
}

func TestManagerConcurrentUpdatesDoNotLoseMessages(t *testing.T) {
	const updatesPerKind = 8

	for _, factory := range managerFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			ctx := context.Background()
			manager := factory.new(t)
			contextUID, err := manager.Create(ctx, []*message.Message{
				message.SystemMessage("system"),
			})
			if err != nil {
				t.Fatal(err)
			}

			var wg sync.WaitGroup
			errs := make(chan error, updatesPerKind*2)
			for i := 0; i < updatesPerKind; i++ {
				i := i
				wg.Add(2)
				go func() {
					defer wg.Done()
					errs <- manager.Append(
						ctx,
						contextUID,
						message.UserMessage(fmt.Sprintf("append-%d", i)),
					)
				}()
				go func() {
					defer wg.Done()
					errs <- manager.Enqueue(ctx, contextUID, []*message.Message{
						message.UserMessage(fmt.Sprintf("pending-%d", i)),
					})
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}

			if got := len(mustLoad(t, manager, contextUID)); got != updatesPerKind+1 {
				t.Fatalf("committed message count = %d", got)
			}
			result, err := manager.CommitTurn(ctx, contextUID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(result.AppliedPendingMessages); got != updatesPerKind {
				t.Fatalf("applied pending count = %d", got)
			}
			assertUniquePrefixedTexts(t, mustLoad(t, manager, contextUID), "append-", updatesPerKind)
			assertUniquePrefixedTexts(t, result.AppliedPendingMessages, "pending-", updatesPerKind)
		})
	}
}

func TestManagerSettleRunOutcomes(t *testing.T) {
	outcomes := []contextmgr.RunOutcome{
		contextmgr.RunOutcomeCompleted,
		contextmgr.RunOutcomeInterrupted,
		contextmgr.RunOutcomeCanceled,
		contextmgr.RunOutcomeFailed,
	}

	for _, factory := range managerFactories() {
		factory := factory
		for _, outcome := range outcomes {
			outcome := outcome
			t.Run(factory.name+"/"+string(outcome), func(t *testing.T) {
				ctx := context.Background()
				manager := factory.new(t)
				runUID := common.RunUID("run-" + string(outcome))
				user := runStartMessage("request", runUID)
				contextUID, err := manager.Create(ctx, []*message.Message{
					message.SystemMessage("system"),
					user,
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := manager.Enqueue(ctx, contextUID, []*message.Message{
					message.UserMessage("pending"),
				}); err != nil {
					t.Fatal(err)
				}

				args := &contextmgr.SettleRunArgs{
					Signature: common.RunSignature{ContextUID: contextUID, RunUID: runUID},
					Outcome:   outcome,
				}
				if outcome == contextmgr.RunOutcomeCompleted {
					args.FinalMessage = common.AssistantTextMessage("answer")
				}
				if err := manager.SettleRun(ctx, args); err != nil {
					t.Fatal(err)
				}
				if err := manager.SettleRun(ctx, args); err != nil {
					t.Fatalf("SettleRun was not idempotent: %v", err)
				}

				if outcome == contextmgr.RunOutcomeCompleted {
					assertTexts(t, mustLoad(t, manager, contextUID), []string{"system", "request", "answer"})
					if err := manager.Enqueue(ctx, contextUID, []*message.Message{
						message.UserMessage("late"),
					}); !errors.Is(err, contextmgr.ErrConversationFinalized) {
						t.Fatalf("Enqueue after final error = %v", err)
					}
				} else {
					// Non-completed runs retain committed and pending state.
					assertTexts(t, mustLoad(t, manager, contextUID), []string{"system", "request"})
				}

				forkedUID, err := manager.Fork(ctx, args.Signature)
				if err != nil {
					t.Fatal(err)
				}
				result, err := manager.CommitTurn(ctx, forkedUID, nil)
				if err != nil || len(result.AppliedPendingMessages) != 0 {
					t.Fatalf("fork inherited pending messages: %+v, %v", result, err)
				}
			})
		}
	}
}

func TestManagerReplaceInvalidatesPreviousForkPoints(t *testing.T) {
	ctx := context.Background()
	manager := ram.NewRAMContextManager()
	runUID := common.RunUID("run-before-replace")
	user := runStartMessage("request", runUID)
	contextUID, err := manager.Create(ctx, []*message.Message{user})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
		Signature: common.RunSignature{ContextUID: contextUID, RunUID: runUID},
		Outcome:   contextmgr.RunOutcomeInterrupted,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Replace(ctx, contextUID, []*message.Message{
		message.SystemMessage("compressed"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Fork(ctx, common.RunSignature{ContextUID: contextUID, RunUID: runUID}); !errors.Is(err, contextmgr.ErrRunNotSettled) {
		t.Fatalf("Fork after Replace error = %v, want ErrRunNotSettled", err)
	}
}

func TestManagerOnlyRetainsLatestForkPoint(t *testing.T) {
	ctx := context.Background()
	manager := ram.NewRAMContextManager()
	contextUID, err := manager.Create(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"first", "second"} {
		runUID := common.RunUID("run-" + name)
		if err := manager.Append(ctx, contextUID, runStartMessage(name, runUID)); err != nil {
			t.Fatal(err)
		}
		if err := manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
			Signature: common.RunSignature{ContextUID: contextUID, RunUID: runUID},
			Outcome:   contextmgr.RunOutcomeInterrupted,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := manager.Fork(ctx, common.RunSignature{ContextUID: contextUID, RunUID: "run-first"}); !errors.Is(err, contextmgr.ErrRunNotSettled) {
		t.Fatalf("old Fork error = %v, want ErrRunNotSettled", err)
	}
	if _, err := manager.Fork(ctx, common.RunSignature{ContextUID: contextUID, RunUID: "run-second"}); err != nil {
		t.Fatalf("latest Fork error = %v", err)
	}
}

func TestManagerRunUIDIsScopedToContext(t *testing.T) {
	ctx := context.Background()
	store := ram.NewRAMStore()
	manager := contextmgr.NewManager(store)
	const runUID = common.RunUID("reused-run")

	first, err := manager.Create(ctx, []*message.Message{
		runStartMessage("context one", runUID),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create(ctx, []*message.Message{
		runStartMessage("context two", runUID),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, contextUID := range []common.ContextUID{first, second} {
		if err := manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
			Signature: common.RunSignature{ContextUID: contextUID, RunUID: runUID},
			Outcome:   contextmgr.RunOutcomeInterrupted,
		}); err != nil {
			t.Fatalf("SettleRun(%s) error = %v", contextUID, err)
		}
	}

	firstFork, err := manager.Fork(ctx, common.RunSignature{ContextUID: first, RunUID: runUID})
	if err != nil {
		t.Fatal(err)
	}
	assertTexts(t, mustLoad(t, manager, firstFork), []string{"context one"})
	secondFork, err := manager.Fork(ctx, common.RunSignature{ContextUID: second, RunUID: runUID})
	if err != nil {
		t.Fatal(err)
	}
	assertTexts(t, mustLoad(t, manager, secondFork), []string{"context two"})

	const reusedContext = common.ContextUID("reused-context")
	if err := manager.CreateWithUID(ctx, reusedContext, []*message.Message{
		runStartMessage("old incarnation", runUID),
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
		Signature: common.RunSignature{ContextUID: reusedContext, RunUID: runUID},
		Outcome:   contextmgr.RunOutcomeInterrupted,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Delete(ctx, reusedContext); err != nil {
		t.Fatal(err)
	}
	if err := manager.CreateWithUID(ctx, reusedContext, []*message.Message{
		runStartMessage("new incarnation", runUID),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Fork(ctx, common.RunSignature{ContextUID: reusedContext, RunUID: runUID}); !errors.Is(err, contextmgr.ErrRunNotSettled) {
		t.Fatalf("Fork() reused an old context incarnation: %v", err)
	}
}

func runStartMessage(text string, runUID common.RunUID) *message.Message {
	message := message.UserMessage(text)
	common.MarkRunStart(message, runUID)
	return message
}

func mustLoad(
	t *testing.T,
	manager *contextmgr.Manager,
	contextUID common.ContextUID,
) []*message.Message {
	t.Helper()
	messages, err := manager.Load(context.Background(), contextUID)
	if err != nil {
		t.Fatal(err)
	}
	return messages
}

func assertUniquePrefixedTexts(
	t *testing.T,
	messages []*message.Message,
	prefix string,
	want int,
) {
	t.Helper()
	seen := make(map[string]struct{})
	for _, message := range messages {
		text := messageText(message)
		if len(text) >= len(prefix) && text[:len(prefix)] == prefix {
			seen[text] = struct{}{}
		}
	}
	if len(seen) != want {
		t.Fatalf("unique %q messages = %d, want %d", prefix, len(seen), want)
	}
}

func assertTexts(t *testing.T, messages []*message.Message, want []string) {
	t.Helper()
	if len(messages) != len(want) {
		t.Fatalf("message count = %d, want %d", len(messages), len(want))
	}
	for i, message := range messages {
		if got := messageText(message); got != want[i] {
			t.Fatalf("message[%d] text = %q, want %q", i, got, want[i])
		}
	}
}

func messageText(message *message.Message) string {
	if message == nil {
		return ""
	}
	for _, block := range message.Blocks {
		if block == nil {
			continue
		}
		if block.Text != nil {
			return block.Text.Text
		}
		if block.Text != nil {
			return block.Text.Text
		}
	}
	return ""
}
