package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/sshutil"
)

const maxResticBackupRecordBuffer = 4 << 20

type resticBackupSummary struct {
	NativePointID    string
	CaptureStartedAt time.Time
	CaptureEndedAt   time.Time
	FilesProcessed   uint64
	LogicalBytes     uint64
}

type resticBackupParser struct {
	progress func(ResticBackupProgress)
	now      func() time.Time

	code        backupasset.PublicationFailureCode
	summary     *resticBackupSummary
	summarySeen bool
}

func (adapter *ResticAdapter) Backup(ctx context.Context, attempt ResticAttemptV1, input ResticBackupInput, progress func(ResticBackupProgress)) (ResticBackupResult, error) {
	unknown := ResticBackupResult{ExitCode: UnknownProviderExitCode, Completion: backupasset.CompletionOutcomeUnknown}
	attempt, err := adapter.normalizePublicationAttempt(attempt)
	if err != nil {
		return unknown, err
	}
	if adapter.streamTransport == nil || adapter.publicationConfigSource == nil {
		return unknown, fmt.Errorf("%w: Restic publication transport unavailable", backupasset.ErrInvalidState)
	}
	if !validResticAbsoluteSource(input.Source) {
		return unknown, fmt.Errorf("%w: invalid Restic backup source", backupasset.ErrInvalidState)
	}
	for _, exclude := range input.Excludes {
		if !validResticExclude(exclude) {
			return unknown, fmt.Errorf("%w: invalid Restic backup exclusion", backupasset.ErrInvalidState)
		}
	}
	config, err := adapter.publicationConfigSource()
	if err != nil || config.BackupStreamMaxBytes <= 0 {
		if err != nil {
			return unknown, fmt.Errorf("resolve Restic publication configuration: %w", err)
		}
		return unknown, fmt.Errorf("%w: invalid Restic publication stream limit", backupasset.ErrInvalidState)
	}
	limits, err := adapter.operationLimits()
	if err != nil {
		return unknown, err
	}
	remaining := attempt.PointDeadlineAt.Sub(adapter.now().UTC()) - sshutil.CommandExecutionJoinTimeout
	if remaining <= 0 {
		return unknown, fmt.Errorf("%w: Restic publication deadline has no command margin", backupasset.ErrInvalidState)
	}
	limits.Timeout = remaining
	if err := limits.Validate(); err != nil {
		return unknown, err
	}

	arguments := []string{"--password-file", "/dev/stdin", "backup", "--json", "--tag", attempt.RequiredTags[0], "--tag", attempt.RequiredTags[1]}
	for _, exclude := range input.Excludes {
		arguments = append(arguments, "--exclude", exclude)
	}
	arguments = append(arguments, "--", input.Source)
	binding, err := publicationAccessBinding(attempt)
	if err != nil {
		return unknown, err
	}
	invocation := adapter.repositoryInvocation(binding, OperationResticBackup, arguments, CommandPurposePublish)
	if err := invocation.Validate(); err != nil {
		return unknown, err
	}
	execution, err := adapter.streamTransport.OpenExecution(ctx, invocation, limits, config.BackupStreamMaxBytes)
	if err != nil {
		return unknown, err
	}
	if execution == nil {
		return unknown, fmt.Errorf("%w: nil Restic publication execution", backupasset.ErrProviderUnavailable)
	}

	parser := &resticBackupParser{progress: progress, now: adapter.now}
	readErr, hardLimit := parser.read(execution, limits.MaxRecordBytes, config.BackupStreamMaxBytes)
	if hardLimit {
		_ = execution.Cancel()
	}
	completion, joinErr := execution.Join()
	if joinErr != nil {
		return unknown, joinErr
	}
	if readErr != nil || hardLimit || !completion.ExitCodeKnown {
		if hardLimit || errors.Is(readErr, sshutil.ErrCommandOutputLimit) {
			return unknown, sshutil.ErrCommandOutputLimit
		}
		return unknown, sshutil.ErrCommandFailed
	}
	if completion.ExitCode != 0 {
		if completion.ExitCode <= 0 {
			return unknown, sshutil.ErrCommandFailed
		}
		return ResticBackupResult{ExitCode: completion.ExitCode, Completion: backupasset.CompletionKnownNonzero}, fmt.Errorf("%w: Restic backup returned non-zero exit", sshutil.ErrCommandFailed)
	}
	if parser.code != "" {
		return ResticBackupResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, EvidenceCode: parser.code}, nil
	}
	if parser.summary == nil {
		return ResticBackupResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, EvidenceCode: backupasset.FailureEvidenceMissingSummary}, nil
	}
	return ResticBackupResult{
		ExitCode:   0,
		Completion: backupasset.CompletionKnownExitZero,
		ProviderCommit: &ResticCommitV1{
			Provider:           backupasset.ProviderRestic,
			RepositoryIdentity: attempt.RepositoryIdentity,
			NativePointID:      parser.summary.NativePointID,
			CaptureStartedAt:   parser.summary.CaptureStartedAt,
			CaptureFinishedAt:  parser.summary.CaptureEndedAt,
			FilesProcessed:     parser.summary.FilesProcessed,
			LogicalBytes:       parser.summary.LogicalBytes,
		},
	}, nil
}

