package task

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

const (
	taskRunCodeInterruptedAfterProviderCommit  = "process_interrupted_after_provider_commit"
	taskRunCodeInterruptedBeforeProviderCommit = "process_interrupted_before_provider_commit"
)

// ReportInterruptedPublication reconciles only a stale TaskRun after the
// worker discovers a durable terminal/verifying publication fact. A current
// process entry is authoritative: post-hooks and normal run finalization win
// over a fast worker callback.
func (manager *Manager) ReportInterruptedPublication(ctx context.Context, outcome publication.Outcome) error {
	if manager == nil || manager.db == nil {
		return fmt.Errorf("%w: interrupted publication reporter unavailable", backupasset.ErrInvalidState)
	}
	if _, live := manager.pendingRuns.Load(outcome.TaskID); live {
		return nil
	}
	status, code, reportable := interruptedPublicationTaskRunState(outcome)
	if !reportable {
		return nil
	}
	return manager.reportInterruptedPublication(ctx, outcome.TaskID, outcome.TaskRunID, status, code)
}

func interruptedPublicationTaskRunState(outcome publication.Outcome) (string, string, bool) {
	if outcome.TaskID == 0 || outcome.TaskRunID == 0 {
		return "", "", false
	}
	if outcome.ProviderCommitRecorded {
		switch outcome.State {
		case backupasset.RecoveryPointVerifying, backupasset.RecoveryPointCommitted, backupasset.RecoveryPointFailed:
			return "warning", taskRunCodeInterruptedAfterProviderCommit, true
		}
	}
	if !outcome.ProviderCommitRecorded && outcome.State == backupasset.RecoveryPointFailed {
		return "failed", taskRunCodeInterruptedBeforeProviderCommit, true
	}
	return "", "", false
}

func (manager *Manager) reportInterruptedPublication(ctx context.Context, taskID, taskRunID uint, targetStatus, code string) error {
	if taskID == 0 || taskRunID == 0 || (targetStatus != "warning" && targetStatus != "failed") || code == "" {
		return fmt.Errorf("%w: invalid interrupted publication report", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	return manager.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run model.TaskRun
		if err := tx.Where("id = ? AND task_id = ? AND status IN ?", taskRunID, taskID, []string{"pending", "running", "retrying"}).First(&run).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return fmt.Errorf("load interrupted TaskRun: %w", err)
		}
		updated := tx.Model(&model.TaskRun{}).
			Where("id = ? AND task_id = ? AND status IN ?", taskRunID, taskID, []string{"pending", "running", "retrying"}).
			Updates(map[string]any{"status": targetStatus, "finished_at": &now, "last_error": code})
		if updated.Error != nil {
			return fmt.Errorf("mark interrupted TaskRun: %w", updated.Error)
		}
		if updated.RowsAffected != 1 || run.StartedAt == nil {
			return nil
		}
		// The aggregate update is intentionally a precise CAS: a subsequent run
		// must never have its Task status overwritten by this stale reporter.
		result := tx.Model(&model.Task{}).
			Where("id = ? AND status = ? AND last_run_at = ?", taskID, "running", run.StartedAt.UTC()).
			Where("NOT EXISTS (SELECT 1 FROM task_runs AS newer WHERE newer.task_id = ? AND newer.id <> ? AND newer.status IN ?)", taskID, taskRunID, []string{"pending", "running", "retrying"}).
			Updates(map[string]any{"status": targetStatus, "last_error": code})
		if result.Error != nil {
			return fmt.Errorf("mark interrupted Task aggregate: %w", result.Error)
		}
		return nil
	})
}

