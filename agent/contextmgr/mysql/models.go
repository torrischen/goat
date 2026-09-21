package mysql

import "time"

// HeadModel is the durable MySQL representation of a context head.
// RunsJSON contains the bounded run snapshot map encoded as JSON.
type HeadModel struct {
	UID          string    `gorm:"column:uid;type:varchar(191);primaryKey"`
	Generation   string    `gorm:"column:generation;type:varchar(191);not null"`
	Version      uint64    `gorm:"column:version;not null"`
	CommittedSeq uint64    `gorm:"column:committed_seq;not null"`
	PendingStart uint64    `gorm:"column:pending_start;not null"`
	PendingSeq   uint64    `gorm:"column:pending_seq;not null"`
	Finalized    bool      `gorm:"column:finalized;not null"`
	RunsJSON     []byte    `gorm:"column:runs_json;type:longblob"`
	CreatedAt    time.Time `gorm:"column:created_at;not null"`
	UpdatedAt    time.Time `gorm:"column:updated_at;not null"`
}

func (HeadModel) TableName() string { return "goat_context_heads" }

// MessageModel is a sequence-addressed message-log row.
// The composite primary key is also the covering lookup order for
// (uid, lane, seq) range reads and deletes.
type MessageModel struct {
	UID       string    `gorm:"column:uid;type:varchar(191);primaryKey"`
	Lane      string    `gorm:"column:lane;type:varchar(32);primaryKey"`
	Seq       uint64    `gorm:"column:seq;primaryKey"`
	Message   []byte    `gorm:"column:message;type:longblob;not null"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
}

func (MessageModel) TableName() string { return "goat_context_messages" }