func (adapter *ResticAdapter) LookupAttempt(ctx context.Context, attempt ResticAttemptV1) ([]ResticSnapshotObservation, error) {
	attempt, err := adapter.normalizePublicationAttempt(attempt)
	if err != nil {
		return nil, err
	}
	if adapter.streamTransport == nil {
		return nil, fmt.Errorf("%w: Restic publication transport unavailable", backupasset.ErrInvalidState)
	}
	limits, err := adapter.operationLimits()
	if err != nil {
		return nil, err
	}
	binding, err := publicationAccessBinding(attempt)
	if err != nil {
		return nil, err
	}
	invocation := adapter.repositoryInvocation(binding, OperationResticSnapshotsByTags, []string{
		"--password-file", "/dev/stdin", "snapshots", "--json", "--tag", attempt.RequiredTags[0] + "," + attempt.RequiredTags[1],
	}, CommandPurposeManifest)
	if err := invocation.Validate(); err != nil {
		return nil, err
	}
	execution, err := adapter.streamTransport.OpenExecution(ctx, invocation, limits, limits.MaxMetadataBytes)
	if err != nil {
		return nil, err
	}
	if execution == nil {
		return nil, fmt.Errorf("%w: nil Restic lookup execution", backupasset.ErrProviderUnavailable)
	}
	payload, readErr := readBoundedExecution(execution, limits.MaxMetadataBytes)
	if readErr != nil {
		_ = execution.Cancel()
	}
	completion, joinErr := execution.Join()
	if joinErr != nil {
		return nil, adapter.operationError(ctx, joinErr)
	}
	if readErr != nil || !completion.ExitCodeKnown || completion.ExitCode != 0 {
		if errors.Is(readErr, sshutil.ErrCommandOutputLimit) {
			return nil, newCapabilityError(backupasset.CapabilityProviderResourceLimit)
		}
		return nil, newCapabilityError(backupasset.CapabilityProviderUnavailable)
	}
	return parseResticSnapshotObservations(payload, attempt.RepositoryIdentity, limits)
}

