package contextmgr_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	filectx "github.com/torrischen/goat/agent/contextmgr/file"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	"github.com/torrischen/goat/agent/contextmgr/sqlite"

	"github.com/cloudwego/eino/schema"
)

type managerFixture struct {
	manager *contextmgr.Manager
	store   contextmgr.Store
}

type managerFactory struct {
	name string
	new  func(*testing.T) managerFixture
}

func managerFactories() []managerFactory {
	return []managerFactory{
		{
			name: "ram",
			new: func(*testing.T) managerFixture {
				store := ram.NewRAMStore()
				return managerFixture{manager: contextmgr.NewManager(store), store: store}
			},
		},
		{
			name: "file",
			new: func(t *testing.T) managerFixture {
				store := filectx.NewFileStore(t.TempDir())
				return managerFixture{manager: contextmgr.NewManager(store), store: store}
			},
		},
		{
			name: "sqlite",
			new: func(t *testing.T) managerFixture {
				store, err := sqlite.NewSQLiteStore(filepath.Join(t.TempDir(), "context.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				return managerFixture{manager: contextmgr.NewManager(store), store: store}
			},
		},
	}
}

func TestStoreContract(t *testing.T) {
	for _, factory := range managerFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := factory.new(t)
			initial := contextmgr.NewState([]*schema.AgenticMessage{schema.UserAgenticMessage("initial")})

			contextUID, err := fixture.store.Create(ctx, initial)
			if err != nil || contextUID == "" {
				t.Fatalf("Create() = %q, %v", contextUID, err)
			}
			initial.Messages[0].ContentBlocks[0].UserInputText.Text = "mutated input"

			loaded, err := fixture.store.Load(ctx, contextUID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Revision != 1 || messageText(loaded.Messages[0]) != "initial" {
				t.Fatalf("Load() = %+v", loaded)
			}
			loaded.Messages[0].ContentBlocks[0].UserInputText.Text = "mutated load"
			reloaded, err := fixture.store.Load(ctx, contextUID)
			if err != nil || messageText(reloaded.Messages[0]) != "initial" {
				t.Fatal("Load exposed stored state")
			}

			events := []contextmgr.Event{{
				Type:     contextmgr.EventMessagesAppended,
				Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("next")},
			}}
			if err := fixture.store.Append(ctx, contextUID, 99, events); !errors.Is(err, contextmgr.ErrRevisionConflict) {
				t.Fatalf("Append(stale) error = %v", err)
			}
			if err := fixture.store.Append(ctx, contextUID, reloaded.Revision, events); err != nil {
				t.Fatalf("Append() error = %v", err)
			}
			events[0].Messages[0].ContentBlocks[0].UserInputText.Text = "mutated append input"
			stored, err := fixture.store.Load(ctx, contextUID)
			if err != nil || stored.Revision != 2 || messageText(stored.Messages[1]) != "next" {
				t.Fatalf("state after Append = %+v, %v", stored, err)
			}
			historical, err := fixture.store.LoadAt(ctx, contextUID, 1)
			if err != nil || len(historical.Messages) != 1 || messageText(historical.Messages[0]) != "initial" {
				t.Fatalf("LoadAt(1) = %+v, %v", historical, err)
			}

			missing := common.ContextUID("missing")
			if _, err := fixture.store.Load(ctx, missing); !errors.Is(err, contextmgr.ErrContextNotFound) {
				t.Fatalf("Load(missing) error = %v", err)
			}
			if err := fixture.store.Append(ctx, missing, 1, events); !errors.Is(err, contextmgr.ErrContextNotFound) {
				t.Fatalf("Append(missing) error = %v", err)
			}
			if _, err := fixture.store.LoadAt(ctx, contextUID, 99); !errors.Is(err, contextmgr.ErrRevisionNotFound) {
				t.Fatalf("LoadAt(future) error = %v", err)
			}
			if err := fixture.store.Append(ctx, contextUID, stored.Revision, nil); !errors.Is(err, contextmgr.ErrInvalidEvent) {
				t.Fatalf("Append(empty) error = %v", err)
			}
			if err := fixture.store.Delete(ctx, contextUID); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.Load(ctx, contextUID); !errors.Is(err, contextmgr.ErrContextNotFound) {
				t.Fatalf("Load(deleted) error = %v", err)
			}
			if err := fixture.store.Delete(ctx, contextUID); err != nil {
				t.Fatalf("Delete was not idempotent: %v", err)
			}
		})
	}
}

