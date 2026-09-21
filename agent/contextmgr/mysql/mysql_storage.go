package mysql

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/bytedance/sonic"
	"github.com/go-sql-driver/mysql"
	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	"github.com/torrischen/goat/agent/message"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultHost = "127.0.0.1"
	defaultPort = 3306
)

// Config configures a MySQL context store. DSN takes precedence over the
// individual connection fields.
type Config struct {
	DSN      string
	Host     string
	Port     int
	User     string
	Password string
	Database string
}

// MySQLStore persists heads and sequence-addressed messages with GORM.
type MySQLStore struct {
	db *gorm.DB
}

var _ contextmgr.Store = (*MySQLStore)(nil)

// NewMySQLStore opens a GORM MySQL connection and creates the required tables.
func NewMySQLStore(config Config) (*MySQLStore, error) {
	dsn, err := config.dsn()
	if err != nil {
		return nil, err
	}
	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{TranslateError: true})
	if err != nil {
		return nil, fmt.Errorf("open MySQL: %w", err)
	}
	store := &MySQLStore{db: db}
	if err := store.migrate(config); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("migrate MySQL context tables: %w", err)
	}
	return store, nil
}

// NewMySQLStoreWithDB wraps an existing GORM connection and migrates the
// supplied tables. The caller owns the DB connection.
func NewMySQLStoreWithDB(db *gorm.DB) (*MySQLStore, error) {
	if db == nil {
		return nil, errors.New("MySQL GORM DB must not be nil")
	}
	store := &MySQLStore{db: db}
	if err := store.migrate(Config{}); err != nil {
		return nil, fmt.Errorf("migrate MySQL context tables: %w", err)
	}
	return store, nil
}

func (config Config) dsn() (string, error) {
	if config.DSN != "" {
		return config.DSN, nil
	}
	if config.User == "" {
		return "", errors.New("MySQL context manager user is required")
	}
	if config.Database == "" {
		return "", errors.New("MySQL context manager database is required")
	}
	host := config.Host
	if host == "" {
		host = defaultHost
	}
	port := config.Port
	if port == 0 {
		port = defaultPort
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("MySQL context manager port is invalid: %d", port)
	}
	cfg := mysql.NewConfig()
	cfg.User = config.User
	cfg.Passwd = config.Password
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	cfg.DBName = config.Database
	cfg.ParseTime = true
	cfg.Params = map[string]string{"charset": "utf8mb4", "collation": "utf8mb4_bin"}
	return cfg.FormatDSN(), nil
}

func (s *MySQLStore) migrate(config Config) error {
	return s.db.AutoMigrate(&HeadModel{}, &MessageModel{})
}

// NewMySQLContextManager creates a Manager backed by MySQL.
func NewMySQLContextManager(config Config) (*contextmgr.Manager, error) {
	store, err := NewMySQLStore(config)
	if err != nil {
		return nil, err
	}
	return contextmgr.NewManager(store), nil
}

func (s *MySQLStore) LoadHead(ctx context.Context, uid common.ContextUID) (*contextmgr.Head, error) {
	var model HeadModel
	err := s.db.WithContext(ctx).Where("uid = ?", string(uid)).Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, contextmgr.ErrContextNotFound
	}
	if err != nil {
		return nil, err
	}
	return modelToHead(&model)
}