func (adapter *ResticAdapter) normalizePublicationAttempt(attempt ResticAttemptV1) (ResticAttemptV1, error) {
	if adapter == nil || adapter.transport == nil || adapter.streamTransport == nil || attempt.Provider != backupasset.ProviderRestic ||
		backupasset.ValidateOpaqueID(attempt.RepositoryID) != nil || backupasset.ValidateOpaqueID(attempt.TaskRepositoryLinkID) != nil ||
		backupasset.ValidateOpaqueID(attempt.RecoveryPointID) != nil || attempt.TaskID == 0 || attempt.TaskRunID == 0 ||
		attempt.CapabilityRevision <= 0 || attempt.AdapterRevision != resticAdapterRevision || attempt.PointDeadlineAt.IsZero() {
		return ResticAttemptV1{}, fmt.Errorf("%w: invalid Restic publication attempt", backupasset.ErrInvalidState)
	}
	attempt.PointDeadlineAt = attempt.PointDeadlineAt.UTC()
	if !attempt.PointDeadlineAt.After(adapter.now().UTC()) || !strings.HasPrefix(attempt.RepositoryIdentity, NativeResticIdentityPrefix) ||
		!lowerHex(strings.TrimPrefix(attempt.RepositoryIdentity, NativeResticIdentityPrefix), 64) ||
		!validGeneratedResticTag(attempt.RequiredTags[0], 0) || !validGeneratedResticTag(attempt.RequiredTags[1], 1) {
		return ResticAttemptV1{}, fmt.Errorf("%w: invalid Restic publication attempt", backupasset.ErrInvalidState)
	}
	if err := backupasset.ValidatePublicationAuditContext(attempt.Audit); err != nil {
		return ResticAttemptV1{}, err
	}
	if err := validatePublicationFence(attempt.Fence, attempt.RecoveryPointID); err != nil {
		return ResticAttemptV1{}, err
	}
	if err := adapter.validateBinding(attempt.Access); err != nil || attempt.Access.RepositoryID != attempt.RepositoryID || attempt.Access.TaskID != attempt.TaskID {
		return ResticAttemptV1{}, fmt.Errorf("%w: invalid Restic publication access", backupasset.ErrInvalidState)
	}
	runtimeAccess, ok := attempt.Access.AdapterData.(ResticRuntimeAccess)
	if !ok || !lowerHex(runtimeAccess.NativeRepositoryID, 64) || NativeResticIdentityPrefix+runtimeAccess.NativeRepositoryID != attempt.RepositoryIdentity || runtimeAccess.Command == nil {
		return ResticAttemptV1{}, fmt.Errorf("%w: invalid Restic publication runtime", backupasset.ErrInvalidState)
	}
	return attempt, nil
}

func validatePublicationFence(fence backupasset.LeaseFence, pointID string) error {
	if backupasset.ValidateOpaqueID(fence.LeaseID) != nil || fence.RecoveryPointID != pointID || fence.HolderType != backupasset.LeaseHolderPointPublication ||
		strings.TrimSpace(fence.OwnerID) == "" || len(fence.OwnerID) > 128 || backupasset.ValidateOpaqueID(fence.AttemptID) != nil || !lowerHex(fence.FenceToken, 64) {
		return fmt.Errorf("%w: invalid Restic publication fence", backupasset.ErrInvalidState)
	}
	return nil
}

func publicationAccessBinding(attempt ResticAttemptV1) (AccessBinding, error) {
	binding := attempt.Access
	runtimeAccess, ok := binding.AdapterData.(ResticRuntimeAccess)
	if !ok || runtimeAccess.Command == nil {
		return AccessBinding{}, fmt.Errorf("%w: Restic publication runtime missing", backupasset.ErrInvalidState)
	}
	expected := sshutil.DialAuditContext{
		UserID: attempt.Audit.Actor.UserID, Username: attempt.Audit.Actor.Username, Role: attempt.Audit.Actor.Role,
		CorrelationID: attempt.Audit.CorrelationID, TaskID: uintPointer(attempt.TaskID), TaskRunID: uintPointer(attempt.TaskRunID),
	}
	if !emptyDialAuditContext(runtimeAccess.Command.Audit) && !equalDialAuditContext(runtimeAccess.Command.Audit, expected) {
		return AccessBinding{}, fmt.Errorf("%w: mismatched Restic publication audit context", backupasset.ErrInvalidState)
	}
	command := *runtimeAccess.Command
	command.Audit = expected
	runtimeAccess.Command = &command
	binding.AdapterData = runtimeAccess
	return binding, nil
}

