package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	"github.com/torrischen/goat/util"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const defaultSQLitePath = "data/goat_context.sqlite"

// SQLiteStore persists a stream head, incremental events, and checkpoints.
type SQLiteStore struct {
	db *gorm.DB
}

var _ contextmgr.ContextStore = (*SQLiteStore)(nil)

type contextConversation struct {
	ContextUID    string `gorm:"column:context_uid;primaryKey;size:191"`
	Revision      uint64 `gorm:"column:revision;not null;default:1"`
	Finalized     bool   `gorm:"column:finalized;not null;default:false"`
	CurrentRunUID string `gorm:"column:current_run_uid;size:191"`
	PendingCount  int    `gorm:"column:pending_count;not null;default:0"`
	StatePayload  string `gorm:"column:state_payload;type:text"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type contextEvent struct {
	ContextUID string `gorm:"column:context_uid;primaryKey;size:191"`
	Revision   uint64 `gorm:"column:revision;primaryKey"`
	Payload    string `gorm:"column:payload;type:text;not null"`
	CreatedAt  time.Time
}

func (contextEvent) TableName() string {
	return "goat_context_events"
}

type contextCheckpoint struct {
	ContextUID string `gorm:"column:context_uid;primaryKey;size:191"`
	Revision   uint64 `gorm:"column:revision;primaryKey"`
	Payload    string `gorm:"column:payload;type:text;not null"`
	CreatedAt  time.Time
}

func (contextCheckpoint) TableName() string {
	return "goat_context_checkpoints"
}

func (contextConversation) TableName() string {
	return "goat_context_conversations"
}

type contextRun struct {
	ContextUID string `gorm:"column:context_uid;primaryKey;size:191"`
	RunUID     string `gorm:"column:run_uid;primaryKey;size:191"`
}

func (contextRun) TableName() string { return "goat_context_runs" }

// The following rows are retained only to migrate contexts written by the
// pre-Store context manager and to clean them up on Delete.
type contextMessage struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	ContextUID   string `gorm:"column:context_uid;size:191;not null;index:idx_goat_context_messages_uid;uniqueIndex:idx_goat_context_uid_message_index,priority:1"`
	MessageIndex int64  `gorm:"column:message_index;not null;uniqueIndex:idx_goat_context_uid_message_index,priority:2"`
	Payload      string `gorm:"column:payload;type:text;not null"`
	CreatedAt    time.Time
}

func (contextMessage) TableName() string {
	return "goat_context_messages"
}

type pendingMessage struct {
	ID         uint64 `gorm:"primaryKey;autoIncrement"`
	ContextUID string `gorm:"column:context_uid;size:191;not null;index:idx_goat_context_pending_messages_uid"`
	Payload    string `gorm:"column:payload;type:text;not null"`
	CreatedAt  time.Time
}

func (pendingMessage) TableName() string {
	return "goat_context_pending_messages"
}

type runSnapshot struct {
	ID         uint64 `gorm:"primaryKey;autoIncrement"`
	ContextUID string `gorm:"column:context_uid;size:191;not null;index:idx_goat_context_run_snapshots_uid;uniqueIndex:idx_goat_context_run_snapshot_key,priority:1"`
	RunUID     string `gorm:"column:run_uid;size:191;not null;uniqueIndex:idx_goat_context_run_snapshot_key,priority:2"`
	Payload    string `gorm:"column:payload;type:text;not null"`
	CreatedAt  time.Time
}

func (runSnapshot) TableName() string {
	return "goat_context_run_snapshots"
}

// NewSQLiteContextManager creates a Manager backed by SQLite. If dbPath is
// empty, data/goat_context.sqlite is used.
func NewSQLiteContextManager(dbPath string) (*contextmgr.Manager, error) {
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		return nil, err
	}
	return contextmgr.NewManager(store), nil
}

func (s *SQLiteStore) Create(ctx context.Context, request contextmgr.CreateRequest) (contextmgr.CreateResult, error) {
	if err := ctx.Err(); err != nil {
		return contextmgr.CreateResult{}, err
	}
	contextUID := common.ContextUID(uuid.NewString())
	state := contextmgr.NewState(request.InitialMessages)
	for runUID, snapshot := range request.RunSnapshots {
		state.RunSnapshots[runUID] = snapshot
	}
	state.Revision = 1
	payload, err := encodeState(state)
	if err != nil {
		return contextmgr.CreateResult{}, err
	}
	head := contextHead(contextUID, state)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&contextConversation{
			ContextUID: contextUID.String(), Revision: 1, Finalized: head.Finalized,
			CurrentRunUID: head.CurrentRunUID.String(), PendingCount: head.PendingCount,
			StatePayload: payload,
		}).Error; err != nil {
			return err
		}
		if err := recordRuns(tx, contextUID, request.InitialMessages, false); err != nil {
			return err
		}
		for runUID := range request.RunSnapshots {
			if err := tx.Create(&runSnapshot{ContextUID: contextUID.String(), RunUID: runUID.String(), Payload: "[]"}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return contextmgr.CreateResult{}, err
	}
	return contextmgr.CreateResult{ContextUID: contextUID, Revision: 1}, nil
}

func (s *SQLiteStore) ReadHead(ctx context.Context, contextUID common.ContextUID) (contextmgr.ContextHead, error) {
	var row contextConversation
	err := s.db.WithContext(ctx).Where("context_uid = ?", contextUID.String()).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return contextmgr.ContextHead{}, contextmgr.ErrContextNotFound
	}
	if err != nil {
		return contextmgr.ContextHead{}, err
	}
	return contextmgr.ContextHead{
		ContextUID: contextUID, Revision: row.Revision, Finalized: row.Finalized,
		CurrentRunUID: common.RunUID(row.CurrentRunUID), PendingCount: row.PendingCount,
	}, nil
}

func (s *SQLiteStore) ReadEvents(ctx context.Context, contextUID common.ContextUID, fromRevision uint64) ([]contextmgr.RevisionedEvent, error) {
	var count int64
	if err := s.db.WithContext(ctx).Model(&contextConversation{}).Where("context_uid = ?", contextUID.String()).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, contextmgr.ErrContextNotFound
	}
	if fromRevision == 0 {
		fromRevision = 1
	}
	var rows []contextEvent
	if err := s.db.WithContext(ctx).Where(
		"context_uid = ? AND revision >= ?", contextUID.String(), fromRevision,
	).Order("revision ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]contextmgr.RevisionedEvent, 0, len(rows))
	for _, row := range rows {
		var events []contextmgr.Event
		if err := sonic.UnmarshalString(row.Payload, &events); err != nil {
			return nil, fmt.Errorf("decode context event %d: %w", row.Revision, err)
		}
		result = append(result, contextmgr.RevisionedEvent{Revision: row.Revision, Events: events})
	}
	return result, nil
}

func (s *SQLiteStore) ReadView(ctx context.Context, request contextmgr.ReadViewRequest) (contextmgr.ContextView, error) {
	state, err := s.loadAt(s.db.WithContext(ctx), request.ContextUID, request.Revision)
	if err != nil {
		return contextmgr.ContextView{}, err
	}
	state.ContextUID = request.ContextUID
	if request.MessageLimit > 0 && len(state.Messages) > request.MessageLimit {
		state.Messages = state.Messages[len(state.Messages)-request.MessageLimit:]
	}
	if !request.IncludePending {
		state.PendingMessages = nil
	}
	if !request.IncludeRuns {
		state.RunSnapshots = nil
	}
	return *state, nil
}

// NewSQLiteStore opens SQLite and migrates both the current state table and
// legacy tables needed for transparent reads of existing conversations.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	dsn, err := buildDSN(dbPath)
	if err != nil {
		return nil, err
	}

	db, err := gorm.Open(gormsqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// A single connection also keeps :memory: databases coherent.
	sqlDB.SetMaxOpenConns(1)

	if err := configureSQLite(db); err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(
		&contextConversation{},
		&contextEvent{},
		&contextCheckpoint{},
		&contextRun{},
		&contextMessage{},
		&pendingMessage{},
		&runSnapshot{},
	); err != nil {
		return nil, err
	}
	if err := db.Model(&contextConversation{}).
		Where("revision = ?", 0).
		Update("revision", 1).Error; err != nil {
		return nil, err
	}
	if err := backfillLegacyHeads(db); err != nil {
		return nil, err
	}

	return &SQLiteStore{db: db}, nil
}

func backfillLegacyHeads(db *gorm.DB) error {
	var rows []contextConversation
	if err := db.Where("state_payload = ?", "").Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		contextUID := common.ContextUID(row.ContextUID)
		state, err := loadLegacyState(db, contextUID)
		if err != nil {
			return err
		}
		state.Revision = row.Revision
		head := contextHead(contextUID, state)
		if err := db.Model(&contextConversation{}).Where("context_uid = ?", row.ContextUID).Updates(map[string]any{
			"finalized": head.Finalized, "current_run_uid": head.CurrentRunUID.String(),
			"pending_count": head.PendingCount,
		}).Error; err != nil {
			return err
		}
		if err := recordRuns(db, contextUID, state.Messages, true); err != nil {
			return err
		}
	}
	return nil
}

func buildDSN(dbPath string) (string, error) {
	if dbPath == "" {
		dbPath = defaultSQLitePath
	}
	if strings.TrimSpace(dbPath) == "" {
		return "", errors.New("sqlite context manager db path is required")
	}
	if shouldCreateParentDir(dbPath) {
		dir := filepath.Dir(dbPath)
		if dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("create sqlite context manager directory: %w", err)
			}
		}
	}
	return dbPath, nil
}

func shouldCreateParentDir(dbPath string) bool {
	return dbPath != ":memory:" && !strings.HasPrefix(dbPath, "file:")
}

func configureSQLite(db *gorm.DB) error {
	if err := db.Exec("PRAGMA busy_timeout = 5000").Error; err != nil {
		return err
	}
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		return err
	}
	return db.Exec("PRAGMA journal_mode = WAL").Error
}

func (s *SQLiteStore) createState(
	ctx context.Context,
	state *contextmgr.State,
) (common.ContextUID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	next := state.Clone()
	next.Revision = 1
	payload, err := encodeState(next)
	if err != nil {
		return "", err
	}
	contextUID := common.ContextUID(uuid.NewString())
	head := contextHead(contextUID, next)
	if err := s.db.WithContext(ctx).Create(&contextConversation{
		ContextUID:    contextUID.String(),
		Revision:      next.Revision,
		Finalized:     head.Finalized,
		CurrentRunUID: head.CurrentRunUID.String(),
		PendingCount:  head.PendingCount,
		StatePayload:  payload,
	}).Error; err != nil {
		return "", err
	}
	return contextUID, nil
}

func (s *SQLiteStore) loadLatest(
	ctx context.Context,
	contextUID common.ContextUID,
) (*contextmgr.State, error) {
	return s.loadAt(s.db.WithContext(ctx), contextUID, 0)
}

func (s *SQLiteStore) loadViewAt(
	ctx context.Context,
	contextUID common.ContextUID,
	revision uint64,
) (*contextmgr.State, error) {
	return s.loadAt(s.db.WithContext(ctx), contextUID, revision)
}

func (s *SQLiteStore) loadAt(
	db *gorm.DB,
	contextUID common.ContextUID,
	targetRevision uint64,
) (*contextmgr.State, error) {
	var row contextConversation
	err := db.Where("context_uid = ?", contextUID.String()).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, contextmgr.ErrContextNotFound
	}
	if err != nil {
		return nil, err
	}
	if targetRevision == 0 {
		targetRevision = row.Revision
	}
	if targetRevision > row.Revision {
		return nil, contextmgr.ErrRevisionNotFound
	}

	var state *contextmgr.State
	if row.StatePayload != "" {
		state, err = decodeState(row.StatePayload)
		if err != nil {
			return nil, fmt.Errorf("decode context %s state: %w", contextUID, err)
		}
	} else {
		state, err = loadLegacyState(db, contextUID)
		if err != nil {
			return nil, err
		}
		state.Revision = row.Revision
	}
	if targetRevision < state.Revision {
		return nil, contextmgr.ErrRevisionNotFound
	}
	var checkpoints []contextCheckpoint
	if err := db.Where(
		"context_uid = ? AND revision <= ?", contextUID.String(), targetRevision,
	).Order("revision DESC").Limit(1).Find(&checkpoints).Error; err != nil {
		return nil, err
	}
	if len(checkpoints) == 1 && checkpoints[0].Revision > state.Revision {
		state, err = decodeState(checkpoints[0].Payload)
		if err != nil {
			return nil, fmt.Errorf("decode context checkpoint %d: %w", checkpoints[0].Revision, err)
		}
		state.Revision = checkpoints[0].Revision
	}

	var rows []contextEvent
	if err := db.Where(
		"context_uid = ? AND revision > ? AND revision <= ?",
		contextUID.String(), state.Revision, targetRevision,
	).Order("revision ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, eventRow := range rows {
		var events []contextmgr.Event
		if err := sonic.UnmarshalString(eventRow.Payload, &events); err != nil {
			return nil, fmt.Errorf("decode context event %d: %w", eventRow.Revision, err)
		}
		if err := contextmgr.ApplyEvents(state, eventRow.Revision, events); err != nil {
			return nil, err
		}
	}
	if state.Revision != targetRevision {
		return nil, contextmgr.ErrRevisionNotFound
	}
	return state.Clone(), nil
}

func (s *SQLiteStore) Append(ctx context.Context, request contextmgr.AppendRequest) (contextmgr.AppendResult, error) {
	if err := ctx.Err(); err != nil {
		return contextmgr.AppendResult{}, err
	}
	if err := contextmgr.ValidateEvents(request.Events); err != nil {
		return contextmgr.AppendResult{}, err
	}
	cloned, err := contextmgr.CloneEvents(request.Events)
	if err != nil {
		return contextmgr.AppendResult{}, err
	}
	payload, err := sonic.Marshal(cloned)
	if err != nil {
		return contextmgr.AppendResult{}, err
	}
	var result contextmgr.AppendResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row contextConversation
		if err := tx.Where("context_uid = ?", request.ContextUID.String()).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return contextmgr.ErrContextNotFound
			}
			return err
		}
		if row.Revision != request.ExpectedRevision {
			return contextmgr.ErrRevisionConflict
		}

		applied, noOp, err := applySQLTransition(tx, request.ContextUID, &row, cloned)
		if err != nil {
			return err
		}
		if noOp {
			result.Revision = row.Revision
			return nil
		}
		nextRevision := request.ExpectedRevision + 1
		updates := map[string]any{
			"revision": nextRevision, "finalized": row.Finalized,
			"current_run_uid": row.CurrentRunUID, "pending_count": row.PendingCount,
			"updated_at": time.Now(),
		}
		var checkpointPayload string
		if nextRevision%contextmgr.CheckpointInterval == 0 {
			checkpoint, err := s.loadAt(tx, request.ContextUID, request.ExpectedRevision)
			if err != nil {
				return err
			}
			if err := contextmgr.ApplyEvents(checkpoint, nextRevision, cloned); err != nil {
				return err
			}
			checkpointPayload, err = encodeState(checkpoint)
			if err != nil {
				return err
			}
		}
		if row.StatePayload == "" {
			baseline, err := loadLegacyState(tx, request.ContextUID)
			if err != nil {
				return err
			}
			baseline.Revision = request.ExpectedRevision
			encoded, err := encodeState(baseline)
			if err != nil {
				return err
			}
			updates["state_payload"] = encoded
		}
		updated := tx.Model(&contextConversation{}).
			Where("context_uid = ? AND revision = ?", request.ContextUID.String(), request.ExpectedRevision).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return contextmgr.ErrRevisionConflict
		}
		if err := tx.Create(&contextEvent{
			ContextUID: request.ContextUID.String(), Revision: nextRevision,
			Payload: util.ByteToString(payload),
		}).Error; err != nil {
			return err
		}
		if checkpointPayload != "" {
			if err := tx.Create(&contextCheckpoint{
				ContextUID: request.ContextUID.String(), Revision: nextRevision, Payload: checkpointPayload,
			}).Error; err != nil {
				return err
			}
		}
		result = contextmgr.AppendResult{Revision: nextRevision, AppliedPendingMessages: applied}
		return nil
	})
	return result, err
}

func applySQLTransition(tx *gorm.DB, contextUID common.ContextUID, head *contextConversation, events []contextmgr.Event) ([]*schema.AgenticMessage, bool, error) {
	var applied []*schema.AgenticMessage
	for _, event := range events {
		switch event.Type {
		case contextmgr.EventMessagesAppended:
			if err := recordRuns(tx, contextUID, event.Messages, false); err != nil {
				return nil, false, err
			}
			applyMessageHead(head, event.Messages, false)
		case contextmgr.EventMessagesReplaced:
			if err := recordRuns(tx, contextUID, event.Messages, true); err != nil {
				return nil, false, err
			}
			applyMessageHead(head, event.Messages, true)
		case contextmgr.EventPendingEnqueued:
			if head.Finalized {
				return nil, false, contextmgr.ErrConversationFinalized
			}
			for _, message := range event.Messages {
				payload, err := sonic.Marshal(message)
				if err != nil {
					return nil, false, err
				}
				if err := tx.Create(&pendingMessage{ContextUID: contextUID.String(), Payload: util.ByteToString(payload)}).Error; err != nil {
					return nil, false, err
				}
			}
			head.PendingCount += len(event.Messages)
		case contextmgr.EventTurnCommitted:
			var rows []pendingMessage
			if err := tx.Where("context_uid = ?", contextUID.String()).Order("id ASC").Find(&rows).Error; err != nil {
				return nil, false, err
			}
			if len(event.Messages) == 0 && len(rows) == 0 {
				return nil, true, nil
			}
			for _, row := range rows {
				message, err := decodeMessage(row.Payload)
				if err != nil {
					return nil, false, err
				}
				applied = append(applied, message)
			}
			if err := tx.Where("context_uid = ?", contextUID.String()).Delete(&pendingMessage{}).Error; err != nil {
				return nil, false, err
			}
			applyMessageHead(head, append(event.Messages, applied...), false)
			head.PendingCount = 0
		case contextmgr.EventRunSettled:
			var count int64
			if err := tx.Model(&runSnapshot{}).Where("context_uid = ? AND run_uid = ?", contextUID.String(), event.RunUID.String()).Count(&count).Error; err != nil {
				return nil, false, err
			}
			if count > 0 {
				return nil, true, nil
			}
			if err := tx.Model(&contextRun{}).Where("context_uid = ? AND run_uid = ?", contextUID.String(), event.RunUID.String()).Count(&count).Error; err != nil {
				return nil, false, err
			}
			if count == 0 {
				return nil, false, contextmgr.ErrRunNotFound
			}
			if head.CurrentRunUID != event.RunUID.String() {
				return nil, false, contextmgr.ErrRunNotCurrent
			}
			if err := tx.Create(&runSnapshot{ContextUID: contextUID.String(), RunUID: event.RunUID.String(), Payload: "[]"}).Error; err != nil {
				return nil, false, err
			}
			if event.Outcome == contextmgr.RunOutcomeCompleted {
				head.Finalized = true
				head.PendingCount = 0
				if err := tx.Where("context_uid = ?", contextUID.String()).Delete(&pendingMessage{}).Error; err != nil {
					return nil, false, err
				}
			}
		}
	}
	return common.CloneAgenticMessages(applied), false, nil
}

func recordRuns(tx *gorm.DB, contextUID common.ContextUID, messages []*schema.AgenticMessage, replace bool) error {
	if replace {
		if err := tx.Where("context_uid = ?", contextUID.String()).Delete(&contextRun{}).Error; err != nil {
			return err
		}
	}
	for _, message := range messages {
		if runUID, ok := common.RunUIDFromMessage(message); ok {
			if err := tx.FirstOrCreate(&contextRun{ContextUID: contextUID.String(), RunUID: runUID.String()}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func applyMessageHead(head *contextConversation, messages []*schema.AgenticMessage, replace bool) {
	if replace {
		head.CurrentRunUID = ""
		head.Finalized = false
	}
	for _, message := range messages {
		if runUID, ok := common.RunUIDFromMessage(message); ok {
			head.CurrentRunUID = runUID.String()
		}
	}
	if len(messages) > 0 {
		head.Finalized = isFinalAnswerMessage(messages[len(messages)-1])
	}
}

func (s *SQLiteStore) Delete(ctx context.Context, contextUID common.ContextUID) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("context_uid = ?", contextUID.String()).Delete(&contextCheckpoint{}).Error; err != nil {
			return err
		}
		if err := tx.Where("context_uid = ?", contextUID.String()).Delete(&contextEvent{}).Error; err != nil {
			return err
		}
		if err := tx.Where("context_uid = ?", contextUID.String()).Delete(&contextRun{}).Error; err != nil {
			return err
		}
		if err := tx.Where("context_uid = ?", contextUID.String()).Delete(&runSnapshot{}).Error; err != nil {
			return err
		}
		if err := tx.Where("context_uid = ?", contextUID.String()).Delete(&pendingMessage{}).Error; err != nil {
			return err
		}
		if err := tx.Where("context_uid = ?", contextUID.String()).Delete(&contextMessage{}).Error; err != nil {
			return err
		}
		return tx.Where("context_uid = ?", contextUID.String()).Delete(&contextConversation{}).Error
	})
}

func loadLegacyState(db *gorm.DB, contextUID common.ContextUID) (*contextmgr.State, error) {
	var messageRows []contextMessage
	if err := db.Where("context_uid = ?", contextUID.String()).
		Order("message_index ASC").
		Find(&messageRows).Error; err != nil {
		return nil, err
	}
	messages := make([]*schema.AgenticMessage, 0, len(messageRows))
	for _, row := range messageRows {
		message, err := decodeMessage(row.Payload)
		if err != nil {
			return nil, fmt.Errorf("decode legacy message %d: %w", row.MessageIndex, err)
		}
		messages = append(messages, message)
	}

	state := contextmgr.NewState(messages)
	var pendingRows []pendingMessage
	if err := db.Where("context_uid = ?", contextUID.String()).
		Order("id ASC").
		Find(&pendingRows).Error; err != nil {
		return nil, err
	}
	for _, row := range pendingRows {
		message, err := decodeMessage(row.Payload)
		if err != nil {
			return nil, fmt.Errorf("decode legacy pending message %d: %w", row.ID, err)
		}
		state.PendingMessages = append(state.PendingMessages, message)
	}

	var snapshotRows []runSnapshot
	if err := db.Where("context_uid = ?", contextUID.String()).
		Order("id ASC").
		Find(&snapshotRows).Error; err != nil {
		return nil, err
	}
	for _, row := range snapshotRows {
		var snapshotMessages []*schema.AgenticMessage
		if err := sonic.UnmarshalString(row.Payload, &snapshotMessages); err != nil {
			return nil, fmt.Errorf("decode legacy run snapshot %s: %w", row.RunUID, err)
		}
		state.RunSnapshots[common.RunUID(row.RunUID)] = contextmgr.RunSnapshot{
			Outcome:  inferLegacyOutcome(snapshotMessages),
			Messages: common.CloneAgenticMessages(snapshotMessages),
		}
	}
	return state, nil
}

func contextHead(contextUID common.ContextUID, state *contextmgr.State) contextmgr.ContextHead {
	head := contextmgr.ContextHead{ContextUID: contextUID, Revision: state.Revision, PendingCount: len(state.PendingMessages)}
	if len(state.Messages) > 0 {
		head.Finalized = isFinalAnswerMessage(state.Messages[len(state.Messages)-1])
	}
	for _, message := range state.Messages {
		if runUID, ok := common.RunUIDFromMessage(message); ok {
			head.CurrentRunUID = runUID
		}
	}
	return head
}

func inferLegacyOutcome(messages []*schema.AgenticMessage) contextmgr.RunOutcome {
	if len(messages) > 0 && isFinalAnswerMessage(messages[len(messages)-1]) {
		return contextmgr.RunOutcomeCompleted
	}
	return contextmgr.RunOutcomeInterrupted
}

func isFinalAnswerMessage(message *schema.AgenticMessage) bool {
	if message == nil || message.Role != schema.AgenticRoleTypeAssistant {
		return false
	}
	for _, block := range message.ContentBlocks {
		if block != nil && block.FunctionToolCall != nil {
			return false
		}
	}
	return true
}

func encodeState(state *contextmgr.State) (string, error) {
	payload, err := sonic.Marshal(state.Clone())
	if err != nil {
		return "", err
	}
	return util.ByteToString(payload), nil
}

func decodeState(payload string) (*contextmgr.State, error) {
	var state contextmgr.State
	if err := sonic.UnmarshalString(payload, &state); err != nil {
		return nil, err
	}
	return state.Clone(), nil
}

func decodeMessage(payload string) (*schema.AgenticMessage, error) {
	var message schema.AgenticMessage
	if err := sonic.UnmarshalString(payload, &message); err != nil {
		return nil, err
	}
	return &message, nil
}
