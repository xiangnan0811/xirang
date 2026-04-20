package nodelogs

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/model"
)

// Runner is the SSH abstraction. Production implementation in ssh_runner.go (Task 5).
type Runner interface {
	Run(ctx context.Context, node model.Node, cmd string, timeout time.Duration, maxBytes int) (string, error)
}

type Fetcher struct {
	runner Runner
}

func NewFetcher(r Runner) *Fetcher { return &Fetcher{runner: r} }

// Fetch batches journalctl + file whitelist into one SSH invocation. Returns parsed
// entries, new cursor state (for ALL configured sources, even empty results), and error.
// On error, cursors are NOT touched (caller should not persist).
func (f *Fetcher) Fetch(ctx context.Context, node model.Node, cursors map[CursorKey]Cursor) ([]LogEntry, []Cursor, error) {
	if !node.LogJournalctlEnabled && len(node.DecodedLogPaths()) == 0 {
		return nil, nil, nil
	}
	script := buildScript(node, cursors)
	start := time.Now()
	out, err := f.runner.Run(ctx, node, script, FetchTimeout, MaxFetchBytes)
	fetchDuration.WithLabelValues(nodeIDLabel(node.ID)).Observe(time.Since(start).Seconds())
	if err != nil {
		reason := "ssh_error"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "timeout"
		}
		fetchErrors.WithLabelValues(nodeIDLabel(node.ID), reason).Inc()
		return nil, nil, err
	}

	var entries []LogEntry
	var newCursors []Cursor

	delimIdx := strings.Index(out, JournalDelim)
	var journalRaw, filesRaw string
	if delimIdx >= 0 {
		journalRaw = out[:delimIdx]
		filesRaw = out[delimIdx+len(JournalDelim):]
	} else {
		journalRaw = out
	}

	// Journal block
	if node.LogJournalctlEnabled {
		jEntries, lastCursor := parseJournalJSON(node.ID, journalRaw)
		entries = append(entries, jEntries...)
		prev := cursors[CursorKey{SourceJournalctl, ""}].CursorText
		if lastCursor == "" {
			lastCursor = prev
		}
		newCursors = append(newCursors, Cursor{
			NodeID: node.ID, Source: SourceJournalctl, Path: "", CursorText: lastCursor,
		})
	}

	// File blocks
	paths := node.DecodedLogPaths()
	if len(paths) > 0 {
		chunks := strings.Split(filesRaw, FileEnd)
		for i, path := range paths {
			if i >= len(chunks) {
				break
			}
			chunk := strings.TrimLeft(chunks[i], "\n")
			inode, _, body := parseStatHeader(chunk)
			prev := cursors[CursorKey{SourceFile, path}]
			var fileEntries []LogEntry
			var newOffset int64
			if prev.FileInode != 0 && inode != 0 && inode != prev.FileInode {
				fileEntries, newOffset = parseFileChunk(node.ID, path, body, 0)
			} else {
				fileEntries, newOffset = parseFileChunk(node.ID, path, body, prev.FileOffset)
			}
			entries = append(entries, fileEntries...)
			newCursors = append(newCursors, Cursor{
				NodeID: node.ID, Source: SourceFile, Path: path,
				FileOffset: newOffset, FileInode: inode,
			})
		}
	}

	return entries, newCursors, nil
}

// buildScript generates the bash script to run on the remote node.
func buildScript(node model.Node, cursors map[CursorKey]Cursor) string {
	var b strings.Builder

	if node.LogJournalctlEnabled {
		prev := cursors[CursorKey{SourceJournalctl, ""}].CursorText
		if prev != "" {
			b.WriteString(fmt.Sprintf(
				`( journalctl --after-cursor="%s" --output=json --output-fields=__REALTIME_TIMESTAMP,__CURSOR,PRIORITY,_SYSTEMD_UNIT,MESSAGE --no-pager 2>/dev/null ) || true`+"\n",
				shellEscape(prev),
			))
		} else {
			b.WriteString(
				`( journalctl -n 200 --output=json --output-fields=__REALTIME_TIMESTAMP,__CURSOR,PRIORITY,_SYSTEMD_UNIT,MESSAGE --no-pager 2>/dev/null ) || true` + "\n",
			)
		}
	}
	b.WriteString(JournalDelim + "\n")

	for _, path := range node.DecodedLogPaths() {
		prev := cursors[CursorKey{SourceFile, path}]
		// tail -c uses 1-based indexing; FileOffset=0 → "+1" reads the full file.
		offsetArg := prev.FileOffset + 1
		b.WriteString(fmt.Sprintf(
			`( stat -c "INODE=%%i SIZE=%%s" "%s" 2>/dev/null; tail -c +%d "%s" 2>/dev/null ) || true`+"\n",
			shellEscape(path), offsetArg, shellEscape(path),
		))
		b.WriteString(FileEnd + "\n")
	}

	return b.String()
}

func shellEscape(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

var statLineRE = regexp.MustCompile(`^INODE=(\d+) SIZE=(\d+)\s*\n`)

// parseStatHeader peels the "INODE=X SIZE=Y" header off a file chunk.
func parseStatHeader(chunk string) (int64, int64, string) {
	m := statLineRE.FindStringSubmatchIndex(chunk)
	if m == nil {
		return 0, 0, chunk
	}
	inode, _ := strconv.ParseInt(chunk[m[2]:m[3]], 10, 64)
	size, _ := strconv.ParseInt(chunk[m[4]:m[5]], 10, 64)
	body := chunk[m[1]:]
	return inode, size, body
}