func emptyDialAuditContext(value sshutil.DialAuditContext) bool {
	return value.Action == "" && value.CorrelationID == "" && value.UserID == 0 && value.Username == "" && value.Role == "" && value.TaskID == nil && value.TaskRunID == nil
}

func equalDialAuditContext(left, right sshutil.DialAuditContext) bool {
	return left.Action == right.Action && left.CorrelationID == right.CorrelationID && left.UserID == right.UserID && left.Username == right.Username && left.Role == right.Role &&
		equalUintPointer(left.TaskID, right.TaskID) && equalUintPointer(left.TaskRunID, right.TaskRunID)
}

func equalUintPointer(left, right *uint) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func uintPointer(value uint) *uint { return &value }

func (parser *resticBackupParser) read(execution CommandExecution, maxRecordBytes int, maxTotalBytes int64) (error, bool) {
	if maxRecordBytes <= 0 || maxRecordBytes > maxResticBackupRecordBuffer {
		return sshutil.ErrCommandFailed, false
	}
	reader := bufio.NewReaderSize(execution, maxRecordBytes+1)
	var total int64
	discardingRecord := false
	for {
		fragment, err := reader.ReadSlice('\n')
		total += int64(len(fragment))
		if total > maxTotalBytes {
			return sshutil.ErrCommandOutputLimit, true
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			parser.remember(backupasset.FailureEvidenceMalformedStream)
			discardingRecord = true
			continue
		case errors.Is(err, io.EOF):
			if len(fragment) > 0 || discardingRecord {
				parser.remember(backupasset.FailureEvidenceMalformedStream)
			}
			return nil, false
		case err != nil:
			return err, false
		}
		if discardingRecord {
			discardingRecord = false
			continue
		}
		if len(fragment) == 0 {
			continue
		}
		parser.record(fragment[:len(fragment)-1])
	}
}

func (parser *resticBackupParser) record(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(line, &object); err != nil {
		parser.remember(backupasset.FailureEvidenceMalformedStream)
		return
	}
	messageType, err := requiredJSONString(object, "message_type")
	if err != nil || messageType == "" {
		parser.remember(backupasset.FailureEvidenceMalformedStream)
		return
	}
	if parser.summarySeen {
		if messageType == "summary" {
			parser.remember(backupasset.FailureEvidenceDuplicateSummary)
		} else {
			parser.remember(backupasset.FailureEvidenceNonFinalSummary)
		}
		return
	}
	switch messageType {
	case "status":
		if progress, err := parseResticStatus(object, parser.now); err != nil {
			parser.remember(backupasset.FailureEvidenceMalformedStream)
		} else if parser.progress != nil {
			parser.progress(progress)
		}
	case "verbose_status":
		if err := validateResticVerboseStatus(object); err != nil {
			parser.remember(backupasset.FailureEvidenceMalformedStream)
		}
	case "summary":
		parser.summarySeen = true
		summary, code := parseResticBackupSummary(object)
		if code != "" {
			parser.remember(code)
			return
		}
		parser.summary = &summary
	case "error":
		// Error payload fields can contain paths and messages. They are not
		// publication evidence and are intentionally discarded.
	default:
		// Restic's JSON stream is forward-compatible. Unknown pre-summary
		// records are ignored after structural parsing of message_type.
	}
}

func (parser *resticBackupParser) remember(code backupasset.PublicationFailureCode) {
	if parser.code == "" {
		parser.code = code
	}
}

