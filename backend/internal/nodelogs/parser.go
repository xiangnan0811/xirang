package nodelogs

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// mapPriority converts systemd numeric priority to string label. Empty for unknown.
func mapPriority(p string) string {
	switch p {
	case "0":
		return "emerg"
	case "1":
		return "alert"
	case "2":
		return "crit"
	case "3":
		return "err"
	case "4":
		return "warning"
	case "5":
		return "notice"
	case "6":
		return "info"
	case "7":
		return "debug"
	default:
		return ""
	}
}

type journalLine struct {
	RealtimeTimestamp string `json:"__REALTIME_TIMESTAMP"`
	Cursor            string `json:"__CURSOR"`
	Priority          string `json:"PRIORITY"`
	SystemdUnit       string `json:"_SYSTEMD_UNIT"`
	Message           string `json:"MESSAGE"`
}

// parseJournalJSON parses the concatenated JSON-line output of `journalctl --output=json`.
// Returns entries in order plus the final cursor seen. Malformed lines are skipped.
func parseJournalJSON(nodeID uint, raw string) ([]LogEntry, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ""
	}
	lines := strings.Split(raw, "\n")
	entries := make([]LogEntry, 0, len(lines))
	lastCursor := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var jl journalLine
		if err := json.Unmarshal([]byte(line), &jl); err != nil {
			continue
		}
		tsMicro, err := strconv.ParseInt(jl.RealtimeTimestamp, 10, 64)
		if err != nil {
			continue
		}
		ts := time.Unix(tsMicro/1_000_000, (tsMicro%1_000_000)*1_000).UTC()
		if jl.Cursor != "" {
			lastCursor = jl.Cursor
		}
		entries = append(entries, LogEntry{
			NodeID:    nodeID,
			Source:    SourceJournalctl,
			Path:      jl.SystemdUnit,
			Timestamp: ts,
			Priority:  mapPriority(jl.Priority),
			Message:   jl.Message,
		})
	}
	return entries, lastCursor
}

// parseFileChunk splits raw text into one LogEntry per complete line. Trailing
// text without a newline is NOT consumed — offset advances only to the byte
// after the final newline.
func parseFileChunk(nodeID uint, path, raw string, prevOffset int64) ([]LogEntry, int64) {
	if raw == "" {
		return nil, prevOffset
	}
	lastNL := strings.LastIndex(raw, "\n")
	if lastNL < 0 {
		return nil, prevOffset
	}
	complete := raw[:lastNL]
	lines := strings.Split(complete, "\n")
	entries := make([]LogEntry, 0, len(lines))
	now := time.Now().UTC()
	for _, l := range lines {
		if l == "" {
			continue
		}
		entries = append(entries, LogEntry{
			NodeID:    nodeID,
			Source:    SourceFile,
			Path:      path,
			Timestamp: now,
			Priority:  "",
			Message:   l,
		})
	}
	return entries, prevOffset + int64(lastNL+1)
}
