package nodelogs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

// fakeRunner is a drop-in for the real SSH runner.
type fakeRunner struct {
	out string
	err error
}

func (f *fakeRunner) Run(ctx context.Context, node model.Node, cmd string, timeout time.Duration, maxBytes int) (string, error) {
	return f.out, f.err
}

func TestFetch_EmptyJournalAndFiles(t *testing.T) {
	out := "\n" + JournalDelim + "\n"
	f := &Fetcher{runner: &fakeRunner{out: out}}
	cs := map[CursorKey]Cursor{
		{SourceJournalctl, ""}: {NodeID: 1, Source: SourceJournalctl},
	}
	entries, newCursors, err := f.Fetch(context.Background(), model.Node{ID: 1, LogJournalctlEnabled: true}, cs)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("len=%d want 0", len(entries))
	}
	_ = newCursors
}

func TestFetch_JournalOnly(t *testing.T) {
	journalJSON := `{"__REALTIME_TIMESTAMP":"1713600000000000","__CURSOR":"c1","PRIORITY":"3","MESSAGE":"err1"}`
	out := journalJSON + "\n" + JournalDelim + "\n"
	f := &Fetcher{runner: &fakeRunner{out: out}}
	n := model.Node{ID: 1, LogJournalctlEnabled: true}
	cs := map[CursorKey]Cursor{
		{SourceJournalctl, ""}: {NodeID: 1, Source: SourceJournalctl, CursorText: "prev"},
	}
	entries, newCursors, err := f.Fetch(context.Background(), n, cs)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("len=%d want 1", len(entries))
	}
	if entries[0].Priority != "err" {
		t.Fatalf("pri=%q", entries[0].Priority)
	}
	found := false
	for _, c := range newCursors {
		if c.Source == SourceJournalctl && c.CursorText == "c1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected cursor 'c1', got %+v", newCursors)
	}
}

func TestFetch_FilesWithRotation(t *testing.T) {
	fileOut := "INODE=42 SIZE=100\nhello\nworld\n"
	out := "\n" + JournalDelim + "\n" + fileOut + "\n" + FileEnd + "\n"
	f := &Fetcher{runner: &fakeRunner{out: out}}
	n := model.Node{
		ID:       1,
		LogPaths: `["/var/log/app.log"]`,
	}
	cs := map[CursorKey]Cursor{
		{SourceFile, "/var/log/app.log"}: {NodeID: 1, Source: SourceFile, Path: "/var/log/app.log", FileInode: 42, FileOffset: 0},
	}
	entries, newCursors, err := f.Fetch(context.Background(), n, cs)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("len=%d want 2", len(entries))
	}
	var fileCursor *Cursor
	for i := range newCursors {
		if newCursors[i].Source == SourceFile {
			fileCursor = &newCursors[i]
		}
	}
	if fileCursor == nil {
		t.Fatal("no file cursor")
	}
	if fileCursor.FileInode != 42 {
		t.Fatalf("inode=%d want 42", fileCursor.FileInode)
	}
	if fileCursor.FileOffset == 0 {
		t.Fatalf("offset should have advanced")
	}
}

func TestFetch_InodeChangeResetsOffset(t *testing.T) {
	fileOut := "INODE=99 SIZE=50\nnew-line\n"
	out := "\n" + JournalDelim + "\n" + fileOut + "\n" + FileEnd + "\n"
	f := &Fetcher{runner: &fakeRunner{out: out}}
	n := model.Node{ID: 1, LogPaths: `["/var/log/app.log"]`}
	cs := map[CursorKey]Cursor{
		{SourceFile, "/var/log/app.log"}: {NodeID: 1, Source: SourceFile, Path: "/var/log/app.log", FileInode: 42, FileOffset: 1000},
	}
	_, newCursors, err := f.Fetch(context.Background(), n, cs)
	if err != nil {
		t.Fatal(err)
	}
	var fc *Cursor
	for i := range newCursors {
		if newCursors[i].Source == SourceFile {
			fc = &newCursors[i]
		}
	}
	if fc == nil {
		t.Fatal("no file cursor")
	}
	if fc.FileInode != 99 {
		t.Fatalf("inode=%d want 99 (new)", fc.FileInode)
	}
	if fc.FileOffset <= 0 || fc.FileOffset > 50 {
		t.Fatalf("offset=%d (want small positive after reset)", fc.FileOffset)
	}
}

func TestFetch_SSHError(t *testing.T) {
	f := &Fetcher{runner: &fakeRunner{err: errors.New("dial timeout")}}
	_, _, err := f.Fetch(context.Background(), model.Node{ID: 1, LogJournalctlEnabled: true}, map[CursorKey]Cursor{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildScript_JournalAfterCursor(t *testing.T) {
	n := model.Node{LogJournalctlEnabled: true}
	cs := map[CursorKey]Cursor{
		{SourceJournalctl, ""}: {CursorText: "abc123"},
	}
	script := buildScript(n, cs)
	if !strings.Contains(script, "--after-cursor=\"abc123\"") {
		t.Fatalf("missing after-cursor in script: %q", script)
	}
}

func TestBuildScript_FileWithOffset(t *testing.T) {
	n := model.Node{LogPaths: `["/var/log/a.log"]`}
	cs := map[CursorKey]Cursor{
		{SourceFile, "/var/log/a.log"}: {FileOffset: 500, FileInode: 42},
	}
	script := buildScript(n, cs)
	if !strings.Contains(script, "tail -c +501") {
		t.Fatalf("expected 'tail -c +501' in %q", script)
	}
	if !strings.Contains(script, "/var/log/a.log") {
		t.Fatalf("path missing")
	}
	if !strings.Contains(script, fmt.Sprintf("%s\n", FileEnd)) {
		t.Fatalf("file end delim missing")
	}
}