func parseResticStatus(object map[string]json.RawMessage, now func() time.Time) (ResticBackupProgress, error) {
	percent, err := requiredJSONFloat(object, "percent_done")
	if err != nil || percent < 0 || percent > 1 {
		return ResticBackupProgress{}, errors.New("invalid Restic status percent")
	}
	filesDone, err := requiredJSONUint(object, "files_done")
	if err != nil {
		return ResticBackupProgress{}, errors.New("invalid Restic status files")
	}
	totalFiles, err := requiredJSONUint(object, "total_files")
	if err != nil || filesDone > totalFiles {
		return ResticBackupProgress{}, errors.New("invalid Restic status total files")
	}
	bytesDone, err := requiredJSONUint(object, "bytes_done")
	if err != nil {
		return ResticBackupProgress{}, errors.New("invalid Restic status bytes")
	}
	totalBytes, err := requiredJSONUint(object, "total_bytes")
	if err != nil || bytesDone > totalBytes {
		return ResticBackupProgress{}, errors.New("invalid Restic status total bytes")
	}
	throughput := 0.0
	if elapsed, exists, elapsedErr := optionalJSONFloat(object, "seconds_elapsed"); elapsedErr != nil || (exists && elapsed < 0) {
		return ResticBackupProgress{}, errors.New("invalid Restic status duration")
	} else if exists && elapsed > 0 {
		throughput = float64(bytesDone) / elapsed / (1024 * 1024)
	}
	if now == nil {
		now = time.Now
	}
	return ResticBackupProgress{ObservedAt: now().UTC(), Percent: int(percent * 100), ThroughputMbps: throughput, FilesDone: filesDone}, nil
}

func validateResticVerboseStatus(object map[string]json.RawMessage) error {
	action, err := requiredJSONString(object, "action")
	if err != nil {
		return err
	}
	switch action {
	case "scan_started", "scan_finished", "backup_started", "backup_finished":
	default:
		return errors.New("unknown Restic verbose status action")
	}
	for _, key := range []string{"files_done", "total_files", "bytes_done", "total_bytes"} {
		if _, exists, err := optionalJSONUint(object, key); err != nil || (exists && err != nil) {
			return errors.New("invalid Restic verbose status counter")
		}
	}
	return nil
}

func parseResticBackupSummary(object map[string]json.RawMessage) (resticBackupSummary, backupasset.PublicationFailureCode) {
	dryRun, err := requiredJSONBool(object, "dry_run")
	if err != nil || dryRun {
		return resticBackupSummary{}, backupasset.FailureEvidenceInvalidNativeID
	}
	nativePointID, err := requiredJSONString(object, "snapshot_id")
	if err != nil || !lowerHex(nativePointID, 64) {
		return resticBackupSummary{}, backupasset.FailureEvidenceInvalidNativeID
	}
	startedRaw, err := requiredJSONString(object, "backup_start")
	if err != nil {
		return resticBackupSummary{}, backupasset.FailureEvidenceMalformedStream
	}
	finishedRaw, err := requiredJSONString(object, "backup_end")
	if err != nil {
		return resticBackupSummary{}, backupasset.FailureEvidenceMalformedStream
	}
	startedAt, err := time.Parse(time.RFC3339Nano, startedRaw)
	if err != nil || startedAt.IsZero() {
		return resticBackupSummary{}, backupasset.FailureEvidenceMalformedStream
	}
	finishedAt, err := time.Parse(time.RFC3339Nano, finishedRaw)
	if err != nil || finishedAt.IsZero() || finishedAt.Before(startedAt) {
		return resticBackupSummary{}, backupasset.FailureEvidenceMalformedStream
	}
	filesProcessed, err := requiredJSONUint(object, "total_files_processed")
	if err != nil {
		return resticBackupSummary{}, backupasset.FailureEvidenceMalformedStream
	}
	logicalBytes, err := requiredJSONUint(object, "total_bytes_processed")
	if err != nil {
		return resticBackupSummary{}, backupasset.FailureEvidenceMalformedStream
	}
	return resticBackupSummary{NativePointID: nativePointID, CaptureStartedAt: startedAt.UTC(), CaptureEndedAt: finishedAt.UTC(), FilesProcessed: filesProcessed, LogicalBytes: logicalBytes}, ""
}

