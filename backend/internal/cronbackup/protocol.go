package cronbackup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

type stateDocument struct {
	Version       int           `json:"version"`
	SourceID      string        `json:"source_id"`
	Revision      int64         `json:"revision"`
	Engine        string        `json:"engine"`
	InitializedAt time.Time     `json:"initialized_at"`
	LatestAttempt *stateAttempt `json:"latest_attempt,omitempty"`
	LastSuccess   *stateSuccess `json:"last_success,omitempty"`
}

type stateAttempt struct {
	RunID        string        `json:"run_id"`
	StartedAt    time.Time     `json:"started_at"`
	Result       AttemptResult `json:"result"`
	FinishedAt   *time.Time    `json:"finished_at,omitempty"`
	DetectedAt   *time.Time    `json:"detected_at,omitempty"`
	FailureCode  string        `json:"failure_code,omitempty"`
	ArtifactName string        `json:"artifact_name,omitempty"`
}

type stateSuccess struct {
	RunID        string    `json:"run_id"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	ArtifactName string    `json:"artifact_name"`
}

var allowedStateFields = map[string]bool{
	"version": true, "source_id": true, "revision": true, "engine": true,
	"initialized_at": true, "latest_attempt": true, "last_success": true,
}

var allowedAttemptFields = map[string]bool{
	"run_id": true, "started_at": true, "result": true, "finished_at": true,
	"detected_at": true, "failure_code": true, "artifact_name": true,
}

var allowedSuccessFields = map[string]bool{
	"run_id": true, "started_at": true, "finished_at": true, "artifact_name": true,
}

func parseState(data []byte, cfg Config) (stateDocument, error) {
	if !utf8.Valid(data) {
		return stateDocument{}, fmt.Errorf("state is not valid UTF-8")
	}
	fields, err := strictObject(data)
	if err != nil {
		return stateDocument{}, err
	}
	for name := range fields {
		if !allowedStateFields[name] {
			return stateDocument{}, fmt.Errorf("unknown state field %q", name)
		}
	}

	version, err := requiredInt(fields, "version")
	if err != nil {
		return stateDocument{}, err
	}
	sourceID, err := requiredString(fields, "source_id")
	if err != nil {
		return stateDocument{}, err
	}
	revision, err := requiredInt64(fields, "revision")
	if err != nil {
		return stateDocument{}, err
	}
	engine, err := requiredString(fields, "engine")
	if err != nil {
		return stateDocument{}, err
	}
	initializedAt, err := requiredTime(fields, "initialized_at")
	if err != nil {
		return stateDocument{}, err
	}

	state := stateDocument{
		Version:       version,
		SourceID:      sourceID,
		Revision:      revision,
		Engine:        engine,
		InitializedAt: initializedAt,
	}
	if raw, ok := fields["latest_attempt"]; ok {
		state.LatestAttempt, err = parseAttempt(raw, cfg.Engine)
		if err != nil {
			return stateDocument{}, err
		}
	}
	if raw, ok := fields["last_success"]; ok {
		state.LastSuccess, err = parseSuccess(raw, cfg.Engine)
		if err != nil {
			return stateDocument{}, err
		}
	}
	if err := validateStateForRead(state, cfg); err != nil {
		return stateDocument{}, err
	}
	return state, nil
}

func parseAttempt(raw []byte, engine string) (*stateAttempt, error) {
	fields, err := strictObject(raw)
	if err != nil {
		return nil, err
	}
	for name := range fields {
		if !allowedAttemptFields[name] {
			return nil, fmt.Errorf("unknown attempt field %q", name)
		}
	}
	runID, err := requiredString(fields, "run_id")
	if err != nil {
		return nil, err
	}
	startedAt, err := requiredTime(fields, "started_at")
	if err != nil {
		return nil, err
	}
	resultText, err := requiredString(fields, "result")
	if err != nil {
		return nil, err
	}
	attempt := &stateAttempt{RunID: runID, StartedAt: startedAt, Result: AttemptResult(resultText)}
	switch attempt.Result {
	case AttemptRunning:
		if hasAny(fields, "finished_at", "detected_at", "failure_code", "artifact_name") {
			return nil, fmt.Errorf("running attempt has completion fields")
		}
	case AttemptSuccess:
		attempt.FinishedAt, err = requiredTimePtr(fields, "finished_at")
		if err != nil {
			return nil, err
		}
		attempt.ArtifactName, err = requiredString(fields, "artifact_name")
		if err != nil || !validArtifactName(attempt.ArtifactName, engine) {
			return nil, fmt.Errorf("invalid successful artifact")
		}
		if hasAny(fields, "detected_at", "failure_code") {
			return nil, fmt.Errorf("success attempt has failure fields")
		}
	case AttemptFailed:
		attempt.FinishedAt, err = requiredTimePtr(fields, "finished_at")
		if err != nil {
			return nil, err
		}
		attempt.FailureCode, err = requiredString(fields, "failure_code")
		if err != nil || !validFailureCode(attempt.FailureCode, false) {
			return nil, fmt.Errorf("invalid failed attempt code")
		}
		if hasAny(fields, "detected_at", "artifact_name") {
			return nil, fmt.Errorf("failed attempt has invalid fields")
		}
	case AttemptInterrupted:
		attempt.DetectedAt, err = requiredTimePtr(fields, "detected_at")
		if err != nil {
			return nil, err
		}
		attempt.FailureCode, err = requiredString(fields, "failure_code")
		if err != nil || attempt.FailureCode != FailureProcessInterrupted {
			return nil, fmt.Errorf("invalid interrupted attempt code")
		}
		if hasAny(fields, "finished_at", "artifact_name") {
			return nil, fmt.Errorf("interrupted attempt has completion fields")
		}
	default:
		return nil, fmt.Errorf("unsupported attempt result")
	}
	return attempt, nil
}

func parseSuccess(raw []byte, engine string) (*stateSuccess, error) {
	fields, err := strictObject(raw)
	if err != nil {
		return nil, err
	}
	for name := range fields {
		if !allowedSuccessFields[name] {
			return nil, fmt.Errorf("unknown success field %q", name)
		}
	}
	runID, err := requiredString(fields, "run_id")
	if err != nil {
		return nil, err
	}
	startedAt, err := requiredTime(fields, "started_at")
	if err != nil {
		return nil, err
	}
	finishedAt, err := requiredTime(fields, "finished_at")
	if err != nil {
		return nil, err
	}
	artifactName, err := requiredString(fields, "artifact_name")
	if err != nil || !validArtifactName(artifactName, engine) {
		return nil, fmt.Errorf("invalid last success artifact")
	}
	return &stateSuccess{RunID: runID, StartedAt: startedAt, FinishedAt: finishedAt, ArtifactName: artifactName}, nil
}

func validateState(state stateDocument, cfg Config) error {
	return validateStateWithOrdering(state, cfg, true)
}

func validateStateForRead(state stateDocument, cfg Config) error {
	return validateStateWithOrdering(state, cfg, false)
}

func validateStateWithOrdering(state stateDocument, cfg Config, enforceOrdering bool) error {
	if state.Version != 1 || state.Revision <= 0 || !validHexID(state.SourceID) || state.Engine != cfg.Engine || !isUTC(state.InitializedAt) {
		return fmt.Errorf("invalid state identity")
	}
	if state.LatestAttempt == nil {
		if state.LastSuccess != nil {
			return fmt.Errorf("last success without attempt")
		}
		return nil
	}
	attempt := state.LatestAttempt
	if !validHexID(attempt.RunID) || !isUTC(attempt.StartedAt) || (enforceOrdering && attempt.StartedAt.Before(state.InitializedAt)) {
		return fmt.Errorf("invalid attempt times or identity")
	}
	switch attempt.Result {
	case AttemptRunning:
		if attempt.FinishedAt != nil || attempt.DetectedAt != nil || attempt.FailureCode != "" || attempt.ArtifactName != "" {
			return fmt.Errorf("invalid running attempt")
		}
	case AttemptSuccess:
		if attempt.FinishedAt == nil || !isUTC(*attempt.FinishedAt) || (enforceOrdering && attempt.FinishedAt.Before(attempt.StartedAt)) || attempt.DetectedAt != nil || attempt.FailureCode != "" || !validArtifactName(attempt.ArtifactName, cfg.Engine) {
			return fmt.Errorf("invalid successful attempt")
		}
	case AttemptFailed:
		if attempt.FinishedAt == nil || !isUTC(*attempt.FinishedAt) || (enforceOrdering && attempt.FinishedAt.Before(attempt.StartedAt)) || attempt.DetectedAt != nil || attempt.ArtifactName != "" || !validFailureCode(attempt.FailureCode, false) {
			return fmt.Errorf("invalid failed attempt")
		}
	case AttemptInterrupted:
		if attempt.DetectedAt == nil || !isUTC(*attempt.DetectedAt) || (enforceOrdering && attempt.DetectedAt.Before(attempt.StartedAt)) || attempt.FinishedAt != nil || attempt.ArtifactName != "" || attempt.FailureCode != FailureProcessInterrupted {
			return fmt.Errorf("invalid interrupted attempt")
		}
	default:
		return fmt.Errorf("invalid attempt result")
	}
	if state.LastSuccess != nil {
		last := state.LastSuccess
		if !validHexID(last.RunID) || !isUTC(last.StartedAt) || !isUTC(last.FinishedAt) || (enforceOrdering && last.FinishedAt.Before(last.StartedAt)) || (enforceOrdering && last.StartedAt.Before(state.InitializedAt)) || !validArtifactName(last.ArtifactName, cfg.Engine) {
			return fmt.Errorf("invalid last success")
		}
		if attempt.Result == AttemptSuccess && (last.RunID != attempt.RunID || !last.StartedAt.Equal(attempt.StartedAt) || !last.FinishedAt.Equal(*attempt.FinishedAt) || last.ArtifactName != attempt.ArtifactName) {
			return fmt.Errorf("latest success differs from last success")
		}
		if attempt.Result != AttemptSuccess && last.RunID == attempt.RunID {
			return fmt.Errorf("last success reuses current non-success run")
		}
	} else if attempt.Result == AttemptSuccess {
		return fmt.Errorf("successful attempt without last success")
	}
	return nil
}

func strictObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := first.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("object required")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok || name == "" {
			return nil, fmt.Errorf("object key required")
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, fmt.Errorf("duplicate object key")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		fields[name] = raw
	}
	last, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := last.(json.Delim); !ok || delimiter != '}' {
		return nil, fmt.Errorf("object close required")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing JSON value")
		}
		return nil, err
	}
	return fields, nil
}

func requiredInt(fields map[string]json.RawMessage, name string) (int, error) {
	raw, ok := fields[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, fmt.Errorf("missing %s", name)
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, err
	}
	return value, nil
}

func requiredInt64(fields map[string]json.RawMessage, name string) (int64, error) {
	raw, ok := fields[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, fmt.Errorf("missing %s", name)
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, err
	}
	return value, nil
}

func requiredString(fields map[string]json.RawMessage, name string) (string, error) {
	raw, ok := fields[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fmt.Errorf("missing %s", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func requiredTime(fields map[string]json.RawMessage, name string) (time.Time, error) {
	raw, ok := fields[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return time.Time{}, fmt.Errorf("missing %s", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return time.Time{}, err
	}
	return parseProtocolTime(value)
}

func requiredTimePtr(fields map[string]json.RawMessage, name string) (*time.Time, error) {
	value, err := requiredTime(fields, name)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func hasAny(fields map[string]json.RawMessage, names ...string) bool {
	for _, name := range names {
		if _, ok := fields[name]; ok {
			return true
		}
	}
	return false
}

func parseProtocolTime(value string) (time.Time, error) {
	if value == "" || !strings.HasSuffix(value, "Z") {
		return time.Time{}, fmt.Errorf("timestamp must be UTC")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || !isUTC(parsed) {
		return time.Time{}, fmt.Errorf("invalid timestamp")
	}
	return parsed, nil
}

func isUTC(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC
}

func validHexID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validFailureCode(value string, start bool) bool {
	if start {
		return value == FailureBackupStartFailed
	}
	return value == FailureBackupFailed || value == FailureBackupStartFailed || value == FailureResultInvalid
}

func validArtifactName(name, engine string) bool {
	prefix, suffix := "xirang-"+engine+"-", ".db"
	if engine == "postgres" {
		suffix = ".dump"
	}
	if !validEngine(engine) || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if len(stamp) != len("20060102-150405") || stamp[8] != '-' {
		return false
	}
	for index, char := range stamp {
		if index == 8 {
			continue
		}
		if char < '0' || char > '9' {
			return false
		}
	}
	parsed, err := time.ParseInLocation("20060102-150405", stamp, time.UTC)
	return err == nil && parsed.Location() == time.UTC
}

func stateToJob(state stateDocument, status JobStatus, now time.Time, maxAge time.Duration) JobObservation {
	return JobObservation{
		Evidence:      "job_record",
		Status:        status,
		CheckedAt:     now.UTC(),
		MaxAgeSeconds: int64(maxAge / time.Second),
		LatestAttempt: safeAttempt(state.LatestAttempt),
		LastSuccess:   safeSuccess(state.LastSuccess),
	}
}

func safeAttempt(attempt *stateAttempt) *AttemptObservation {
	if attempt == nil {
		return nil
	}
	result := &AttemptObservation{
		RunID: attempt.RunID, StartedAt: attempt.StartedAt, Result: attempt.Result,
		FailureCode: attempt.FailureCode, ArtifactName: attempt.ArtifactName,
	}
	if attempt.FinishedAt != nil {
		finished := *attempt.FinishedAt
		result.FinishedAt = &finished
	}
	if attempt.DetectedAt != nil {
		detected := *attempt.DetectedAt
		result.DetectedAt = &detected
	}
	return result
}

func safeSuccess(success *stateSuccess) *SuccessObservation {
	if success == nil {
		return nil
	}
	return &SuccessObservation{RunID: success.RunID, StartedAt: success.StartedAt, FinishedAt: success.FinishedAt, ArtifactName: success.ArtifactName}
}

func cloneState(state stateDocument) stateDocument {
	copy := state
	if state.LatestAttempt != nil {
		attempt := *state.LatestAttempt
		if state.LatestAttempt.FinishedAt != nil {
			finished := *state.LatestAttempt.FinishedAt
			attempt.FinishedAt = &finished
		}
		if state.LatestAttempt.DetectedAt != nil {
			detected := *state.LatestAttempt.DetectedAt
			attempt.DetectedAt = &detected
		}
		copy.LatestAttempt = &attempt
	}
	if state.LastSuccess != nil {
		success := *state.LastSuccess
		copy.LastSuccess = &success
	}
	return copy
}

func encodeState(state stateDocument) ([]byte, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	if len(data) > maxStateBytes {
		return nil, fmt.Errorf("state exceeds size limit")
	}
	return data, nil
}
