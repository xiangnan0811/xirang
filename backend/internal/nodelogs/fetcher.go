package nodelogs

import (
	"context"
	"encoding/json"
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

// Fetch batches journalctl + file whitelist, probing a recent journal cursor first.
// Both SSH invocations share one deadline and cumulative byte budget. Returns parsed
// entries, new cursor state (for ALL configured sources, even empty results), and error.
// On error, cursors are NOT touched (caller should not persist).
func (f *Fetcher) Fetch(ctx context.Context, node model.Node, cursors map[CursorKey]Cursor) (entries []LogEntry, newCursors []Cursor, err error) {
	if !node.LogJournalctlEnabled && len(node.DecodedLogPaths()) == 0 {
		return nil, nil, nil
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	defer func() {
		fetchDuration.WithLabelValues(nodeIDLabel(node.ID)).Observe(time.Since(start).Seconds())
		if err == nil {
			return
		}
		reason := "ssh_error"
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			reason = "timeout"
		case errors.Is(err, context.Canceled):
			reason = "canceled"
		case errors.Is(err, ErrOutputLimit):
			reason = "output_limit"
		case errors.Is(err, errFetchProtocol):
			reason = "protocol"
		}
		fetchErrors.WithLabelValues(nodeIDLabel(node.ID), reason).Inc()
	}()
	remaining := MaxFetchBytes
	run := func(script string) (string, error) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if remaining <= 0 {
			return "", ErrOutputLimit
		}
		out, runErr := f.runner.Run(ctx, node, script, FetchTimeout, remaining)
		if runErr != nil {
			return "", runErr
		}
		remaining -= len(out)
		if remaining < 0 {
			return "", ErrOutputLimit
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return out, nil
	}
	prev := cursors[CursorKey{SourceJournalctl, ""}]
	resume, resetReason := journalResumePolicy(prev, start)
	if node.LogJournalctlEnabled && resume {
		probe, probeErr := run(buildJournalProbe(prev.CursorText))
		if probeErr != nil {
			return nil, nil, probeErr
		}
		metadata, probeErr := parseJournalProbe(probe)
		if probeErr != nil {
			return nil, nil, probeErr
		}
		switch {
		case metadata.Cursor != prev.CursorText:
			resume, resetReason = false, "cursor_unavailable"
		case metadata.Timestamp.Before(start.Add(-JournalRecoveryWindow)):
			resume, resetReason = false, "window_expired"
		}
	}
	out, err := run(buildCollectionScript(node, cursors, resume))
	if err != nil {
		return nil, nil, err
	}
	journalRaw, fileChunks, err := splitFetchOutput(out, len(node.DecodedLogPaths()))
	if err != nil {
		return nil, nil, err
	}

	// Journal block
	if node.LogJournalctlEnabled {
		jEntries, lastCursor, skipped, parseErr := parseCollectedJournal(node.ID, journalRaw, !resume, start.Add(-JournalRecoveryWindow))
		if parseErr != nil {
			return nil, nil, parseErr
		}
		entries = append(entries, jEntries...)
		if skipped && resetReason == "" {
			resetReason = "window_expired"
		}
		if lastCursor == "" && resume {
			lastCursor = prev.CursorText
		}
		newCursors = append(newCursors, Cursor{
			NodeID: node.ID, Source: SourceJournalctl, Path: "", CursorText: lastCursor,
			recoveryReason: resetReason,
		})
	}

	// File blocks
	paths := node.DecodedLogPaths()
	if len(paths) > 0 {
		for i, path := range paths {
			chunk := fileChunks[i]
			if !statLineRE.MatchString(chunk) {
				return nil, nil, errFetchProtocol
			}
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

// buildScript generates a POSIX shell collection script. Fetch additionally
// probes a recent token before trusting its journal event age and identity.
func buildScript(node model.Node, cursors map[CursorKey]Cursor) string {
	resume, _ := journalResumePolicy(cursors[CursorKey{SourceJournalctl, ""}], time.Now())
	return buildCollectionScript(node, cursors, resume)
}

func buildCollectionScript(node model.Node, cursors map[CursorKey]Cursor, resume bool) string {
	var b strings.Builder

	if node.LogJournalctlEnabled {
		prev := cursors[CursorKey{SourceJournalctl, ""}].CursorText
		if resume {
			fmt.Fprintf(&b,
				`journalctl --after-cursor=%s -n %d --output=json --output-fields=__REALTIME_TIMESTAMP,__CURSOR,PRIORITY,_SYSTEMD_UNIT,MESSAGE --no-pager 2>/dev/null || exit 1`+"\n",
				shellQuote(prev), JournalBatchSize,
			)
		} else {
			fmt.Fprintf(&b,
				`journalctl --since=%s --reverse -n %d --output=json --output-fields=__REALTIME_TIMESTAMP,__CURSOR,PRIORITY,_SYSTEMD_UNIT,MESSAGE --no-pager 2>/dev/null || exit 1`+"\n",
				shellQuote(fmt.Sprintf("-%d seconds", int(JournalRecoveryWindow.Seconds()))), JournalBatchSize,
			)
		}
	}
	fmt.Fprintf(&b, "printf '\\n%%s\\n' %s\n", shellQuote(JournalDelim))

	for _, path := range node.DecodedLogPaths() {
		prev := cursors[CursorKey{SourceFile, path}]
		// tail -c uses 1-based indexing; FileOffset=0 → "+1" reads the full file.
		offsetArg := prev.FileOffset + 1
		quoted := shellQuote(path)
		fmt.Fprintf(&b,
			`stat -c "INODE=%%i SIZE=%%s" %s 2>/dev/null && tail -c +%d %s 2>/dev/null || exit 1`+"\n",
			quoted, offsetArg, quoted,
		)
		fmt.Fprintf(&b, "printf '\\n%%s\\n' %s\n", shellQuote(FileEnd))
	}

	fmt.Fprintf(&b, "printf '%%s\\n' %s\n", shellQuote(FetchEnd))
	return b.String()
}

var errFetchProtocol = errors.New("node log collection protocol failed")

func journalResumePolicy(prev Cursor, now time.Time) (bool, string) {
	if prev.CursorText == "" {
		return false, ""
	}
	if prev.UpdatedAt.IsZero() {
		return false, "unknown_age"
	}
	if prev.UpdatedAt.Before(now.Add(-JournalRecoveryWindow)) {
		return false, "stale_poll"
	}
	return true, ""
}

func buildJournalProbe(cursor string) string {
	return fmt.Sprintf("journalctl --cursor=%s -n 1 --output=json --output-fields=__REALTIME_TIMESTAMP,__CURSOR --no-pager 2>/dev/null || exit 1\nprintf '\\n%%s\\n' %s\n", shellQuote(cursor), shellQuote(FetchEnd))
}

type journalMetadata struct {
	Cursor    string
	Timestamp time.Time
}

func decodeJournalMetadata(raw string) (journalMetadata, error) {
	var row struct {
		Cursor    string `json:"__CURSOR"`
		Timestamp string `json:"__REALTIME_TIMESTAMP"`
	}
	if json.Unmarshal([]byte(raw), &row) != nil || row.Cursor == "" {
		return journalMetadata{}, errFetchProtocol
	}
	micros, err := strconv.ParseInt(row.Timestamp, 10, 64)
	if err != nil || micros <= 0 {
		return journalMetadata{}, errFetchProtocol
	}
	return journalMetadata{Cursor: row.Cursor, Timestamp: time.UnixMicro(micros).UTC()}, nil
}

func parseJournalProbe(out string) (journalMetadata, error) {
	raw, ok := strings.CutSuffix(out, "\n"+FetchEnd+"\n")
	if !ok {
		return journalMetadata{}, errFetchProtocol
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return journalMetadata{}, nil
	}
	return decodeJournalMetadata(raw)
}

func splitFetchOutput(out string, fileCount int) (string, []string, error) {
	out, ok := strings.CutSuffix(out, FetchEnd+"\n")
	if !ok {
		return "", nil, errFetchProtocol
	}
	parts := strings.Split(out, "\n"+JournalDelim+"\n")
	if len(parts) != 2 {
		return "", nil, errFetchProtocol
	}
	chunks := strings.Split(parts[1], "\n"+FileEnd+"\n")
	if len(chunks) != fileCount+1 || chunks[len(chunks)-1] != "" {
		return "", nil, errFetchProtocol
	}
	return parts[0], chunks[:fileCount], nil
}

// Validate every row before advancing its cursor. systemd may represent MESSAGE
// and other user fields as arrays/null; metadata is still sufficient to consume
// that entry safely, with the existing secret-safe placeholder for its body.
func parseCollectedJournal(nodeID uint, raw string, reverse bool, cutoff time.Time) ([]LogEntry, string, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, "", false, nil
	}
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) > JournalBatchSize {
		return nil, "", false, errFetchProtocol
	}
	if reverse {
		for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
			lines[i], lines[j] = lines[j], lines[i]
		}
	}
	var entries []LogEntry
	var last string
	skipped := false
	for _, line := range lines {
		metadata, err := decodeJournalMetadata(line)
		if err != nil {
			return nil, "", false, err
		}
		last = metadata.Cursor
		if metadata.Timestamp.Before(cutoff) {
			skipped = true
			continue
		}
		parsed, _ := parseJournalJSON(nodeID, line)
		if len(parsed) == 0 {
			parsed = []LogEntry{{NodeID: nodeID, Source: SourceJournalctl, Timestamp: metadata.Timestamp, Message: nodeLogMessagePlaceholder}}
		}
		entries = append(entries, parsed...)
	}
	return entries, last, skipped, nil
}

// shellQuote wraps s in single quotes and escapes embedded single quotes via
// the canonical close-quote + backslash-quote + reopen sequence. This renders
// the value as a single opaque shell token, neutralizing $(...), `...`, \, $,
// "", and newlines in one stroke. Callers must NOT add their own quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
