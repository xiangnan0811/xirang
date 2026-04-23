package nodelogs

import (
	"time"

	"xirang/backend/internal/model"
)

// Source constants for log origin.
const (
	SourceJournalctl = "journalctl"
	SourceFile       = "file"
)

// LogEntry is a parsed log line ready for insertion into the node_logs table.
// The table name is explicit because GORM's default pluralization would emit
// `log_entries`, but migration 000039 created the canonical table `node_logs`.
type LogEntry struct {
	NodeID    uint
	Source    string
	Path      string
	Timestamp time.Time
	Priority  string
	Message   string
}

// TableName binds LogEntry writes to the node_logs table (populated by migration 000039).
func (LogEntry) TableName() string { return "node_logs" }

// CollectJob is a scheduler -> worker handoff: "please fetch logs from this node".
type CollectJob struct {
	Node model.Node
}

// CursorKey identifies a (source, path) pair within a node.
type CursorKey struct {
	Source string
	Path   string
}

// Cursor carries the state needed to do incremental fetch next tick.
type Cursor struct {
	NodeID     uint
	Source     string
	Path       string
	CursorText string
	FileOffset int64
	FileInode  int64
}

// DefaultRetentionDays is the fallback if system_settings.log_retention_days_default is unset.
const DefaultRetentionDays = 30

// DefaultWorkerCount is the worker pool size.
const DefaultWorkerCount = 10

// DefaultJobQueueSize is the scheduler -> worker buffered channel size.
const DefaultJobQueueSize = 50

// DefaultTickInterval is the scheduler cadence.
const DefaultTickInterval = 30 * time.Second

// FetchTimeout is the SSH batch command ceiling.
const FetchTimeout = 15 * time.Second

// MaxFetchBytes caps SSH output to prevent runaway transfer.
const MaxFetchBytes = 10 * 1024 * 1024 // 10 MB

// InsertBatchSize controls log rows per insert transaction.
const InsertBatchSize = 500

// SSH batch command delimiters.
const (
	JournalDelim = "<<<XIRANG_DELIM>>>"
	FileEnd      = "<<<XIRANG_FILE_END>>>"
)