func parseResticSnapshotObservations(payload []byte, repositoryIdentity string, limits OperationLimits) ([]ResticSnapshotObservation, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return nil, protocolCapabilityError()
	}
	observations := make([]ResticSnapshotObservation, 0)
	for decoder.More() {
		if len(observations) >= limits.MaxItems {
			return nil, newCapabilityError(backupasset.CapabilityProviderResourceLimit)
		}
		var object map[string]json.RawMessage
		if err := decoder.Decode(&object); err != nil {
			return nil, protocolCapabilityError()
		}
		nativePointID, err := requiredJSONString(object, "id")
		if err != nil || !lowerHex(nativePointID, 64) {
			return nil, protocolCapabilityError()
		}
		timeRaw, err := requiredJSONString(object, "time")
		if err != nil {
			return nil, protocolCapabilityError()
		}
		snapshotTime, err := time.Parse(time.RFC3339Nano, timeRaw)
		if err != nil || snapshotTime.IsZero() {
			return nil, protocolCapabilityError()
		}
		var tags []string
		if rawTags, ok := object["tags"]; !ok || json.Unmarshal(rawTags, &tags) != nil {
			return nil, protocolCapabilityError()
		}
		for _, tag := range tags {
			if strings.ContainsRune(tag, '\x00') || len(tag) > 4096 {
				return nil, protocolCapabilityError()
			}
		}
		observation := ResticSnapshotObservation{
			RepositoryIdentity: repositoryIdentity,
			NativePointID:      nativePointID,
			SnapshotTime:       snapshotTime.UTC(),
			Tags:               append([]string(nil), tags...),
		}
		if original, exists := object["original"]; exists {
			observation.OriginalPresent = true
			if string(original) != "null" {
				value, err := decodeJSONString(original)
				if err != nil {
					return nil, protocolCapabilityError()
				}
				observation.Original = &value
			}
		}
		if rawSummary, exists := object["summary"]; exists {
			observation.Summary = parseResticStoredSummary(rawSummary)
		}
		observations = append(observations, observation)
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim(']') {
		return nil, protocolCapabilityError()
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, protocolCapabilityError()
	}
	return observations, nil
}

func parseResticStoredSummary(raw json.RawMessage) *ResticStoredSummary {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil
	}
	startedRaw, err := requiredJSONString(object, "backup_start")
	if err != nil {
		return nil
	}
	finishedRaw, err := requiredJSONString(object, "backup_end")
	if err != nil {
		return nil
	}
	startedAt, err := time.Parse(time.RFC3339Nano, startedRaw)
	if err != nil || startedAt.IsZero() {
		return nil
	}
	finishedAt, err := time.Parse(time.RFC3339Nano, finishedRaw)
	if err != nil || finishedAt.IsZero() || finishedAt.Before(startedAt) {
		return nil
	}
	filesProcessed, err := requiredJSONUint(object, "total_files_processed")
	if err != nil {
		return nil
	}
	logicalBytes, err := requiredJSONUint(object, "total_bytes_processed")
	if err != nil {
		return nil
	}
	return &ResticStoredSummary{BackupStartedAt: startedAt.UTC(), BackupFinishedAt: finishedAt.UTC(), FilesProcessed: filesProcessed, LogicalBytes: logicalBytes}
}

func readBoundedExecution(execution CommandExecution, maximum int64) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(execution, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > maximum {
		return nil, sshutil.ErrCommandOutputLimit
	}
	return payload, nil
}

func requiredJSONString(object map[string]json.RawMessage, key string) (string, error) {
	raw, exists := object[key]
	if !exists {
		return "", errors.New("missing JSON string")
	}
	return decodeJSONString(raw)
}

func decodeJSONString(raw json.RawMessage) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func requiredJSONBool(object map[string]json.RawMessage, key string) (bool, error) {
	raw, exists := object[key]
	if !exists {
		return false, errors.New("missing JSON bool")
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, err
	}
	return value, nil
}

func requiredJSONUint(object map[string]json.RawMessage, key string) (uint64, error) {
	value, exists, err := optionalJSONUint(object, key)
	if err != nil || !exists {
		return 0, errors.New("missing JSON uint")
	}
	return value, nil
}

