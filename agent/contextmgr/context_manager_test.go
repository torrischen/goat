package contextmgr_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	filectx "github.com/torrischen/goat/agent/contextmgr/file"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	redisctx "github.com/torrischen/goat/agent/contextmgr/redis"

	"github.com/cloudwego/eino/schema"
)

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
		{
			name: "file",
			new: func(t *testing.T) *contextmgr.Manager {
				manager, err := filectx.NewFileContextManager(filepath.Join(t.TempDir(), "contexts"))
				if err != nil {
					t.Fatal(err)
				}
				return manager
			},
		},
		{
			name: "redis",
			new: func(t *testing.T) *contextmgr.Manager {
				server := miniredis.RunT(t)
				return newRedisTestManager(t, server, "manager:")
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
			results <- manager.CreateWithUID(ctx, uid, []*schema.AgenticMessage{schema.UserAgenticMessage("initial")})
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

			initial := []*schema.AgenticMessage{schema.UserAgenticMessage("initial")}
			contextUID, err := manager.Create(ctx, initial)
			if err != nil || contextUID == "" {
				t.Fatalf("Create() = %q, %v", contextUID, err)
			}
			// Mutating the caller's slice must not affect stored state.
			initial[0].ContentBlocks[0].UserInputText.Text = "mutated input"

			loaded := mustLoad(t, manager, contextUID)
			if len(loaded) != 1 || messageText(loaded[0]) != "initial" {
				t.Fatalf("Load() = %+v", loaded)
			}
			// Mutating the loaded slice must not affect stored state.
			loaded[0].ContentBlocks[0].UserInputText.Text = "mutated load"
			reloaded := mustLoad(t, manager, contextUID)
			if messageText(reloaded[0]) != "initial" {
				t.Fatal("Load exposed stored state")
			}

			if err := manager.Append(ctx, contextUID, schema.UserAgenticMessage("next")); err != nil {
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

func TestManagerPendingAndReplaceContract(t *testing.T) {
	for _, factory := range managerFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			ctx := context.Background()
			manager := factory.new(t)
			contextUID, err := manager.Create(ctx, []*schema.AgenticMessage{
				schema.SystemAgenticMessage("system"),
			})
			if err != nil {
				t.Fatal(err)
			}

			steering := []*schema.AgenticMessage{
				schema.UserAgenticMessage("steer one"),
				schema.UserAgenticMessage("steer two"),
			}
			if err := manager.Enqueue(ctx, contextUID, steering); err != nil {
				t.Fatal(err)
			}
			steering[0] = nil
			assertTexts(t, mustLoad(t, manager, contextUID), []string{"system"})

			if err := manager.Replace(ctx, contextUID, []*schema.AgenticMessage{
				schema.SystemAgenticMessage("replacement"),
			}); err != nil {
				t.Fatal(err)
			}
			result, err := manager.CommitTurn(ctx, contextUID, []*schema.AgenticMessage{
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
			if err := manager.Enqueue(ctx, contextUID, []*schema.AgenticMessage{
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
			contextUID, err := manager.Create(ctx, []*schema.AgenticMessage{
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
					errs <- manager.Append(
						ctx,
						contextUID,
						schema.UserAgenticMessage(fmt.Sprintf("append-%d", i)),
					)
				}()
				go func() {
					defer wg.Done()
					errs <- manager.Enqueue(ctx, contextUID, []*schema.AgenticMessage{
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
				contextUID, err := manager.Create(ctx, []*schema.AgenticMessage{
					schema.SystemAgenticMessage("system"),
					user,
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := manager.Enqueue(ctx, contextUID, []*schema.AgenticMessage{
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
				if err := manager.SettleRun(ctx, args); err != nil {
					t.Fatal(err)
				}
				if err := manager.SettleRun(ctx, args); err != nil {
					t.Fatalf("SettleRun was not idempotent: %v", err)
				}

				if outcome == contextmgr.RunOutcomeCompleted {
					assertTexts(t, mustLoad(t, manager, contextUID), []string{"system", "request", "answer"})
					if err := manager.Enqueue(ctx, contextUID, []*schema.AgenticMessage{
						schema.UserAgenticMessage("late"),
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

func TestManagerForkPreservesHistoricalSnapshots(t *testing.T) {
	for _, factory := range managerFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			ctx := context.Background()
			manager := factory.new(t)
			firstUser := runStartMessage("first request", "run-1")
			contextUID, err := manager.Create(ctx, []*schema.AgenticMessage{
				schema.SystemAgenticMessage("system v1"),
				firstUser,
			})
			if err != nil {
				t.Fatal(err)
			}
			first := common.RunSignature{ContextUID: contextUID, RunUID: "run-1"}
			if err := manager.Enqueue(ctx, contextUID, []*schema.AgenticMessage{
				schema.UserAgenticMessage("resume input"),
			}); err != nil {
				t.Fatal(err)
			}
			if err := manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
				Signature: first,
				Outcome:   contextmgr.RunOutcomeInterrupted,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.CommitTurn(ctx, contextUID, nil); err != nil {
				t.Fatal(err)
			}

			secondUser := runStartMessage("second request", "run-2")
			if err := manager.Append(ctx, contextUID, secondUser); err != nil {
				t.Fatal(err)
			}
			if err := manager.Replace(ctx, contextUID, []*schema.AgenticMessage{
				schema.SystemAgenticMessage("system v2"),
				common.AssistantTextMessage("compressed summary"),
				firstUser,
				schema.UserAgenticMessage("resume input"),
				secondUser,
			}); err != nil {
				t.Fatal(err)
			}
			second := common.RunSignature{ContextUID: contextUID, RunUID: "run-2"}
			if _, err := manager.Fork(ctx, second); !errors.Is(err, contextmgr.ErrRunNotSettled) {
				t.Fatalf("Fork(unsettled) error = %v", err)
			}
			if err := manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
				Signature:    second,
				Outcome:      contextmgr.RunOutcomeCompleted,
				FinalMessage: common.AssistantTextMessage("second answer"),
			}); err != nil {
				t.Fatal(err)
			}

			firstFork, err := manager.Fork(ctx, first)
			if err != nil {
				t.Fatal(err)
			}
			assertTexts(t, mustLoad(t, manager, firstFork), []string{
				"system v1", "first request",
			})

			secondFork, err := manager.Fork(ctx, second)
			if err != nil {
				t.Fatal(err)
			}
			assertTexts(t, mustLoad(t, manager, secondFork), []string{
				"system v2",
				"compressed summary",
				"first request",
				"resume input",
				"second request",
				"second answer",
			})

			if err := manager.Delete(ctx, contextUID); err != nil {
				t.Fatal(err)
			}
			nestedFork, err := manager.Fork(ctx, common.RunSignature{
				ContextUID: secondFork,
				RunUID:     first.RunUID,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertTexts(t, mustLoad(t, manager, nestedFork), []string{
				"system v1", "first request",
			})
		})
	}
}

func TestManagerRunValidation(t *testing.T) {
	ctx := context.Background()
	manager := managerFactories()[0].new(t)
	if _, err := manager.Fork(ctx, common.RunSignature{}); !errors.Is(err, contextmgr.ErrInvalidRunSignature) {
		t.Fatalf("Fork(empty) error = %v", err)
	}
	if err := manager.SettleRun(ctx, nil); err == nil {
		t.Fatal("SettleRun(nil) succeeded")
	}
	if err := manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
		Signature: common.RunSignature{ContextUID: "context", RunUID: "run"},
		Outcome:   contextmgr.RunOutcomeCompleted,
	}); !errors.Is(err, contextmgr.ErrInvalidFinalMessage) {
		t.Fatalf("SettleRun(completed without final) error = %v", err)
	}

	first := runStartMessage("first", "run-1")
	second := runStartMessage("second", "run-2")
	contextUID, err := manager.Create(ctx, []*schema.AgenticMessage{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
		Signature: common.RunSignature{ContextUID: contextUID, RunUID: "run-1"},
		Outcome:   contextmgr.RunOutcomeFailed,
	}); !errors.Is(err, contextmgr.ErrRunNotCurrent) {
		t.Fatalf("SettleRun(non-current) error = %v", err)
	}
	if _, err := manager.Fork(ctx, common.RunSignature{
		ContextUID: contextUID,
		RunUID:     "run-2",
	}); !errors.Is(err, contextmgr.ErrRunNotSettled) {
		t.Fatalf("Fork(unsettled) error = %v", err)
	}
	if _, err := manager.Fork(ctx, common.RunSignature{
		ContextUID: contextUID,
		RunUID:     "unknown",
	}); !errors.Is(err, contextmgr.ErrRunNotFound) {
		t.Fatalf("Fork(unknown run) error = %v", err)
	}
	if _, err := manager.Fork(ctx, common.RunSignature{
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
				open := func() *contextmgr.Manager {
					manager, err := filectx.NewFileContextManager(dir)
					if err != nil {
						t.Fatal(err)
					}
					return manager
				}
				return open(), open
			},
		},
		{
			name: "redis",
			new: func(t *testing.T) (*contextmgr.Manager, func() *contextmgr.Manager) {
				server := miniredis.RunT(t)
				open := func() *contextmgr.Manager {
					return newRedisTestManager(t, server, "reload:")
				}
				return open(), open
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

func newRedisTestManager(t *testing.T, server *miniredis.Miniredis, prefix string) *contextmgr.Manager {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return contextmgr.NewManager(redisctx.NewRedisStorageWithClient(client, prefix))
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