// ReconcileInterruptedRuns performs the restart-only stale-run scan. It owns
// TaskRun queries rather than leaking them into the asset runtime; the final
// unfiltered query is the readiness gate and catches rows beyond this pass.
func (manager *Manager) ReconcileInterruptedRuns(ctx context.Context, limit int) (bool, error) {
	if manager == nil || manager.db == nil || limit <= 0 {
		return false, fmt.Errorf("%w: interrupted publication readiness unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var runs []model.TaskRun
	if err := interruptedPublicationRunsQuery(manager.db.WithContext(ctx)).
		Select("task_runs.*").Joins("JOIN tasks ON tasks.id = task_runs.task_id").
		Where("task_runs.status IN ?", []string{"pending", "running", "retrying"}).
		Order("task_runs.id ASC").Limit(limit).Find(&runs).Error; err != nil {
		return false, fmt.Errorf("list interrupted managed publication TaskRuns: %w", err)
	}
	for _, run := range runs {
		if _, live := manager.pendingRuns.Load(run.TaskID); live {
			continue
		}
		var point model.RecoveryPoint
		result := manager.db.WithContext(ctx).Where("producing_task_run_id = ?", run.ID).Limit(1).Find(&point)
		if result.Error != nil {
			return false, fmt.Errorf("load interrupted publication point: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			continue
		}
		outcome, reportable, err := interruptedOutcomeFromPoint(point, run)
		if err != nil {
			return false, err
		}
		if !reportable {
			continue
		}
		if err := manager.reportInterruptedPublication(ctx, outcome.TaskID, outcome.TaskRunID, interruptedOutcomeStatus(outcome), interruptedOutcomeCode(outcome)); err != nil {
			return false, err
		}
	}
	var remaining int64
	if err := interruptedPublicationRunsQuery(manager.db.WithContext(ctx)).
		Joins("JOIN tasks ON tasks.id = task_runs.task_id").
		Where("task_runs.status IN ?", []string{"pending", "running", "retrying"}).
		Count(&remaining).Error; err != nil {
		return false, fmt.Errorf("count unresolved managed publication TaskRuns: %w", err)
	}
	return remaining > 0, nil
}

func interruptedPublicationRunsQuery(db *gorm.DB) *gorm.DB {
	return db.Table("task_runs").Where(`
		LOWER(tasks.executor_type) = ? OR (
			LOWER(tasks.executor_type) IN ? AND EXISTS (
				SELECT 1 FROM recovery_points AS points
				WHERE points.producing_task_run_id = task_runs.id
				AND points.semantics IN ?
			)
		)`, "restic", []string{"rsync", "rclone"}, []string{string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline)})
}

func interruptedOutcomeFromPoint(point model.RecoveryPoint, run model.TaskRun) (publication.Outcome, bool, error) {
	if point.ProducingTaskRunID == nil || *point.ProducingTaskRunID != run.ID || point.ProducingTaskID == nil || *point.ProducingTaskID != run.TaskID {
		return publication.Outcome{}, false, fmt.Errorf("%w: interrupted publication TaskRun ownership drift", backupasset.ErrConflict)
	}
	lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil {
		return publication.Outcome{}, false, err
	}
	if lineage.TaskID != run.TaskID || lineage.TaskRunID != run.ID {
		return publication.Outcome{}, false, fmt.Errorf("%w: interrupted publication immutable lineage drift", backupasset.ErrConflict)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		return publication.Outcome{}, false, err
	}
	state := backupasset.RecoveryPointState(point.State)
	providerCommitRecorded, err := interruptedProviderCommitRecorded(point, lineage, consistency)
	if err != nil {
		return publication.Outcome{}, false, err
	}
	outcome := publication.Outcome{RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: run.TaskID, TaskRunID: run.ID, State: state, ProviderCommitRecorded: providerCommitRecorded, Code: consistency.Code}
	_, _, reportable := interruptedPublicationTaskRunState(outcome)
	return outcome, reportable, nil
}

func interruptedProviderCommitRecorded(point model.RecoveryPoint, lineage backupasset.PublicationLineageV1, consistency backupasset.PublicationConsistencyV1) (bool, error) {
	switch backupasset.PointVersionSemantics(point.Semantics) {
	case backupasset.PointNativeSnapshot:
		if lineage.PublicationMode != string(backupasset.PublicationNativeSnapshot) {
			return false, fmt.Errorf("%w: interrupted Restic point lineage drift", backupasset.ErrConflict)
		}
		return consistency.Provider == backupasset.ProviderRestic && consistency.ProviderCommitDigest != "" &&
			consistency.CaptureStartedAt != nil && consistency.CaptureFinishedAt != nil && strings.TrimSpace(point.EncryptedProviderLocator) != "", nil
	case backupasset.PointXirangManifest, backupasset.PointImportedBaseline:
		switch backupasset.TaskPublicationMode(lineage.PublicationMode) {
		case backupasset.PublicationVersionedHardlink, backupasset.PublicationVersionedFullCopy:
			locator, attempt, err := decodeInterruptedManagedRsyncLocator(point.EncryptedProviderLocator)
			if err != nil {
				return false, err
			}
			if locator.RepositoryID != point.RepositoryID || locator.RecoveryPointID != point.ID || attempt.RepositoryID != point.RepositoryID ||
				attempt.RecoveryPointID != point.ID || attempt.TaskRepositoryLinkID != lineage.TaskRepositoryLinkID || attempt.TaskID != lineage.TaskID ||
				attempt.TaskRunID != lineage.TaskRunID || attempt.PublicationMode != backupasset.TaskPublicationMode(lineage.PublicationMode) ||
				!attempt.PointDeadlineAt.Equal(lineage.PointDeadlineAt.UTC()) || locator.ManagedRootIdentityDigest != attempt.ManagedRootIdentityDigest {
				return false, fmt.Errorf("%w: interrupted managed Rsync locator drift", backupasset.ErrConflict)
			}
			return consistency.Provider == backupasset.ProviderRsync && validInterruptedDigest(consistency.ProviderCommitDigest) &&
				consistency.RepositoryIdentityDigest == locator.ManagedRootIdentityDigest && consistency.CapabilityRevision > 0, nil
		case backupasset.PublicationVersionedPrefix, backupasset.PublicationNativeObjectVersions:
			if consistency.ProviderCommitDigest == "" {
				return false, nil
			}
			locator, attempt, commit, err := decodeInterruptedManagedRcloneLocator(point.EncryptedProviderLocator)
			if err != nil {
				return false, err
			}
			if locator.RepositoryID != point.RepositoryID || locator.RecoveryPointID != point.ID || attempt.RepositoryID != point.RepositoryID ||
				attempt.RecoveryPointID != point.ID || attempt.TaskRepositoryLinkID != lineage.TaskRepositoryLinkID || attempt.TaskID != lineage.TaskID ||
				attempt.TaskRunID != lineage.TaskRunID || attempt.PublicationMode != backupasset.TaskPublicationMode(lineage.PublicationMode) ||
				!attempt.PointDeadlineAt.Equal(lineage.PointDeadlineAt.UTC()) || point.SourceFingerprint != locator.PhysicalIdentityDigest ||
				point.ManifestDigest != commit.ManifestIndexDigest || point.EntryCount != int64(commit.ManifestEntryCount) || point.LogicalBytes != int64(commit.LogicalBytes) {
				return false, fmt.Errorf("%w: interrupted managed Rclone locator drift", backupasset.ErrConflict)
			}
			return consistency.Provider == backupasset.ProviderRclone && consistency.ProviderCommitDigest == locator.ProviderCommitDigest &&
				consistency.RepositoryIdentityDigest == attempt.RepositoryIdentityDigest && consistency.CapabilityRevision > 0, nil
		default:
			return false, fmt.Errorf("%w: interrupted managed publication point lineage drift", backupasset.ErrConflict)
		}
	default:
		return false, fmt.Errorf("%w: interrupted publication semantics are unsupported", backupasset.ErrConflict)
	}
}

type interruptedManagedRcloneLocatorV1 struct {
	Version                 int                             `json:"version"`
	Provider                backupasset.ProviderKind        `json:"provider"`
	RepositoryID            string                          `json:"repository_id"`
	RecoveryPointID         string                          `json:"recovery_point_id"`
	AttemptID               string                          `json:"attempt_id"`
	PublicationMode         backupasset.TaskPublicationMode `json:"publication_mode"`
	TaggedAttempt           string                          `json:"tagged_attempt"`
	TaggedCommit            string                          `json:"tagged_commit"`
	ChildFenceDigest        string                          `json:"child_fence_digest"`
	CommitPayloadDigest     string                          `json:"commit_payload_digest"`
	PortableAttemptRoot     string                          `json:"portable_attempt_root,omitempty"`
	NativeCommitKey         string                          `json:"native_commit_key,omitempty"`
	NativeCommitVersionID   string                          `json:"native_commit_version_id,omitempty"`
	PhysicalIdentityDigest  string                          `json:"physical_identity_digest"`
	ProviderCommitDigest    string                          `json:"provider_commit_digest"`
	ManifestControlIdentity string                          `json:"manifest_control_identity"`
}

func decodeInterruptedManagedRcloneLocator(payload string) (interruptedManagedRcloneLocatorV1, provider.RcloneAttemptV1, provider.RcloneCommitV1, error) {
	invalid := func() (interruptedManagedRcloneLocatorV1, provider.RcloneAttemptV1, provider.RcloneCommitV1, error) {
		return interruptedManagedRcloneLocatorV1{}, provider.RcloneAttemptV1{}, provider.RcloneCommitV1{}, fmt.Errorf("%w: invalid interrupted managed Rclone locator", backupasset.ErrInvalidState)
	}
	if payload == "" || len(payload) > 64*1024 || rejectDuplicateInterruptedLocatorMembers(payload) != nil {
		return invalid()
	}
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	var locator interruptedManagedRcloneLocatorV1
	if err := decoder.Decode(&locator); err != nil {
		return invalid()
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return invalid()
	}
	attempt, err := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
	if err != nil {
		return invalid()
	}
	commit, err := provider.DecodeRcloneCommitV1(locator.TaggedCommit)
	if err != nil {
		return invalid()
	}
	commitDigest := sha256.Sum256([]byte(locator.TaggedCommit))
	if locator.Version != 1 || locator.Provider != backupasset.ProviderRclone || backupasset.ValidateOpaqueID(locator.RepositoryID) != nil ||
		backupasset.ValidateOpaqueID(locator.RecoveryPointID) != nil || backupasset.ValidateOpaqueID(locator.AttemptID) != nil ||
		!validInterruptedDigest(locator.ChildFenceDigest) || !validInterruptedDigest(locator.CommitPayloadDigest) ||
		!validInterruptedDigest(locator.PhysicalIdentityDigest) || !validInterruptedDigest(locator.ProviderCommitDigest) ||
		!validInterruptedDigest(locator.ManifestControlIdentity) || hex.EncodeToString(commitDigest[:]) != locator.ProviderCommitDigest ||
		attempt.RepositoryID != locator.RepositoryID || attempt.RecoveryPointID != locator.RecoveryPointID || attempt.AttemptID != locator.AttemptID ||
		attempt.PublicationMode != locator.PublicationMode || attempt.ChildFenceDigest != locator.ChildFenceDigest ||
		commit.RepositoryID != locator.RepositoryID || commit.RecoveryPointID != locator.RecoveryPointID || commit.AttemptID != locator.AttemptID ||
		commit.PublicationMode != locator.PublicationMode || commit.ChildFenceDigest != locator.ChildFenceDigest {
		return invalid()
	}
	switch locator.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		if locator.PortableAttemptRoot == "" || locator.NativeCommitKey != "" || locator.NativeCommitVersionID != "" ||
			commit.Portable == nil || commit.Native != nil || commit.Portable.CommitPayloadDigest != locator.CommitPayloadDigest ||
			commit.Portable.ControlIdentityDigest != locator.ManifestControlIdentity {
			return invalid()
		}
	case backupasset.PublicationNativeObjectVersions:
		if locator.PortableAttemptRoot != "" || locator.NativeCommitKey == "" || locator.NativeCommitVersionID == "" ||
			strings.ContainsRune(locator.NativeCommitKey, '\x00') || strings.ContainsRune(locator.NativeCommitVersionID, '\x00') ||
			commit.Native == nil || commit.Portable != nil || commit.Native.CommitKey != "" || commit.Native.CommitVersionID != "" ||
			commit.Native.CommitContentDigest != locator.CommitPayloadDigest || commit.Native.ManifestControlGraphDigest != locator.ManifestControlIdentity {
			return invalid()
		}
	default:
		return invalid()
	}
	return locator, attempt, commit, nil
}

type interruptedManagedRsyncLocatorV1 struct {
	Version                   int    `json:"version"`
	Provider                  string `json:"provider"`
	RepositoryID              string `json:"repository_id"`
	RecoveryPointID           string `json:"recovery_point_id"`
	FinalComponent            string `json:"final_component"`
	ManagedRootIdentityDigest string `json:"managed_root_identity_digest"`
	CommitMarkerDigest        string `json:"commit_marker_digest"`
	TaggedAttempt             string `json:"tagged_attempt"`
	ChildFenceDigest          string `json:"child_fence_digest"`
}

func decodeInterruptedManagedRsyncLocator(payload string) (interruptedManagedRsyncLocatorV1, provider.RsyncTreeAttemptV1, error) {
	if payload == "" || len(payload) > 24*1024 || rejectDuplicateInterruptedLocatorMembers(payload) != nil {
		return interruptedManagedRsyncLocatorV1{}, provider.RsyncTreeAttemptV1{}, fmt.Errorf("%w: invalid interrupted managed Rsync locator", backupasset.ErrInvalidState)
	}
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	var locator interruptedManagedRsyncLocatorV1
	if err := decoder.Decode(&locator); err != nil {
		return interruptedManagedRsyncLocatorV1{}, provider.RsyncTreeAttemptV1{}, fmt.Errorf("%w: invalid interrupted managed Rsync locator", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return interruptedManagedRsyncLocatorV1{}, provider.RsyncTreeAttemptV1{}, fmt.Errorf("%w: trailing interrupted managed Rsync locator", backupasset.ErrInvalidState)
	}
	attempt, err := provider.DecodeRsyncTreeAttemptV1(locator.TaggedAttempt)
	if err != nil || locator.Version != 1 || locator.Provider != string(backupasset.ProviderRsync) ||
		backupasset.ValidateOpaqueID(locator.RepositoryID) != nil || backupasset.ValidateOpaqueID(locator.RecoveryPointID) != nil ||
		locator.FinalComponent != locator.RecoveryPointID || !validInterruptedDigest(locator.ManagedRootIdentityDigest) ||
		!validInterruptedDigest(locator.CommitMarkerDigest) || !validInterruptedDigest(locator.ChildFenceDigest) {
		return interruptedManagedRsyncLocatorV1{}, provider.RsyncTreeAttemptV1{}, fmt.Errorf("%w: invalid interrupted managed Rsync locator", backupasset.ErrInvalidState)
	}
	return locator, attempt, nil
}

func rejectDuplicateInterruptedLocatorMembers(payload string) error {
	decoder := json.NewDecoder(strings.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return fmt.Errorf("invalid interrupted locator object")
	}
	members := make(map[string]struct{})
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := nameToken.(string)
		if !ok {
			return fmt.Errorf("invalid interrupted locator member")
		}
		if _, exists := members[name]; exists {
			return fmt.Errorf("duplicate interrupted locator member")
		}
		members[name] = struct{}{}
		var discard json.RawMessage
		if err := decoder.Decode(&discard); err != nil {
			return err
		}
		if bytes.Equal(bytes.TrimSpace(discard), []byte("null")) {
			return fmt.Errorf("null interrupted locator member")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return fmt.Errorf("invalid interrupted locator terminator")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing interrupted locator data")
	}
	return nil
}

func validInterruptedDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func interruptedOutcomeStatus(outcome publication.Outcome) string {
	status, _, _ := interruptedPublicationTaskRunState(outcome)
	return status
}

func interruptedOutcomeCode(outcome publication.Outcome) string {
	_, code, _ := interruptedPublicationTaskRunState(outcome)
	return code
}

var _ publication.InterruptedRunReporter = (*Manager)(nil)
var _ publication.InterruptedRunReadiness = (*Manager)(nil)