func optionalJSONUint(object map[string]json.RawMessage, key string) (uint64, bool, error) {
	raw, exists := object[key]
	if !exists {
		return 0, false, nil
	}
	number, err := decodeJSONNumber(raw)
	if err != nil {
		return 0, true, err
	}
	value, err := strconv.ParseUint(number, 10, 64)
	if err != nil {
		return 0, true, err
	}
	return value, true, nil
}

func requiredJSONFloat(object map[string]json.RawMessage, key string) (float64, error) {
	value, exists, err := optionalJSONFloat(object, key)
	if err != nil || !exists {
		return 0, errors.New("missing JSON number")
	}
	return value, nil
}

func optionalJSONFloat(object map[string]json.RawMessage, key string) (float64, bool, error) {
	raw, exists := object[key]
	if !exists {
		return 0, false, nil
	}
	number, err := decodeJSONNumber(raw)
	if err != nil {
		return 0, true, err
	}
	value, err := strconv.ParseFloat(number, 64)
	if err != nil || value != value {
		return 0, true, errors.New("invalid JSON float")
	}
	return value, true, nil
}

func decodeJSONNumber(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value interface{}
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", errors.New("trailing JSON number")
	}
	number, ok := value.(json.Number)
	if !ok {
		return "", errors.New("JSON value is not a number")
	}
	return number.String(), nil
}

// resticPublicationStrategy is the tagged bridge from the shared publication
// coordinator to the existing Restic command and manifest ports. It owns the
// only conversion between a Restic V1 payload and the legacy adapter types;
// callers outside this package cannot pass an untagged attempt or a free-form
// provider payload through the boundary.
type resticPublicationStrategy struct {
	publisher ResticPublisher
	manifest  ManifestBuilder
}

func NewResticPublicationStrategy(publisher ResticPublisher, manifest ManifestBuilder) (PublicationStrategy, error) {
	if interfaceNil(publisher) || interfaceNil(manifest) {
		return nil, fmt.Errorf("%w: Restic publication strategy dependencies are unavailable", backupasset.ErrInvalidState)
	}
	return &resticPublicationStrategy{publisher: publisher, manifest: manifest}, nil
}

func (*resticPublicationStrategy) Kind() backupasset.ProviderKind { return backupasset.ProviderRestic }

func (strategy *resticPublicationStrategy) Prepare(_ context.Context, request PublicationPrepareRequest) (PreparedPublication, error) {
	if strategy == nil || interfaceNil(strategy.publisher) || interfaceNil(strategy.manifest) || request.ResticInput == nil || request.RsyncTreeInput != nil {
		return PreparedPublication{}, fmt.Errorf("%w: Restic publication prepare request is unavailable", backupasset.ErrInvalidState)
	}
	if _, err := request.Attempt.ResticAttempt(); err != nil {
		return PreparedPublication{}, err
	}
	input := *request.ResticInput
	input.Excludes = append([]string(nil), input.Excludes...)
	return PreparedPublication{Attempt: request.Attempt, ResticInput: &input}, nil
}

func (strategy *resticPublicationStrategy) Execute(ctx context.Context, prepared PreparedPublication, progress PublicationProgress) (ProviderExecutionResult, error) {
	attempt, input, err := strategy.resticExecutionPrepared(prepared)
	if err != nil {
		return ProviderExecutionResult{}, err
	}
	result, err := strategy.publisher.Backup(ctx, attempt, input, progress.OnResticProgress)
	output := ProviderExecutionResult{ExitCode: result.ExitCode, Completion: result.Completion, EvidenceCode: result.EvidenceCode}
	if result.ProviderCommit != nil {
		commit := NewResticProviderCommit(*result.ProviderCommit)
		if validateErr := commit.Validate(); validateErr != nil {
			return ProviderExecutionResult{}, validateErr
		}
		output.ProviderCommit = &commit
	}
	return output, err
}

