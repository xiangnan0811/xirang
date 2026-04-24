package nodelogs

import (
	"context"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

// TestWorker_ProcessInsertsLogsAndAdvancesCursor exercises the worker.process
// path end-to-end: fake runner returns a journalctl JSON line, worker parses,
// inserts the LogEntry row and the updated cursor.
func TestWorker_ProcessInsertsLogsAndAdvancesCursor(t *testing.T) {
	db := openCursorTestDB(t)
	if err := db.AutoMigrate(&LogEntry{}); err != nil {
		t.Fatalf("automigrate node_logs: %v", err)
	}

	node := model.Node{
		ID:                   42,
		Name:                 "n42",
		Host:                 "h",
		Username:             "u",
		LogJournalctlEnabled: true,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// One journal line bracketed by the JournalDelim the fetcher parses.
	out := `{"__REALTIME_TIMESTAMP":"1700000000000000","__CURSOR":"c1","PRIORITY":"6","_SYSTEMD_UNIT":"sshd","MESSAGE":"hello"}` + "\n" + JournalDelim + "\n"

	w := &Worker{
		db:      db,
		jobs:    make(chan CollectJob, 1),
		fetcher: &Fetcher{runner: &fakeRunner{out: out}},
		curRepo: NewCursorRepo(db),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	w.process(ctx, CollectJob{Node: node})

	var logs []LogEntry
	if err := db.Find(&logs).Error; err != nil {
		t.Fatalf("find logs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 inserted log, got %d", len(logs))
	}
	if logs[0].Message != "hello" {
		t.Fatalf("expected message=hello, got %q", logs[0].Message)
	}
	if logs[0].NodeID != node.ID {
		t.Fatalf("wrong node_id: %d", logs[0].NodeID)
	}

	// Cursor for (node, journalctl, "") must be persisted to "c1".
	cur, err := w.curRepo.LoadForNode(node.ID)
	if err != nil {
		t.Fatalf("load cursors: %v", err)
	}
	got, ok := cur[CursorKey{SourceJournalctl, ""}]
	if !ok {
		t.Fatalf("no journalctl cursor saved; have %v", cur)
	}
	if got.CursorText != "c1" {
		t.Fatalf("expected cursor=c1, got %q", got.CursorText)
	}
}

// TestWorker_ProcessFetchFailurePreservesCursor: a transient SSH failure must
// not overwrite the existing cursor — next tick should retry from the same
// point. This is the regression we care about most (otherwise we'd lose
// journal lines every time SSH hiccups).
func TestWorker_ProcessFetchFailurePreservesCursor(t *testing.T) {
	db := openCursorTestDB(t)
	if err := db.AutoMigrate(&LogEntry{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	node := model.Node{ID: 7, Name: "n7", Host: "h", Username: "u", LogJournalctlEnabled: true}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Pre-seed cursor so we can verify it's unchanged after failure.
	seedCur := []Cursor{{
		NodeID:     node.ID,
		Source:     SourceJournalctl,
		Path:       "",
		CursorText: "prev-cursor",
	}}
	curRepo := NewCursorRepo(db)
	if err := curRepo.SaveForNode(node.ID, seedCur); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	w := &Worker{
		db:      db,
		jobs:    make(chan CollectJob, 1),
		fetcher: &Fetcher{runner: &fakeRunner{err: errTransient}},
		curRepo: curRepo,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	w.process(ctx, CollectJob{Node: node})

	cur, err := curRepo.LoadForNode(node.ID)
	if err != nil {
		t.Fatalf("reload cursors: %v", err)
	}
	got := cur[CursorKey{SourceJournalctl, ""}]
	if got.CursorText != "prev-cursor" {
		t.Fatalf("cursor mutated on failure — lost lines will re-fetch: got %q", got.CursorText)
	}

	var count int64
	if err := db.Model(&LogEntry{}).Count(&count).Error; err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if count != 0 {
		t.Fatalf("no logs should be inserted on fetch failure, got %d", count)
	}
}

// errTransient is a distinctive sentinel so the test output pinpoints the path.
var errTransient = &transientErr{}

type transientErr struct{}

func (*transientErr) Error() string { return "simulated SSH timeout" }
