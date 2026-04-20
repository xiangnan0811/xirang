package nodelogs

import (
	"context"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestNeedsCollection_JournalOnly(t *testing.T) {
	n := &model.Node{LogJournalctlEnabled: true}
	if !needsCollection(n) {
		t.Fatal("expected true")
	}
}

func TestNeedsCollection_FilesOnly(t *testing.T) {
	n := &model.Node{LogPaths: `["/a"]`, LogJournalctlEnabled: false}
	if !needsCollection(n) {
		t.Fatal("expected true")
	}
}

func TestNeedsCollection_NeitherSkips(t *testing.T) {
	n := &model.Node{LogJournalctlEnabled: false, LogPaths: ""}
	if needsCollection(n) {
		t.Fatal("expected false")
	}
}

func TestScheduler_EnqueuesEligibleNodes(t *testing.T) {
	db := openCursorTestDB(t)

	// Node 1: journalctl enabled — use normal Create (default:true matches desired value).
	db.Create(&model.Node{
		ID:                   1,
		Name:                 "enabled",
		Host:                 "h1",
		Username:             "u",
		LogJournalctlEnabled: true,
		BackupDir:            "/b1",
	})

	// Node 2: journalctl disabled — raw SQL bypasses GORM's default:true suppression of false zero-value.
	if err := db.Exec(
		`INSERT INTO nodes (id, name, host, username, auth_type, port, status, log_journalctl_enabled, log_paths, backup_dir, created_at, updated_at)
		 VALUES (2, 'disabled', 'h2', 'u', 'key', 22, 'offline', 0, '', '/b2', datetime('now'), datetime('now'))`,
	).Error; err != nil {
		t.Fatalf("insert disabled node: %v", err)
	}

	// Verify the disabled node actually has log_journalctl_enabled = false in DB.
	var check model.Node
	db.First(&check, 2)
	if check.LogJournalctlEnabled {
		t.Fatalf("setup failed: node 2 should have LogJournalctlEnabled=false, got true")
	}

	s := NewScheduler(db, &fakeRunner{})
	s.workers = 0 // do not start workers — we inspect the channel directly
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.enqueue(ctx)

	// Exactly one job (node 1) should be enqueued.
	select {
	case job := <-s.jobs:
		if job.Node.ID != 1 {
			t.Fatalf("wrong node enqueued: %d", job.Node.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no job enqueued")
	}

	// No second job should appear.
	select {
	case job := <-s.jobs:
		t.Fatalf("unexpected second job: %+v", job)
	case <-time.After(50 * time.Millisecond):
		// ok
	}
}
