package snapshot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
)

func resticLSHeader(snapshotID string) string {
	return fmt.Sprintf(`{"message_type":"snapshot","struct_type":"snapshot","id":%q,"time":"2026-09-12T07:38:33.377492457Z"}`+"\n", snapshotID)
}

func resticLSNode(name, nodeType, nodePath string, size any, mtime string) string {
	sizeJSON := ""
	if size != nil {
		sizeJSON = fmt.Sprintf(`,"size":%v`, size)
	}
	mtimeJSON := ""
	if mtime != "" {
		mtimeJSON = fmt.Sprintf(`,"mtime":%q`, mtime)
	}
	return fmt.Sprintf(`{"message_type":"node","struct_type":"node","name":%q,"type":%q,"path":%q%s%s}`+"\n",
		name, nodeType, nodePath, sizeJSON, mtimeJSON)
}

func validResticLSOutput(snapshotID string) string {
	return resticLSHeader(snapshotID) +
		resticLSNode("one.txt", "file", "/one.txt", 7, "2026-09-12T07:38:21.588239754Z") +
		resticLSNode("nested", "dir", "/nested", nil, "2026-09-12T07:38:21.588239754Z")
}

func TestParseResticLSOutputAcceptsCanonicalSnapshotAndEntries(t *testing.T) {
	entries, err := parseResticLSOutput(validResticLSOutput(indexerPointOne), indexerPointOne)
	if err != nil {
		t.Fatalf("parse Restic ls output: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries=%+v, want two entries", entries)
	}
	if entries[0].Path != "/one.txt" || entries[0].Size != 7 ||
		entries[0].Mtime != "2026-09-12T07:38:21.588239754Z" {
		t.Fatalf("file entry=%+v", entries[0])
	}
	if entries[1].Path != "/nested" || entries[1].Size != 0 ||
		entries[1].Mtime != "2026-09-12T07:38:21.588239754Z" {
		t.Fatalf("directory entry=%+v", entries[1])
	}
}

func TestParseResticLSOutputAcceptsHeaderOnlyEmptySnapshot(t *testing.T) {
	entries, err := parseResticLSOutput(resticLSHeader(indexerPointOne), indexerPointOne)
	if err != nil {
		t.Fatalf("parse empty Restic ls output: %v", err)
	}
	if entries == nil || len(entries) != 0 {
		t.Fatalf("empty snapshot entries=%+v, want non-nil empty slice", entries)
	}
}

func TestParseResticLSOutputAcceptsEitherResticRecordTypeField(t *testing.T) {
	output := `{"message_type":"snapshot","id":"` + indexerPointOne + `"}` + "\n" +
		`{"struct_type":"node","name":"one.txt","type":"file","path":"/one.txt","size":1}` + "\n"
	entries, err := parseResticLSOutput(output, indexerPointOne)
	if err != nil || len(entries) != 1 || entries[0].Path != "/one.txt" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
}

func TestParseResticLSOutputRejectsInvalidOrIncompleteStreams(t *testing.T) {
	valid := validResticLSOutput(indexerPointOne)
	node := resticLSNode("one.txt", "file", "/one.txt", 7, "2026-09-12T07:38:21Z")
	tests := []struct {
		name   string
		output string
	}{
		{"empty", ""},
		{"truncated final record", strings.TrimSuffix(valid, "\n")},
		{"malformed JSON", resticLSHeader(indexerPointOne) + `{"message_type":"node"` + "\n"},
		{"unknown record", resticLSHeader(indexerPointOne) + `{"message_type":"status"}` + "\n"},
		{"missing record type", resticLSHeader(indexerPointOne) + `{"id":"` + indexerPointOne + `"}` + "\n"},
		{"node before header", node + resticLSHeader(indexerPointOne)},
		{"duplicate header", resticLSHeader(indexerPointOne) + resticLSHeader(indexerPointOne)},
		{"mismatched identity", resticLSHeader(indexerPointTwo)},
		{"duplicate path", resticLSHeader(indexerPointOne) + node + node},
		{"noncanonical path", resticLSHeader(indexerPointOne) + resticLSNode("one.txt", "file", "/dir/../one.txt", 1, "")},
		{"negative size", resticLSHeader(indexerPointOne) + resticLSNode("one.txt", "file", "/one.txt", -1, "")},
		{"unsupported type", resticLSHeader(indexerPointOne) + resticLSNode("one.txt", "future", "/one.txt", 1, "")},
		{"conflicting record types", `{"message_type":"snapshot","struct_type":"node","id":"` + indexerPointOne + `"}` + "\n"},
		{"blank record", resticLSHeader(indexerPointOne) + "\n"},
		{"node name mismatch", resticLSHeader(indexerPointOne) + resticLSNode("other.txt", "file", "/one.txt", 1, "")},
		{"invalid mtime", resticLSHeader(indexerPointOne) + resticLSNode("one.txt", "file", "/one.txt", 1, "not-a-time")},
		{"missing file size", resticLSHeader(indexerPointOne) + `{"message_type":"node","name":"one.txt","type":"file","path":"/one.txt"}` + "\n"},
		{"null size", resticLSHeader(indexerPointOne) + `{"message_type":"node","name":"one.txt","type":"file","path":"/one.txt","size":null}` + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entries, err := parseResticLSOutput(test.output, indexerPointOne)
			if err == nil {
				t.Fatalf("entries=%+v, expected strict parser error", entries)
			}
			if entries != nil {
				t.Fatalf("malformed stream returned entries=%+v", entries)
			}
		})
	}
}