func (s *MySQLStore) CommitAppend(ctx context.Context, rows []contextmgr.MessageRow, next *contextmgr.Head, expectVersion uint64) error {
	if next == nil {
		return errors.New("commit append requires a head")
	}
	models := make([]MessageModel, 0, len(rows))
	for _, row := range rows {
		model, err := messageToModel(row)
		if err != nil {
			return err
		}
		models = append(models, *model)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model, err := headToModel(next)
		if err != nil {
			return err
		}
		if expectVersion == 0 {
			if err := tx.Create(model).Error; err != nil {
				if errors.Is(err, gorm.ErrDuplicatedKey) {
					return contextmgr.ErrCASConflict
				}
				return err
			}
		} else {
			result := tx.Model(&HeadModel{}).Where("uid = ? AND version = ?", string(next.UID), expectVersion).Updates(headUpdateMap(model))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				var exists HeadModel
				if err := tx.Where("uid = ?", string(next.UID)).Take(&exists).Error; errors.Is(err, gorm.ErrRecordNotFound) {
					return contextmgr.ErrContextNotFound
				} else if err != nil {
					return err
				}
				return contextmgr.ErrCASConflict
			}
		}
		if len(models) > 0 {
			if err := tx.Create(&models).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *MySQLStore) ReadMessages(ctx context.Context, uid common.ContextUID, lane contextmgr.Lane, fromSeq, toSeq uint64) ([]contextmgr.MessageRow, error) {
	var models []MessageModel
	err := s.db.WithContext(ctx).
		Where("uid = ? AND lane = ? AND seq BETWEEN ? AND ?", string(uid), string(lane), fromSeq, toSeq).
		Order("seq ASC").Find(&models).Error
	if err != nil {
		return nil, err
	}
	rows := make([]contextmgr.MessageRow, 0, len(models))
	for i := range models {
		row, err := modelToMessage(&models[i])
		if err != nil {
			return nil, err
		}
		rows = append(rows, *row)
	}
	return rows, nil
}

func (s *MySQLStore) ReplaceCommitted(ctx context.Context, uid common.ContextUID, rows []contextmgr.MessageRow, next *contextmgr.Head, expectVersion uint64) error {
	if next == nil || next.UID != uid {
		return errors.New("replacement head UID does not match context UID")
	}
	models := make([]MessageModel, 0, len(rows))
	for _, row := range rows {
		model, err := messageToModel(row)
		if err != nil {
			return err
		}
		models = append(models, *model)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("uid = ? AND lane = ?", string(uid), string(contextmgr.LaneCommitted)).Delete(&MessageModel{}).Error; err != nil {
			return err
		}
		if len(models) > 0 {
			if err := tx.Clauses(clause.Insert{Modifier: "IGNORE"}).Create(&models).Error; err != nil {
				return err
			}
		}
		model, err := headToModel(next)
		if err != nil {
			return err
		}
		result := tx.Model(&HeadModel{}).Where("uid = ? AND version = ?", string(uid), expectVersion).Updates(headUpdateMap(model))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}
		var exists HeadModel
		if err := tx.Where("uid = ?", string(uid)).Take(&exists).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return contextmgr.ErrContextNotFound
		} else if err != nil {
			return err
		}
		return contextmgr.ErrCASConflict
	})
}

func (s *MySQLStore) DeleteContext(ctx context.Context, uid common.ContextUID) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("uid = ?", string(uid)).Delete(&HeadModel{}).Error; err != nil {
			return err
		}
		return tx.Where("uid = ?", string(uid)).Delete(&MessageModel{}).Error
	})
}

// Close closes the underlying SQL connection pool.
func (s *MySQLStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func headToModel(head *contextmgr.Head) (*HeadModel, error) {
	runs := make(map[string]contextmgr.RunSnapshot, len(head.Runs))
	for uid, snapshot := range head.Runs {
		runs[string(uid)] = snapshot
	}
	runsJSON, err := sonic.Marshal(runs)
	if err != nil {
		return nil, fmt.Errorf("marshal run snapshots: %w", err)
	}
	return &HeadModel{UID: string(head.UID), Generation: head.Generation, Version: head.Version, CommittedSeq: head.CommittedSeq, PendingStart: head.PendingStart, PendingSeq: head.PendingSeq, Finalized: head.Finalized, RunsJSON: runsJSON}, nil
}

func headUpdateMap(model *HeadModel) map[string]any {
	return map[string]any{
		"generation":    model.Generation,
		"version":       model.Version,
		"committed_seq": model.CommittedSeq,
		"pending_start": model.PendingStart,
		"pending_seq":   model.PendingSeq,
		"finalized":     model.Finalized,
		"runs_json":     model.RunsJSON,
	}
}

func modelToHead(model *HeadModel) (*contextmgr.Head, error) {
	runs := make(map[string]contextmgr.RunSnapshot)
	if len(model.RunsJSON) > 0 {
		if err := sonic.Unmarshal(model.RunsJSON, &runs); err != nil {
			return nil, fmt.Errorf("unmarshal run snapshots: %w", err)
		}
	}
	result := make(map[common.RunUID]contextmgr.RunSnapshot, len(runs))
	for uid, snapshot := range runs {
		result[common.RunUID(uid)] = snapshot
	}
	return &contextmgr.Head{UID: common.ContextUID(model.UID), Generation: model.Generation, Version: model.Version, CommittedSeq: model.CommittedSeq, PendingStart: model.PendingStart, PendingSeq: model.PendingSeq, Finalized: model.Finalized, Runs: result}, nil
}

func messageToModel(row contextmgr.MessageRow) (*MessageModel, error) {
	payload, err := sonic.Marshal(row.Message)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}
	return &MessageModel{UID: string(row.UID), Lane: string(row.Lane), Seq: row.Seq, Message: payload}, nil
}

func modelToMessage(model *MessageModel) (*contextmgr.MessageRow, error) {
	var msg message.Message
	if err := sonic.Unmarshal(model.Message, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}
	return &contextmgr.MessageRow{UID: common.ContextUID(model.UID), Lane: contextmgr.Lane(model.Lane), Seq: model.Seq, Message: &msg}, nil
}
