package nodelogs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"gorm.io/gorm"

	"xirang/backend/internal/model"
)

func recoveryCounterValue(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()
	var metric dto.Metric
	if err := counter.Write(&metric); err != nil {
		t.Fatal(err)
	}
	return metric.GetCounter().GetValue()
}

// shellFixtureRunner deliberately accepts a complete nonzero exit just like the
// SSH adapter. The fetch protocol must prove success independently of exit code.
type shellFixtureRunner struct {
	t        *testing.T
	dir      string
	commands []string
}

func (r *shellFixtureRunner) Run(ctx context.Context, _ model.Node, script string, _ time.Duration, maxBytes int) (string, error) {
	r.commands = append(r.commands, script)
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+r.dir+":"+os.Getenv("PATH"), "JOURNAL_FIXTURE="+r.dir)
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if len(out) > maxBytes {
		return "", ErrOutputLimit
	}
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return "", err
		}
	}
	return string(out), nil
}

func newShellFixture(t *testing.T, count int) *shellFixtureRunner {
	t.Helper()
	r := &shellFixtureRunner{t: t, dir: t.TempDir()}
	fixture := `#!/bin/sh
printf '%s\n' "$@" >> "$JOURNAL_FIXTURE/args"
if test -f "$JOURNAL_FIXTURE/fail"; then printf '%s\n' '{"MESSAGE":"FAKE_FAILURE_FOR_TEST_ONLY"}'; exit 3; fi
cursor= after= reverse=0 metadata=0 limit=200
while test "$#" -gt 0; do
  case "$1" in
    --cursor=*) cursor=${1#*=};;
    --after-cursor=*) after=${1#*=};;
    --reverse) reverse=1;;
    -n) shift; limit=$1;;
    --output-fields=*) case "$1" in *MESSAGE*) ;; *) metadata=1;; esac;;
  esac
  shift
done
awk -F '|' -v cursor="$cursor" -v after="$after" -v reverse="$reverse" -v metadata="$metadata" -v limit="$limit" '
 { c[NR]=$1; ts[NR]=$2; body[NR]=$3; if ($1==cursor) start=NR; if ($1==after) begin=NR+1 }
 END {
   if(cursor!="") { if(!start) start=1; end=start; }
   else if(after!="") { start=begin; if(!start) start=1; end=NR; }
   else {start=1; end=NR;}
   if(reverse) { for(i=end;i>=start && n<limit;i--) { print body[i]; n++ } }
   else { for(i=start;i<=end && n<limit;i++) { if(metadata) printf "{\"__CURSOR\":\"%s\",\"__REALTIME_TIMESTAMP\":\"%s\"}\n",c[i],ts[i]; else print body[i]; n++ } }
 }' "$JOURNAL_FIXTURE/data"
`
	if err := os.WriteFile(filepath.Join(r.dir, "journalctl"), []byte(fixture), 0700); err != nil {
		t.Fatal(err)
	}
	r.writeEntries(count, time.Now().Add(-time.Minute))
	return r
}

func (r *shellFixtureRunner) writeEntries(count int, start time.Time) {
	r.t.Helper()
	var b strings.Builder
	for i := 1; i <= count; i++ {
		stamp := start.Add(time.Duration(i) * time.Millisecond).UnixMicro()
		fmt.Fprintf(&b, "c%d|%d|{\"__CURSOR\":\"c%d\",\"__REALTIME_TIMESTAMP\":\"%d\",\"MESSAGE\":\"entry%d\"}\n", i, stamp, i, stamp, i)
	}
	if err := os.WriteFile(filepath.Join(r.dir, "data"), []byte(b.String()), 0600); err != nil {
		r.t.Fatal(err)
	}
}