func TestManagerPendingAndReplaceContract(t *testing.T) {
	for _, factory := range managerFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := factory.new(t)
			contextUID, err := fixture.manager.Create(ctx, []*schema.AgenticMessage{
				schema.SystemAgenticMessage("system"),
			})
			if err != nil {
				t.Fatal(err)
			}

			steering := []*schema.AgenticMessage{
				schema.UserAgenticMessage("steer one"),
				schema.UserAgenticMessage("steer two"),
			}
			if err := fixture.manager.Enqueue(ctx, contextUID, steering); err != nil {
				t.Fatal(err)
			}
			steering[0] = nil
			assertTexts(t, mustLoad(t, fixture.manager, contextUID), []string{"system"})

			if err := fixture.manager.Replace(ctx, contextUID, []*schema.AgenticMessage{
				schema.SystemAgenticMessage("replacement"),
			}); err != nil {
				t.Fatal(err)
			}
			result, err := fixture.manager.CommitTurn(ctx, contextUID, []*schema.AgenticMessage{
				common.AssistantTextMessage("turn"),
			})
			if err != nil {
				t.Fatal(err)
			}
			assertTexts(t, result.AppliedPendingMessages, []string{"steer one", "steer two"})
			result.AppliedPendingMessages[0] = nil
			assertTexts(t, mustLoad(t, fixture.manager, contextUID), []string{
				"replacement", "turn", "steer one", "steer two",
			})

			result, err = fixture.manager.CommitTurn(ctx, contextUID, nil)
			if err != nil || len(result.AppliedPendingMessages) != 0 {
				t.Fatalf("second CommitTurn() = %+v, %v", result, err)
			}
			if err := fixture.manager.Enqueue(ctx, contextUID, []*schema.AgenticMessage{
				common.AssistantTextMessage("invalid"),
			}); !errors.Is(err, contextmgr.ErrInvalidPendingMessage) {
				t.Fatalf("Enqueue(non-user) error = %v", err)
			}
			if err := fixture.manager.Append(ctx, contextUID, nil); !errors.Is(err, contextmgr.ErrInvalidMessage) {
				t.Fatalf("Append(nil) error = %v", err)
			}
			if _, err := fixture.manager.Load(ctx, "missing"); !errors.Is(err, contextmgr.ErrContextNotFound) {
				t.Fatalf("Load(missing) error = %v", err)
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
			fixture := factory.new(t)
			contextUID, err := fixture.manager.Create(ctx, []*schema.AgenticMessage{
				schema.SystemAgenticMessage("system"),
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
					errs <- fixture.manager.Append(
						ctx,
						contextUID,
						schema.UserAgenticMessage(fmt.Sprintf("append-%d", i)),
					)
				}()
				go func() {
					defer wg.Done()
					errs <- fixture.manager.Enqueue(ctx, contextUID, []*schema.AgenticMessage{
						schema.UserAgenticMessage(fmt.Sprintf("pending-%d", i)),
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

			if got := len(mustLoad(t, fixture.manager, contextUID)); got != updatesPerKind+1 {
				t.Fatalf("committed message count = %d", got)
			}
			result, err := fixture.manager.CommitTurn(ctx, contextUID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(result.AppliedPendingMessages); got != updatesPerKind {
				t.Fatalf("applied pending count = %d", got)
			}
			assertUniquePrefixedTexts(t, mustLoad(t, fixture.manager, contextUID), "append-", updatesPerKind)
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
				fixture := factory.new(t)
				runUID := common.RunUID("run-" + string(outcome))
				user := runStartMessage("request", runUID)
				contextUID, err := fixture.manager.Create(ctx, []*schema.AgenticMessage{
					schema.SystemAgenticMessage("system"),
					user,
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.manager.Enqueue(ctx, contextUID, []*schema.AgenticMessage{
					schema.UserAgenticMessage("pending"),
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
				if err := fixture.manager.SettleRun(ctx, args); err != nil {
					t.Fatal(err)
				}
				if err := fixture.manager.SettleRun(ctx, args); err != nil {
					t.Fatalf("SettleRun was not idempotent: %v", err)
				}

				state, err := fixture.store.Load(ctx, contextUID)
				if err != nil {
					t.Fatal(err)
				}
				snapshot, exists := state.RunSnapshots[runUID]
				if !exists || snapshot.Outcome != outcome || snapshot.Revision == 0 || len(snapshot.Messages) != 0 {
					t.Fatalf("snapshot = %+v, exists %v", snapshot, exists)
				}
				if outcome == contextmgr.RunOutcomeCompleted {
					if len(state.PendingMessages) != 0 {
						t.Fatal("completed run retained pending messages")
					}
					assertTexts(t, state.Messages, []string{"system", "request", "answer"})
					if err := fixture.manager.Enqueue(ctx, contextUID, []*schema.AgenticMessage{
						schema.UserAgenticMessage("late"),
					}); !errors.Is(err, contextmgr.ErrConversationFinalized) {
						t.Fatalf("Enqueue after final error = %v", err)
					}
				} else if len(state.PendingMessages) != 1 {
					t.Fatalf("%s run pending count = %d", outcome, len(state.PendingMessages))
				}

				forkedUID, err := fixture.manager.Fork(ctx, args.Signature)
				if err != nil {
					t.Fatal(err)
				}
				result, err := fixture.manager.CommitTurn(ctx, forkedUID, nil)
				if err != nil || len(result.AppliedPendingMessages) != 0 {
					t.Fatalf("fork inherited pending messages: %+v, %v", result, err)
				}
			})
		}
	}
}

func TestManagerForkPreservesHistoricalSnapshots(t *testing.T) {
	for _, factory := range managerFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := factory.new(t)
			firstUser := runStartMessage("first request", "run-1")
			contextUID, err := fixture.manager.Create(ctx, []*schema.AgenticMessage{
				schema.SystemAgenticMessage("system v1"),
				firstUser,
			})
			if err != nil {
				t.Fatal(err)
			}
			first := common.RunSignature{ContextUID: contextUID, RunUID: "run-1"}
			if err := fixture.manager.Enqueue(ctx, contextUID, []*schema.AgenticMessage{
				schema.UserAgenticMessage("resume input"),
			}); err != nil {
				t.Fatal(err)
			}
			if err := fixture.manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
				Signature: first,
				Outcome:   contextmgr.RunOutcomeInterrupted,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.manager.CommitTurn(ctx, contextUID, nil); err != nil {
				t.Fatal(err)
			}

			secondUser := runStartMessage("second request", "run-2")
			if err := fixture.manager.Append(ctx, contextUID, secondUser); err != nil {
				t.Fatal(err)
			}
			if err := fixture.manager.Replace(ctx, contextUID, []*schema.AgenticMessage{
				schema.SystemAgenticMessage("system v2"),
				common.AssistantTextMessage("compressed summary"),
				firstUser,
				schema.UserAgenticMessage("resume input"),
				secondUser,
			}); err != nil {
				t.Fatal(err)
			}
			if err := fixture.manager.Enqueue(ctx, contextUID, []*schema.AgenticMessage{
				schema.UserAgenticMessage("source-only pending"),
			}); err != nil {
				t.Fatal(err)
			}
			second := common.RunSignature{ContextUID: contextUID, RunUID: "run-2"}
			if _, err := fixture.manager.Fork(ctx, second); !errors.Is(err, contextmgr.ErrRunNotSettled) {
				t.Fatalf("Fork(unsettled) error = %v", err)
			}
			if err := fixture.manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
				Signature:    second,
				Outcome:      contextmgr.RunOutcomeCompleted,
				FinalMessage: common.AssistantTextMessage("second answer"),
			}); err != nil {
				t.Fatal(err)
			}

			firstFork, err := fixture.manager.Fork(ctx, first)
			if err != nil {
				t.Fatal(err)
			}
			assertTexts(t, mustLoad(t, fixture.manager, firstFork), []string{
				"system v1", "first request",
			})

			secondFork, err := fixture.manager.Fork(ctx, second)
			if err != nil {
				t.Fatal(err)
			}
			assertTexts(t, mustLoad(t, fixture.manager, secondFork), []string{
				"system v2",
				"compressed summary",
				"first request",
				"resume input",
				"second request",
				"second answer",
			})

			if err := fixture.manager.Delete(ctx, contextUID); err != nil {
				t.Fatal(err)
			}
			nestedFork, err := fixture.manager.Fork(ctx, common.RunSignature{
				ContextUID: secondFork,
				RunUID:     first.RunUID,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertTexts(t, mustLoad(t, fixture.manager, nestedFork), []string{
				"system v1", "first request",
			})
		})
	}
}

func TestManagerRunValidation(t *testing.T) {
	ctx := context.Background()
	fixture := managerFactories()[0].new(t)
	if _, err := fixture.manager.Fork(ctx, common.RunSignature{}); !errors.Is(err, contextmgr.ErrInvalidRunSignature) {
		t.Fatalf("Fork(empty) error = %v", err)
	}
	if err := fixture.manager.SettleRun(ctx, nil); err == nil {
		t.Fatal("SettleRun(nil) succeeded")
	}
	if err := fixture.manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
		Signature: common.RunSignature{ContextUID: "context", RunUID: "run"},
		Outcome:   contextmgr.RunOutcomeCompleted,
	}); !errors.Is(err, contextmgr.ErrInvalidFinalMessage) {
		t.Fatalf("SettleRun(completed without final) error = %v", err)
	}

	first := runStartMessage("first", "run-1")
	second := runStartMessage("second", "run-2")
	contextUID, err := fixture.manager.Create(ctx, []*schema.AgenticMessage{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
		Signature: common.RunSignature{ContextUID: contextUID, RunUID: "run-1"},
		Outcome:   contextmgr.RunOutcomeFailed,
	}); !errors.Is(err, contextmgr.ErrRunNotCurrent) {
		t.Fatalf("SettleRun(non-current) error = %v", err)
	}
	if _, err := fixture.manager.Fork(ctx, common.RunSignature{
		ContextUID: contextUID,
		RunUID:     "run-2",
	}); !errors.Is(err, contextmgr.ErrRunNotSettled) {
		t.Fatalf("Fork(unsettled) error = %v", err)
	}
	if _, err := fixture.manager.Fork(ctx, common.RunSignature{
		ContextUID: contextUID,
		RunUID:     "unknown",
	}); !errors.Is(err, contextmgr.ErrRunNotFound) {
		t.Fatalf("Fork(unknown run) error = %v", err)
	}
	if _, err := fixture.manager.Fork(ctx, common.RunSignature{
		ContextUID: "missing",
		RunUID:     "run-1",
	}); !errors.Is(err, contextmgr.ErrContextNotFound) {
		t.Fatalf("Fork(missing context) error = %v", err)
	}
}

func TestPersistentManagersReloadSnapshots(t *testing.T) {
	tests := []struct {
		name string
		new  func(*testing.T) (*contextmgr.Manager, func() *contextmgr.Manager)
	}{
		{
			name: "file",
			new: func(t *testing.T) (*contextmgr.Manager, func() *contextmgr.Manager) {
				dir := filepath.Join(t.TempDir(), "contexts")
				return filectx.NewFileContextManager(dir), func() *contextmgr.Manager {
					return filectx.NewFileContextManager(dir)
				}
			},
		},
		{
			name: "sqlite",
			new: func(t *testing.T) (*contextmgr.Manager, func() *contextmgr.Manager) {
				path := filepath.Join(t.TempDir(), "context.sqlite")
				manager, err := sqlite.NewSQLiteContextManager(path)
				if err != nil {
					t.Fatal(err)
				}
				return manager, func() *contextmgr.Manager {
					reloaded, err := sqlite.NewSQLiteContextManager(path)
					if err != nil {
						t.Fatal(err)
					}
					return reloaded
				}
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			manager, reload := test.new(t)
			user := runStartMessage("request", "persisted-run")
			contextUID, err := manager.Create(ctx, []*schema.AgenticMessage{
				schema.SystemAgenticMessage("system"), user,
			})
			if err != nil {
				t.Fatal(err)
			}
			signature := common.RunSignature{ContextUID: contextUID, RunUID: "persisted-run"}
			if err := manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
				Signature:    signature,
				Outcome:      contextmgr.RunOutcomeCompleted,
				FinalMessage: common.AssistantTextMessage("answer"),
			}); err != nil {
				t.Fatal(err)
			}

			forkedUID, err := reload().Fork(ctx, signature)
			if err != nil {
				t.Fatal(err)
			}
			assertTexts(t, mustLoad(t, reload(), forkedUID), []string{"system", "request", "answer"})
		})
	}
}

func runStartMessage(text string, runUID common.RunUID) *schema.AgenticMessage {
	message := schema.UserAgenticMessage(text)
	common.MarkRunStart(message, runUID)
	return message
}

func mustLoad(
	t *testing.T,
	manager *contextmgr.Manager,
	contextUID common.ContextUID,
) []*schema.AgenticMessage {
	t.Helper()
	messages, err := manager.Load(context.Background(), contextUID)
	if err != nil {
		t.Fatal(err)
	}
	return messages
}

func assertUniquePrefixedTexts(
	t *testing.T,
	messages []*schema.AgenticMessage,
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

func assertTexts(t *testing.T, messages []*schema.AgenticMessage, want []string) {
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

func messageText(message *schema.AgenticMessage) string {
	if message == nil {
		return ""
	}
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		if block.UserInputText != nil {
			return block.UserInputText.Text
		}
		if block.AssistantGenText != nil {
			return block.AssistantGenText.Text
		}
	}
	return ""
}
