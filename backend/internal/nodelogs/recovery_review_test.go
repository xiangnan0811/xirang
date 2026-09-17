package nodelogs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestRecoveryCumulativeExactLimitAndExhaustedProbe(t *testing.T) {
	for _, exhaustProbe := range []bool{false, true} {
		t.Run(fmt.Sprintf("exhaust_probe_%t", exhaustProbe), func(t *testing.T) {
			probe := fmt.Sprintf(`{"__CURSOR":"c1","__REALTIME_TIMESTAMP":"%d"}`, time.Now().UnixMicro()) + "\n" + FetchEnd + "\n"
			calls := 0
			runner := recoveryRunnerFunc(func(_ context.Context, _ model.Node, _ string, _ time.Duration, max int) (string, error) {
				calls++
				if calls == 1 {
					if exhaustProbe {
						return strings.Repeat(" ", max-len(probe)) + probe, nil
					}
					return probe, nil
				}
				framing := "\n" + JournalDelim + "\n" + FetchEnd + "\n"
				return strings.Repeat(" ", max-len(framing)) + framing, nil
			})
			entries, cursors, err := NewFetcher(runner).Fetch(context.Background(), model.Node{ID: 1, LogJournalctlEnabled: true}, map[CursorKey]Cursor{
				{SourceJournalctl, ""}: {CursorText: "c1", UpdatedAt: time.Now()},
			})
			if exhaustProbe {
				if calls != 1 || !errors.Is(err, ErrOutputLimit) || len(entries) != 0 || len(cursors) != 0 {
					t.Fatalf("exhausted probe allowed collection: calls=%d err=%v", calls, err)
				}
			} else if err != nil || calls != 2 || len(entries) != 0 || len(cursors) != 1 || cursors[0].CursorText != "c1" {
				t.Fatalf("exact cumulative limit rejected: calls=%d err=%v cursors=%+v", calls, err, cursors)
			}
		})
	}
}

func TestGeneratedShellMultipleFilesPreservePartialLineOffsets(t *testing.T) {
	runner := newShellFixture(t, 0)
	first := filepath.Join(runner.dir, "first.log")
	second := filepath.Join(runner.dir, "second.log")
	for path, content := range map[string]string{first: "one\npartial", second: "two\n"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := json.Marshal([]string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	node := model.Node{ID: 1, LogPaths: string(paths)}
	fetcher := NewFetcher(runner)
	entries, cursors, err := fetcher.Fetch(context.Background(), node, nil)
	if err != nil || len(entries) != 2 || len(cursors) != 2 || cursors[0].FileOffset != 4 || cursors[1].FileOffset != 4 {
		t.Fatalf("first multi-file collection: err=%v entries=%d cursors=%+v", err, len(entries), cursors)
	}
	previous := map[CursorKey]Cursor{{SourceFile, first}: cursors[0], {SourceFile, second}: cursors[1]}
	if err := os.WriteFile(first, []byte("one\npartial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, cursors, err = fetcher.Fetch(context.Background(), node, previous)
	if err != nil || len(entries) != 1 || entries[0].Message != SanitizeLogMessage("partial") || len(cursors) != 2 || cursors[0].FileOffset != 12 || cursors[1].FileOffset != 4 {
		t.Fatalf("partial line continuation: err=%v entries=%+v cursors=%+v", err, entries, cursors)
	}
}
