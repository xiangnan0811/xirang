package nodelogs

import (
	"strings"
	"testing"
	"time"
)

func TestParseJournalJSON_SingleLine(t *testing.T) {
	raw := `{"__REALTIME_TIMESTAMP":"1713600000000000","__CURSOR":"abc123","PRIORITY":"6","_SYSTEMD_UNIT":"sshd.service","MESSAGE":"Accepted publickey"}`
	entries, lastCursor := parseJournalJSON(1, raw)
	if len(entries) != 1 {
		t.Fatalf("len=%d want 1", len(entries))
	}
	e := entries[0]
	if e.NodeID != 1 {
		t.Fatalf("NodeID=%d", e.NodeID)
	}
	if e.Source != SourceJournalctl {
		t.Fatalf("Source=%q", e.Source)
	}
	if e.Path != "sshd.service" {
		t.Fatalf("Path=%q", e.Path)
	}
	if e.Priority != "info" {
		t.Fatalf("Priority=%q (want info for 6)", e.Priority)
	}
	if !e.Timestamp.Equal(time.Unix(1713600000, 0).UTC()) {
		t.Fatalf("Timestamp=%v", e.Timestamp)
	}
	if e.Message != "Accepted publickey" {
		t.Fatalf("Message=%q", e.Message)
	}
	if lastCursor != "abc123" {
		t.Fatalf("lastCursor=%q", lastCursor)
	}
}

func TestParseJournalJSON_MultipleLines(t *testing.T) {
	raw := strings.Join([]string{
		`{"__REALTIME_TIMESTAMP":"1713600000000000","__CURSOR":"c1","PRIORITY":"3","MESSAGE":"err1"}`,
		`{"__REALTIME_TIMESTAMP":"1713600001000000","__CURSOR":"c2","PRIORITY":"4","MESSAGE":"warn1"}`,
		`{"__REALTIME_TIMESTAMP":"1713600002000000","__CURSOR":"c3","MESSAGE":"notice"}`,
	}, "\n")
	entries, lastCursor := parseJournalJSON(1, raw)
	if len(entries) != 3 {
		t.Fatalf("len=%d want 3", len(entries))
	}
	if entries[0].Priority != "err" {
		t.Fatalf("p0=%q", entries[0].Priority)
	}
	if entries[1].Priority != "warning" {
		t.Fatalf("p1=%q", entries[1].Priority)
	}
	if entries[2].Priority != "" {
		t.Fatalf("p2=%q want empty when missing", entries[2].Priority)
	}
	if lastCursor != "c3" {
		t.Fatalf("lastCursor=%q", lastCursor)
	}
}

func TestParseJournalJSON_SkipsMalformed(t *testing.T) {
	raw := strings.Join([]string{
		`{"__REALTIME_TIMESTAMP":"1713600000000000","__CURSOR":"c1","MESSAGE":"ok"}`,
		`not json at all`,
		`{"__REALTIME_TIMESTAMP":"1713600002000000","__CURSOR":"c2","MESSAGE":"also ok"}`,
	}, "\n")
	entries, last := parseJournalJSON(1, raw)
	if len(entries) != 2 {
		t.Fatalf("len=%d want 2", len(entries))
	}
	if last != "c2" {
		t.Fatalf("last=%q", last)
	}
}

func TestParseJournalJSON_Empty(t *testing.T) {
	entries, last := parseJournalJSON(1, "")
	if len(entries) != 0 {
		t.Fatalf("len=%d", len(entries))
	}
	if last != "" {
		t.Fatalf("last=%q", last)
	}
}

func TestParseFileChunk_CompleteLines(t *testing.T) {
	raw := "line1\nline2\nline3\n"
	entries, newOffset := parseFileChunk(1, "/var/log/x", raw, 100)
	if len(entries) != 3 {
		t.Fatalf("len=%d", len(entries))
	}
	if entries[0].Message != "line1" {
		t.Fatalf("msg0=%q", entries[0].Message)
	}
	if entries[0].Path != "/var/log/x" {
		t.Fatalf("path=%q", entries[0].Path)
	}
	if entries[0].Source != SourceFile {
		t.Fatalf("source=%q", entries[0].Source)
	}
	want := int64(100) + int64(len(raw))
	if newOffset != want {
		t.Fatalf("newOffset=%d want %d", newOffset, want)
	}
}

func TestParseFileChunk_TrailingPartial(t *testing.T) {
	raw := "line1\nline2\npartial-no-newline"
	entries, newOffset := parseFileChunk(1, "/a", raw, 0)
	if len(entries) != 2 {
		t.Fatalf("len=%d (trailing partial must be skipped)", len(entries))
	}
	lastNL := strings.LastIndex(raw, "\n") + 1
	if newOffset != int64(lastNL) {
		t.Fatalf("newOffset=%d want %d (stops at last newline)", newOffset, lastNL)
	}
}

func TestParseFileChunk_Empty(t *testing.T) {
	entries, newOffset := parseFileChunk(1, "/a", "", 200)
	if len(entries) != 0 {
		t.Fatalf("len=%d", len(entries))
	}
	if newOffset != 200 {
		t.Fatalf("newOffset=%d want 200 (unchanged)", newOffset)
	}
}

func TestMapPriority_AllLevels(t *testing.T) {
	cases := map[string]string{
		"0": "emerg", "1": "alert", "2": "crit", "3": "err",
		"4": "warning", "5": "notice", "6": "info", "7": "debug",
		"": "", "9": "",
	}
	for in, want := range cases {
		if got := mapPriority(in); got != want {
			t.Errorf("mapPriority(%q)=%q want %q", in, got, want)
		}
	}
}