func (strategy *resticPublicationStrategy) RecordCommit(_ context.Context, prepared PreparedPublication, result ProviderExecutionResult) (ProviderCommit, error) {
	attempt, err := strategy.resticPreparedAttempt(prepared)
	if err != nil {
		return ProviderCommit{}, err
	}
	if result.Completion != backupasset.CompletionKnownExitZero || result.ExitCode != 0 || result.EvidenceCode != "" || result.ProviderCommit == nil {
		return ProviderCommit{}, fmt.Errorf("%w: Restic execution has no commit fact", backupasset.ErrInvalidState)
	}
	commit, err := result.ProviderCommit.ResticCommit()
	if err != nil {
		return ProviderCommit{}, err
	}
	if commit.RepositoryIdentity != attempt.RepositoryIdentity || !commit.CaptureStartedAt.Before(attempt.PointDeadlineAt) || !commit.CaptureFinishedAt.Before(attempt.PointDeadlineAt) {
		return ProviderCommit{}, fmt.Errorf("%w: Restic strategy commit does not match its attempt", backupasset.ErrConflict)
	}
	return *result.ProviderCommit, nil
}

func (strategy *resticPublicationStrategy) VerifyOrBuildManifest(ctx context.Context, prepared PreparedPublication, commit ProviderCommit, limits ManifestLimits) (ManifestResult, error) {
	attempt, err := strategy.resticPreparedAttempt(prepared)
	if err != nil {
		return ManifestResult{}, err
	}
	resticCommit, err := commit.ResticCommit()
	if err != nil {
		return ManifestResult{}, err
	}
	if resticCommit.RepositoryIdentity != attempt.RepositoryIdentity {
		return ManifestResult{}, fmt.Errorf("%w: Restic manifest commit identity mismatch", backupasset.ErrConflict)
	}
	manifest, err := strategy.manifest.BuildManifest(ctx, attempt, resticCommit, limits)
	if err != nil {
		return ManifestResult{}, err
	}
	return ManifestResult{Provider: backupasset.ProviderRestic, Version: taggedPublicationSchemaV1, Restic: &manifest}, nil
}

func (strategy *resticPublicationStrategy) Reconcile(ctx context.Context, request PublicationReconcileRequest) (PublicationReconcileResult, error) {
	if strategy == nil || interfaceNil(strategy.publisher) {
		return PublicationReconcileResult{}, fmt.Errorf("%w: Restic publication reconciler is unavailable", backupasset.ErrInvalidState)
	}
	attempt, err := request.Attempt.ResticAttempt()
	if err != nil {
		return PublicationReconcileResult{}, err
	}
	observations, err := strategy.publisher.LookupAttempt(ctx, attempt)
	if err != nil {
		return PublicationReconcileResult{}, err
	}
	return PublicationReconcileResult{ResticObservations: observations}, nil
}

func (strategy *resticPublicationStrategy) resticPreparedAttempt(prepared PreparedPublication) (ResticAttemptV1, error) {
	if strategy == nil || interfaceNil(strategy.publisher) || interfaceNil(strategy.manifest) {
		return ResticAttemptV1{}, fmt.Errorf("%w: Restic prepared publication is unavailable", backupasset.ErrInvalidState)
	}
	attempt, err := prepared.Attempt.ResticAttempt()
	if err != nil {
		return ResticAttemptV1{}, err
	}
	return attempt, nil
}

func (strategy *resticPublicationStrategy) resticExecutionPrepared(prepared PreparedPublication) (ResticAttemptV1, ResticBackupInput, error) {
	attempt, err := strategy.resticPreparedAttempt(prepared)
	if err != nil {
		return ResticAttemptV1{}, ResticBackupInput{}, err
	}
	if prepared.ResticInput == nil {
		return ResticAttemptV1{}, ResticBackupInput{}, fmt.Errorf("%w: Restic execution input is unavailable", backupasset.ErrInvalidState)
	}
	input := *prepared.ResticInput
	input.Excludes = append([]string(nil), input.Excludes...)
	return attempt, input, nil
}

var _ ResticPublisher = (*ResticAdapter)(nil)
var _ PublicationStrategy = (*resticPublicationStrategy)(nil)
