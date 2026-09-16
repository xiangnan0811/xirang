package nodelogs

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"xirang/backend/internal/model"
)

func TestWorkerExecutionFailurePreservesAllCursorsAndRows(t *testing.T) {
	for _, failure := range []error{context.DeadlineExceeded, context.Canceled, ErrOutputLimit} {
		t.Run(failure.Error(), func(t *testing.T) {
			db := openCursorTestDB(t)
			if err := db.AutoMigrate(&LogEntry{}); err != nil {
				t.Fatal(err)
			}
			node := model.Node{ID: 819, Name: "failure-fixture", Host: "unused", Username: "unused", LogJournalctlEnabled: true, LogPaths: `["/var/log/test.log"]`}
			if err := db.Create(&node).Error; err != nil {
				t.Fatal(err)
			}
			repo := NewCursorRepo(db)
			seed := []Cursor{
				{NodeID: node.ID, Source: SourceJournalctl, CursorText: "previous"},
				{NodeID: node.ID, Source: SourceFile, Path: "/var/log/test.log", FileOffset: 123, FileInode: 456},
			}
			if err := repo.SaveForNode(node.ID, seed); err != nil {
				t.Fatal(err)
			}
			before, err := repo.LoadForNode(node.ID)
			if err != nil {
				t.Fatal(err)
			}
			out := `{"__REALTIME_TIMESTAMP":"1700000000000000","__CURSOR":"unsafe","MESSAGE":"partial"}` + "\n" + JournalDelim + "\nINODE=999 SIZE=99\npartial\n" + FileEnd
			w := Worker{db: db, curRepo: repo, fetcher: NewFetcher(&fakeRunner{out: out, err: fmt.Errorf("execution failed: %w", failure)})}
			w.process(context.Background(), CollectJob{Node: node})
			after, err := repo.LoadForNode(node.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed fetch modified cursor state")
			}
			var count int64
			if err := db.Model(&LogEntry{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("failed fetch inserted %d rows", count)
			}
		})
	}
}
