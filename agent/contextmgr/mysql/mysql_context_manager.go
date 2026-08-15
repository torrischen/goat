package mysql

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	"github.com/torrischen/goat/util"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	gomysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// MysqlStore persists a stream head, incremental events, and checkpoints.
type MysqlStore struct {
	db *gorm.DB
}

var _ contextmgr.Store = (*MysqlStore)(nil)

type contextConversation struct {
	ContextUID   string `gorm:"column:context_uid;primaryKey;size:191"`
	Revision     uint64 `gorm:"column:revision;not null;default:1"`
	StatePayload string `gorm:"column:state_payload;type:longtext"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type contextEvent struct {
	ContextUID string `gorm:"column:context_uid;primaryKey;size:191"`
	Revision   uint64 `gorm:"column:revision;primaryKey"`
	Payload    string `gorm:"column:payload;type:longtext;not null"`
	CreatedAt  time.Time
}

func (contextEvent) TableName() string {
	return "goat_context_events"
}

type contextCheckpoint struct {
	ContextUID string `gorm:"column:context_uid;primaryKey;size:191"`
	Revision   uint64 `gorm:"column:revision;primaryKey"`
	Payload    string `gorm:"column:payload;type:longtext;not null"`
	CreatedAt  time.Time
}

func (contextCheckpoint) TableName() string {
	return "goat_context_checkpoints"
}

func (contextConversation) TableName() string {
	return "goat_context_conversations"
}

// Legacy rows are read only when state_payload is empty.
type contextMessage struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	ContextUID   string `gorm:"column:context_uid;size:191;not null;index:idx_goat_context_messages_uid;uniqueIndex:idx_goat_context_uid_message_index,priority:1"`
	MessageIndex int64  `gorm:"column:message_index;not null;uniqueIndex:idx_goat_context_uid_message_index,priority:2"`
	Payload      string `gorm:"column:payload;type:longtext;not null"`
	CreatedAt    time.Time
}

func (contextMessage) TableName() string {
	return "goat_context_messages"
}

type pendingMessage struct {
	ID         uint64 `gorm:"primaryKey;autoIncrement"`
	ContextUID string `gorm:"column:context_uid;size:191;not null;index:idx_goat_context_pending_messages_uid"`
	Payload    string `gorm:"column:payload;type:longtext;not null"`
	CreatedAt  time.Time
}

func (pendingMessage) TableName() string {
	return "goat_context_pending_messages"
}

type runSnapshot struct {
	ID         uint64 `gorm:"primaryKey;autoIncrement"`
	ContextUID string `gorm:"column:context_uid;size:191;not null;index:idx_goat_context_run_snapshots_uid;uniqueIndex:idx_goat_context_run_snapshot_key,priority:1"`
	RunUID     string `gorm:"column:run_uid;size:191;not null;uniqueIndex:idx_goat_context_run_snapshot_key,priority:2"`
	Payload    string `gorm:"column:payload;type:longtext;not null"`
	CreatedAt  time.Time
}

func (runSnapshot) TableName() string {
	return "goat_context_run_snapshots"
}

// NewMysqlContextManager creates a Manager backed by MySQL.
func NewMysqlContextManager(
	host string,
	port int,
	username string,
	password string,
	dbname string,
) (*contextmgr.Manager, error) {
	store, err := NewMysqlStore(host, port, username, password, dbname)
	if err != nil {
		return nil, err
	}
	return contextmgr.NewManager(store), nil
}

// NewMysqlStore opens MySQL and migrates the current and compatibility tables.
func NewMysqlStore(
	host string,
	port int,
	username string,
	password string,
	dbname string,
) (*MysqlStore, error) {
	dsn, err := buildDSN(host, port, username, password, dbname)
	if err != nil {
		return nil, err
	}
	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(
		&contextConversation{},
		&contextEvent{},
		&contextCheckpoint{},
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
	return &MysqlStore{db: db}, nil
}

func buildDSN(host string, port int, username, password, dbname string) (string, error) {
	if host == "" {
		return "", errors.New("mysql context manager host is required")
	}
	if port <= 0 {
		return "", fmt.Errorf("mysql context manager port must be positive: %d", port)
	}
	if username == "" {
		return "", errors.New("mysql context manager username is required")
	}
	if dbname == "" {
		return "", errors.New("mysql context manager dbname is required")
	}

	cfg := gomysql.NewConfig()
	cfg.User = username
	cfg.Passwd = password
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	cfg.DBName = dbname
	cfg.ParseTime = true
	cfg.Params = map[string]string{
		"charset": "utf8mb4",
		"loc":     "Local",
	}
	return cfg.FormatDSN(), nil
}

func (s *MysqlStore) Create(
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
	if err := s.db.WithContext(ctx).Create(&contextConversation{
		ContextUID:   contextUID.String(),
		Revision:     next.Revision,
		StatePayload: payload,
	}).Error; err != nil {
		return "", err
	}
	return contextUID, nil
}

func (s *MysqlStore) Load(
	ctx context.Context,
	contextUID common.ContextUID,
) (*contextmgr.State, error) {
	return s.loadAt(s.db.WithContext(ctx), contextUID, 0)
}

func (s *MysqlStore) LoadAt(
	ctx context.Context,
	contextUID common.ContextUID,
	revision uint64,
) (*contextmgr.State, error) {
	return s.loadAt(s.db.WithContext(ctx), contextUID, revision)
}

func (s *MysqlStore) loadAt(
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

func (s *MysqlStore) Append(
	ctx context.Context,
	contextUID common.ContextUID,
	expectedRevision uint64,
	events []contextmgr.Event,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := contextmgr.ValidateEvents(events); err != nil {
		return err
	}
	cloned, err := contextmgr.CloneEvents(events)
	if err != nil {
		return err
	}
	payload, err := sonic.Marshal(cloned)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{
			"revision":   expectedRevision + 1,
			"updated_at": time.Now(),
		}
		var row contextConversation
		if err := tx.Where("context_uid = ?", contextUID.String()).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return contextmgr.ErrContextNotFound
			}
			return err
		}
		if row.Revision != expectedRevision {
			return contextmgr.ErrRevisionConflict
		}
		var checkpointPayload string
		if nextRevision := expectedRevision + 1; nextRevision%contextmgr.CheckpointInterval == 0 {
			checkpoint, err := s.loadAt(tx, contextUID, expectedRevision)
			if err != nil {
				return err
			}
			if err := contextmgr.ApplyEvents(checkpoint, nextRevision, events); err != nil {
				return err
			}
			checkpointPayload, err = encodeState(checkpoint)
			if err != nil {
				return err
			}
		}
		if row.StatePayload == "" {
			baseline, err := loadLegacyState(tx, contextUID)
			if err != nil {
				return err
			}
			baseline.Revision = expectedRevision
			encoded, err := encodeState(baseline)
			if err != nil {
				return err
			}
			updates["state_payload"] = encoded
		}
		result := tx.Model(&contextConversation{}).
			Where("context_uid = ? AND revision = ?", contextUID.String(), expectedRevision).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return contextmgr.ErrRevisionConflict
		}
		if err := tx.Create(&contextEvent{
			ContextUID: contextUID.String(),
			Revision:   expectedRevision + 1,
			Payload:    util.ByteToString(payload),
		}).Error; err != nil {
			return err
		}
		if checkpointPayload != "" {
			return tx.Create(&contextCheckpoint{
				ContextUID: contextUID.String(),
				Revision:   expectedRevision + 1,
				Payload:    checkpointPayload,
			}).Error
		}
		return nil
	})
}

func (s *MysqlStore) Delete(ctx context.Context, contextUID common.ContextUID) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("context_uid = ?", contextUID.String()).Delete(&contextCheckpoint{}).Error; err != nil {
			return err
		}
		if err := tx.Where("context_uid = ?", contextUID.String()).Delete(&contextEvent{}).Error; err != nil {
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