func TestGeneratedShellRecentBootstrap(t *testing.T) {
	r := newShellFixture(t, 450)
	entries, cursors, err := NewFetcher(r).Fetch(context.Background(), model.Node{ID: 1, LogJournalctlEnabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 200 || len(cursors) != 1 || cursors[0].CursorText != "c450" {
		t.Fatalf("bootstrap: entries=%d cursors=%v", len(entries), cursors)
	}
	if !entries[0].Timestamp.Before(entries[199].Timestamp) {
		t.Fatal("bootstrap must return chronological order")
	}
}

func TestGeneratedShellSuccessiveBatches(t *testing.T) {
	r := newShellFixture(t, 451)
	f := NewFetcher(r)
	node := model.Node{ID: 1, LogJournalctlEnabled: true}
	prev := Cursor{CursorText: "c1", UpdatedAt: time.Now()}
	for _, want := range []struct {
		count  int
		cursor string
	}{{200, "c201"}, {200, "c401"}, {50, "c451"}, {0, "c451"}} {
		entries, cs, err := f.Fetch(context.Background(), node, map[CursorKey]Cursor{{SourceJournalctl, ""}: prev})
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != want.count || cs[0].CursorText != want.cursor || cs[0].recoveryReason != "" {
			t.Fatalf("batch count=%d cursor=%+v want=%+v", len(entries), cs, want)
		}
		prev = cs[0]
		prev.UpdatedAt = time.Now()
	}
	if len(r.commands) != 8 {
		t.Fatalf("want 4 bounded probe/fetch pairs, got %d", len(r.commands))
	}
}

func TestGeneratedShellRecoveryDecisions(t *testing.T) {
	for _, tt := range []struct {
		name, cursor, reason string
		updated, event       time.Time
	}{
		{"stale", "c1", "stale_poll", time.Now().Add(-120 * 24 * time.Hour), time.Now().Add(-time.Minute)},
		{"unknown", "c1", "unknown_age", time.Time{}, time.Now().Add(-time.Minute)},
		{"vacuumed", "missing", "cursor_unavailable", time.Now(), time.Now().Add(-time.Minute)},
		{"event_expired", "c1", "window_expired", time.Now(), time.Now().Add(-2 * time.Hour)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newShellFixture(t, 450)
			if tt.name == "event_expired" {
				data, err := os.ReadFile(filepath.Join(r.dir, "data"))
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.Split(string(data), "\n")
				stamp := tt.event.UnixMicro()
				lines[0] = fmt.Sprintf("c1|%d|{\"__CURSOR\":\"c1\",\"__REALTIME_TIMESTAMP\":\"%d\",\"MESSAGE\":\"old\"}", stamp, stamp)
				if err := os.WriteFile(filepath.Join(r.dir, "data"), []byte(strings.Join(lines, "\n")), 0600); err != nil {
					t.Fatal(err)
				}
			}
			entries, cs, err := NewFetcher(r).Fetch(context.Background(), model.Node{ID: 1, LogJournalctlEnabled: true}, map[CursorKey]Cursor{{SourceJournalctl, ""}: {CursorText: tt.cursor, UpdatedAt: tt.updated}})
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 200 || cs[0].CursorText != "c450" || cs[0].recoveryReason != tt.reason {
				t.Fatalf("recovery count=%d cursor=%+v", len(entries), cs)
			}
			if strings.Contains(r.commands[len(r.commands)-1], "--after-cursor") {
				t.Fatal("rebaseline revived old token")
			}
		})
	}
}

func TestGeneratedShellMixedFilesAndQuoting(t *testing.T) {
	r := newShellFixture(t, 1)
	path := filepath.Join(r.dir, "a'$(touch FAKE_INJECTION_FOR_TEST_ONLY).log")
	if err := os.WriteFile(path, []byte("line1\npartial"), 0600); err != nil {
		t.Fatal(err)
	}
	paths, err := json.Marshal([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	entries, cs, err := NewFetcher(r).Fetch(context.Background(), model.Node{ID: 1, LogJournalctlEnabled: true, LogPaths: string(paths)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || len(cs) != 2 || cs[1].FileOffset != 6 {
		t.Fatalf("mixed result entries=%d cursors=%+v", len(entries), cs)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "FAKE_INJECTION_FOR_TEST_ONLY")); !os.IsNotExist(err) {
		t.Fatal("path executed")
	}
	// A quoted cursor is passed as one opaque argument during metadata probing.
	token := "FAKE'$(false)`false`_FOR_TEST_ONLY"
	_, _, err = NewFetcher(r).Fetch(context.Background(), model.Node{ID: 1, LogJournalctlEnabled: true}, map[CursorKey]Cursor{{SourceJournalctl, ""}: {CursorText: token, UpdatedAt: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(r.dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--cursor="+token+"\n") {
		t.Fatal("cursor shell quoting changed argument")
	}
}

func TestJournalProtocolRejectsMalformedAndAcceptsMarkerInMessage(t *testing.T) {
	row := fmt.Sprintf(`{"__CURSOR":"c1","__REALTIME_TIMESTAMP":"%d","MESSAGE":"%s"}`, time.Now().UnixMicro(), JournalDelim)
	for _, tt := range []struct {
		name, raw string
		valid     bool
	}{
		{"message_marker", row + "\n" + JournalDelim + "\n" + FetchEnd + "\n", true},
		{"missing_trailer", row + "\n" + JournalDelim + "\n", false},
		{"missing_separator", row + "\n" + FetchEnd + "\n", false},
		{"malformed_row", row + "\n{broken}\n" + JournalDelim + "\n" + FetchEnd + "\n", false},
		{"extra_file", row + "\n" + JournalDelim + "\n\n" + FileEnd + "\n" + FetchEnd + "\n", false},
		{"array_message", strings.Replace(row, `"MESSAGE":"`+JournalDelim+`"`, `"MESSAGE":[65,66]`, 1) + "\n" + JournalDelim + "\n" + FetchEnd + "\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entries, cs, err := NewFetcher(&fakeRunner{out: tt.raw}).Fetch(context.Background(), model.Node{ID: 1, LogJournalctlEnabled: true}, nil)
			if tt.valid {
				if err != nil || len(entries) != 1 || len(cs) != 1 {
					t.Fatalf("valid payload rejected: %v", err)
				}
			} else if !errors.Is(err, errFetchProtocol) || len(entries) != 0 || len(cs) != 0 {
				t.Fatalf("invalid payload accepted: %v", err)
			}
		})
	}
}

func TestWorkerEmptyResetAndRecoveryFailures(t *testing.T) {
	for _, stage := range []string{"empty_success", "command", "insert", "cursor_save"} {
		t.Run(stage, func(t *testing.T) {
			db := openCursorTestDB(t)
			if err := db.AutoMigrate(&LogEntry{}); err != nil {
				t.Fatal(err)
			}
			node := model.Node{ID: 1, LogJournalctlEnabled: true}
			repo := NewCursorRepo(db)
			if err := repo.SaveForNode(1, []Cursor{{Source: SourceJournalctl, CursorText: "FAKE_OLD_CURSOR_FOR_TEST_ONLY"}}); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-120 * 24 * time.Hour)
			if err := db.Model(&model.NodeLogCursor{}).Where("node_id = ?", 1).UpdateColumn("updated_at", old).Error; err != nil {
				t.Fatal(err)
			}
			r := newShellFixture(t, 1)
			if stage == "empty_success" {
				r.writeEntries(0, time.Now())
			}
			if stage == "command" {
				if err := os.WriteFile(filepath.Join(r.dir, "fail"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "insert" || stage == "cursor_save" {
				if err := db.Callback().Create().Before("gorm:create").Register("test:recovery_failure", func(tx *gorm.DB) {
					if stage == "insert" && tx.Statement.Table == "node_logs" || stage == "cursor_save" && tx.Statement.Table == "node_log_cursors" {
						_ = tx.AddError(errors.New("FAKE_DB_FAILURE_FOR_TEST_ONLY"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			before := recoveryCounterValue(t, journalRecoveries.WithLabelValues("stale_poll"))
			w := Worker{db: db, curRepo: repo, fetcher: NewFetcher(r)}
			w.process(context.Background(), CollectJob{Node: node})
			stored, err := repo.LoadForNode(1)
			if err != nil {
				t.Fatal(err)
			}
			got := stored[CursorKey{SourceJournalctl, ""}]
			after := recoveryCounterValue(t, journalRecoveries.WithLabelValues("stale_poll"))
			if stage != "empty_success" {
				if got.CursorText != "FAKE_OLD_CURSOR_FOR_TEST_ONLY" || !got.UpdatedAt.Equal(old) || after != before {
					t.Fatalf("failure changed cursor/recovery evidence: %+v %v/%v", got, before, after)
				}
				return
			}
			if got.CursorText != "" || !got.UpdatedAt.After(old) || after != before+1 {
				t.Fatalf("empty reset not persisted: %+v %v/%v", got, before, after)
			}
			w.process(context.Background(), CollectJob{Node: node})
			if recoveryCounterValue(t, journalRecoveries.WithLabelValues("stale_poll")) != after {
				t.Fatal("empty poll repeated gap event")
			}
			r.writeEntries(1, time.Now().Add(-time.Second))
			w.process(context.Background(), CollectJob{Node: node})
			stored, err = repo.LoadForNode(1)
			if err != nil {
				t.Fatal(err)
			}
			if stored[CursorKey{SourceJournalctl, ""}].CursorText != "c1" {
				t.Fatal("new entries did not establish new cursor")
			}
			for _, cmd := range r.commands {
				if strings.Contains(cmd, "FAKE_OLD_CURSOR_FOR_TEST_ONLY") {
					t.Fatal("old token revived")
				}
			}
		})
	}
}

type recoveryRunnerFunc func(context.Context, model.Node, string, time.Duration, int) (string, error)

func (f recoveryRunnerFunc) Run(ctx context.Context, node model.Node, cmd string, timeout time.Duration, max int) (string, error) {
	return f(ctx, node, cmd, timeout, max)
}

func TestRecoverySharesContextAndByteBudget(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(fmt.Sprintf("overflow_%t", overflow), func(t *testing.T) {
			probe := fmt.Sprintf(`{"__CURSOR":"c1","__REALTIME_TIMESTAMP":"%d"}`, time.Now().UnixMicro()) + "\n" + FetchEnd + "\n"
			calls := 0
			var firstCtx context.Context
			r := recoveryRunnerFunc(func(ctx context.Context, _ model.Node, _ string, timeout time.Duration, max int) (string, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > FetchTimeout || timeout != FetchTimeout {
					t.Fatal("missing overall timeout")
				}
				if calls == 1 {
					firstCtx = ctx
					if max != MaxFetchBytes {
						t.Fatal("wrong initial budget")
					}
					return probe, nil
				}
				if ctx != firstCtx || max != MaxFetchBytes-len(probe) {
					t.Fatal("probe and collection did not share deadline/budget")
				}
				if overflow {
					return strings.Repeat("x", max+1), nil
				}
				return "\n" + JournalDelim + "\n" + FetchEnd + "\n", nil
			})
			entries, cs, err := NewFetcher(r).Fetch(context.Background(), model.Node{ID: 1, LogJournalctlEnabled: true}, map[CursorKey]Cursor{{SourceJournalctl, ""}: {CursorText: "c1", UpdatedAt: time.Now()}})
			if calls != 2 {
				t.Fatalf("calls=%d", calls)
			}
			if overflow {
				if !errors.Is(err, ErrOutputLimit) || len(entries) != 0 || len(cs) != 0 {
					t.Fatalf("overflow accepted: %v", err)
				}
			} else if err != nil || len(cs) != 1 || cs[0].CursorText != "c1" {
				t.Fatalf("empty continuation lost cursor: %v", err)
			}
		})
	}
}

func TestRecoveryProbeFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"command", "cancel", "malformed", "empty"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			r := recoveryRunnerFunc(func(_ context.Context, _ model.Node, _ string, _ time.Duration, _ int) (string, error) {
				calls++
				if calls == 2 {
					return "\n" + JournalDelim + "\n" + FetchEnd + "\n", nil
				}
				switch mode {
				case "command":
					return "", nil
				case "cancel":
					cancel()
					return "", nil
				case "malformed":
					return "{broken}\n" + FetchEnd + "\n", nil
				default:
					return "\n" + FetchEnd + "\n", nil
				}
			})
			entries, cs, err := NewFetcher(r).Fetch(ctx, model.Node{ID: 1, LogJournalctlEnabled: true}, map[CursorKey]Cursor{{SourceJournalctl, ""}: {CursorText: "c1", UpdatedAt: time.Now()}})
			if mode == "empty" {
				if err != nil || calls != 2 || len(cs) != 1 || cs[0].CursorText != "" || cs[0].recoveryReason != "cursor_unavailable" {
					t.Fatalf("empty probe reset: %v %+v", err, cs)
				}
				return
			}
			if err == nil || calls != 1 || len(entries) != 0 || len(cs) != 0 {
				t.Fatalf("failed probe progressed: calls=%d err=%v", calls, err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}

func TestGeneratedShellMissingFileFails(t *testing.T) {
	r := newShellFixture(t, 1)
	paths, err := json.Marshal([]string{filepath.Join(r.dir, "missing")})
	if err != nil {
		t.Fatal(err)
	}
	entries, cs, err := NewFetcher(r).Fetch(context.Background(), model.Node{ID: 1, LogJournalctlEnabled: true, LogPaths: string(paths)}, nil)
	if !errors.Is(err, errFetchProtocol) || len(entries) != 0 || len(cs) != 0 {
		t.Fatalf("file failure accepted partial journal: %v", err)
	}
}
