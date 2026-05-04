package nodelogs

import (
	"testing"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openCursorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.Node{}, &model.NodeLogCursor{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestCursorRepo_SaveLoadJournalctl(t *testing.T) {
	db := openCursorTestDB(t)
	repo := NewCursorRepo(db)
	cs := []Cursor{{NodeID: 1, Source: SourceJournalctl, Path: "", CursorText: "abc"}}
	if err := repo.SaveForNode(1, cs); err != nil {
		t.Fatal(err)
	}
	got, err := repo.LoadForNode(1)
	if err != nil {
		t.Fatal(err)
	}
	if got[CursorKey{SourceJournalctl, ""}].CursorText != "abc" {
		t.Fatalf("cursor mismatch: %+v", got)
	}
}

func TestCursorRepo_SaveUpsert(t *testing.T) {
	db := openCursorTestDB(t)
	repo := NewCursorRepo(db)
	_ = repo.SaveForNode(1, []Cursor{{NodeID: 1, Source: SourceFile, Path: "/var/log/a", FileOffset: 100, FileInode: 42}})
	_ = repo.SaveForNode(1, []Cursor{{NodeID: 1, Source: SourceFile, Path: "/var/log/a", FileOffset: 200, FileInode: 42}})
	got, _ := repo.LoadForNode(1)
	c := got[CursorKey{SourceFile, "/var/log/a"}]
	if c.FileOffset != 200 {
		t.Fatalf("upsert failed, got offset %d", c.FileOffset)
	}
	var count int64
	db.Model(&model.NodeLogCursor{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 row after upsert, got %d", count)
	}
}

func TestCursorRepo_LoadEmptyNode(t *testing.T) {
	db := openCursorTestDB(t)
	repo := NewCursorRepo(db)
	got, err := repo.LoadForNode(999)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %+v", got)
	}
}