func TestParseResticLSOutputBoundsEntryCount(t *testing.T) {
	output := resticLSHeader(indexerPointOne) +
		resticLSNode("one.txt", "file", "/one.txt", 1, "") +
		resticLSNode("two.txt", "file", "/two.txt", 2, "")
	entries, err := parseResticLSReader(strings.NewReader(output), indexerPointOne, 1, 1<<20)
	if err == nil || !errors.Is(err, errResticLSResourceLimit) {
		t.Fatalf("entries=%+v err=%v, want resource limit", entries, err)
	}
}

type fakeResticLSExecution struct {
	*strings.Reader
	completion sshutil.CommandCompletion
	joinErr    error
	canceled   bool
}

func (execution *fakeResticLSExecution) Join() (sshutil.CommandCompletion, error) {
	return execution.completion, execution.joinErr
}

func (execution *fakeResticLSExecution) Cancel() error {
	execution.canceled = true
	return nil
}

func TestCollectResticLSExecutionRequiresSuccessfulCompletion(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		completion   sshutil.CommandCompletion
		joinErr      error
		wantErr      bool
		wantCanceled bool
		wantEntries  int
	}{
		{
			name:        "successful output",
			output:      validResticLSOutput(indexerPointOne),
			completion:  sshutil.CommandCompletion{ExitCodeKnown: true},
			wantEntries: 2,
		},
		{
			name:        "successful empty snapshot",
			output:      resticLSHeader(indexerPointOne),
			completion:  sshutil.CommandCompletion{ExitCodeKnown: true},
			wantEntries: 0,
		},
		{
			name:       "nonzero exit",
			output:     validResticLSOutput(indexerPointOne),
			completion: sshutil.CommandCompletion{ExitCode: 3, ExitCodeKnown: true},
			wantErr:    true,
		},
		{
			name:       "unknown completion",
			output:     validResticLSOutput(indexerPointOne),
			completion: sshutil.CommandCompletion{},
			wantErr:    true,
		},
		{
			name:       "interrupted lifecycle",
			output:     validResticLSOutput(indexerPointOne),
			completion: sshutil.CommandCompletion{},
			joinErr:    errors.New("interrupted SSH lifecycle"),
			wantErr:    true,
		},
		{
			name:         "malformed output",
			output:       validResticLSOutput(indexerPointOne) + `{"message_type":"node"` + "\n",
			completion:   sshutil.CommandCompletion{ExitCodeKnown: true},
			wantErr:      true,
			wantCanceled: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			execution := &fakeResticLSExecution{
				Reader: strings.NewReader(test.output), completion: test.completion, joinErr: test.joinErr,
			}
			entries, err := collectResticLSExecution(execution, indexerPointOne, defaultResticLSLimits())
			if test.wantErr {
				if err == nil {
					t.Fatalf("entries=%+v, expected completion/parser error", entries)
				}
				if entries != nil {
					t.Fatalf("failed execution returned partial entries=%+v", entries)
				}
				if execution.canceled != test.wantCanceled {
					t.Fatalf("canceled=%v, want %v", execution.canceled, test.wantCanceled)
				}
				return
			}
			if err != nil || len(entries) != test.wantEntries {
				t.Fatalf("entries=%+v err=%v, want %d entries", entries, err, test.wantEntries)
			}
		})
	}
}

func TestPublishResticLSIndexUsesCompatibilityScopedMarker(t *testing.T) {
	db := openIndexerTestDB(t)
	const taskID uint = 7
	if err := db.Create(&model.SnapshotFileIndex{
		TaskID: taskID, SnapshotID: indexerPointOne, Path: "/trusted.txt", Size: 5, Mtime: "old",
	}).Error; err != nil {
		t.Fatal(err)
	}
	entries := []resticLSEntry{{Path: "/one.txt", Size: 7, Mtime: "2026-09-12T07:38:21Z"}}
	if err := publishResticLSIndex(context.Background(), db, taskID, indexerPointOne, entries); err != nil {
		t.Fatalf("publish compatibility index: %v", err)
	}
	var rows []model.SnapshotFileIndex
	if err := db.Where("task_id = ? AND snapshot_id = ?", taskID, indexerPointOne).Order("path").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("published rows=%+v, want entry plus marker", rows)
	}
	if rows[0].Path != "" || rows[0].Mtime != legacyIndexCompleteMarkerMtime || rows[0].Size != 1 {
		t.Fatalf("compatibility marker=%+v", rows[0])
	}
	if rows[1].Path != "/one.txt" || rows[1].Mtime != entries[0].Mtime {
		t.Fatalf("published entry=%+v", rows[1])
	}
	if rows[0].Mtime == exactIndexCompleteMarkerMtime {
		t.Fatal("compatibility marker is not scoped away from exact readiness")
	}
}

func TestPublishResticLSIndexRollsBackOnWriteFailure(t *testing.T) {
	db := openIndexerTestDB(t)
	const taskID uint = 8
	previous := []model.SnapshotFileIndex{
		{TaskID: taskID, SnapshotID: indexerPointOne, Path: "/trusted.txt", Size: 5, Mtime: "old"},
		{TaskID: taskID, SnapshotID: indexerPointOne, Path: legacyIndexCompleteMarkerPath, Size: 1, Mtime: legacyIndexCompleteMarkerMtime},
	}
	if err := db.Create(&previous).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := []resticLSEntry{
		{Path: "/same.txt", Size: 1},
		{Path: "/same.txt", Size: 2},
	}
	if err := publishResticLSIndex(context.Background(), db, taskID, indexerPointOne, duplicate); err == nil {
		t.Fatal("duplicate compatibility entries unexpectedly published")
	}
	var rows []model.SnapshotFileIndex
	if err := db.Where("task_id = ? AND snapshot_id = ?", taskID, indexerPointOne).Order("path").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(previous) || rows[0].Path != "" || rows[1].Path != "/trusted.txt" {
		t.Fatalf("failed publication changed prior complete index: %+v", rows)
	}
}
