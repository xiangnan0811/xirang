package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

const maxCommitEvidenceBytes = 64 << 10

type EvidenceDTO struct {
	RecoveryPointID string                  `json:"recovery_point_id"`
	Lineage         LineageEvidenceDTO      `json:"lineage"`
	Manifest        ManifestEvidenceDTO     `json:"manifest"`
	Publication     PublicationEvidenceDTO  `json:"publication_verification"`
	RestoreDrills   RestoreDrillEvidenceDTO `json:"restore_drills"`
}

type LineageEvidenceDTO struct {
	Status     EvidenceLayerStatus `json:"status"`
	TaskID     *uint               `json:"task_id"`
	TaskRunID  *uint               `json:"task_run_id"`
	TaskName   string              `json:"task_name"`
	NodeID     uint                `json:"node_id"`
	NodeName   string              `json:"node_name"`
	Trigger    string              `json:"trigger"`
	RunStatus  string              `json:"run_status"`
	StartedAt  *time.Time          `json:"started_at"`
	FinishedAt *time.Time          `json:"finished_at"`
}

type ManifestEvidenceDTO struct {
	Status           EvidenceLayerStatus              `json:"status"`
	ID               string                           `json:"id"`
	Revision         int                              `json:"revision"`
	DigestAlgorithm  string                           `json:"digest_algorithm"`
	Digest           string                           `json:"digest"`
	EntryCount       int64                            `json:"entry_count"`
	LogicalBytes     int64                            `json:"logical_bytes"`
	Generator        string                           `json:"generator"`
	GeneratorVersion string                           `json:"generator_version"`
	Completeness     backupasset.ManifestCompleteness `json:"completeness"`
	CreatedAt        *time.Time                       `json:"created_at"`
	UpdatedAt        *time.Time                       `json:"updated_at"`
}

type PublicationEvidenceDTO struct {
	Status            EvidenceLayerStatus                 `json:"status"`
	Provider          backupasset.ProviderKind            `json:"provider"`
	Completion        backupasset.ProviderCompletionClass `json:"completion"`
	FailureCode       backupasset.PublicationFailureCode  `json:"failure_code"`
	CaptureStartedAt  *time.Time                          `json:"capture_started_at"`
	CaptureFinishedAt *time.Time                          `json:"capture_finished_at"`
	FilesProcessed    uint64                              `json:"files_processed"`
	LogicalBytes      uint64                              `json:"logical_bytes"`
	CommitRecorded    bool                                `json:"commit_recorded"`
}

type RestoreDrillEvidenceDTO struct {
	Status EvidenceLayerStatus      `json:"status"`
	Items  []RestoreDrillSummaryDTO `json:"items"`
}

type RestoreDrillSummaryDTO struct {
	TaskRunID          uint       `json:"task_run_id"`
	Status             string     `json:"status"`
	FailedStep         string     `json:"failed_step"`
	ConfidenceEligible bool       `json:"confidence_eligible"`
	StartedAt          *time.Time `json:"started_at"`
	FinishedAt         *time.Time `json:"finished_at"`
	DurationMs         int64      `json:"duration_ms"`
}

func (service *Service) GetEvidence(ctx context.Context, pointID string, scope AuthorizationScope) (EvidenceDTO, error) {
	if _, err := service.GetRecoveryPoint(ctx, pointID, scope); err != nil {
		return EvidenceDTO{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	point, _, err := service.loadPointAndRepository(ctx, pointID)
	if err != nil {
		return EvidenceDTO{}, err
	}
	result := EvidenceDTO{
		RecoveryPointID: point.ID,
		Lineage:         LineageEvidenceDTO{Status: EvidenceNotRecorded},
		Manifest:        ManifestEvidenceDTO{Status: EvidenceNotRecorded},
		Publication:     PublicationEvidenceDTO{Status: EvidenceNotRecorded},
		RestoreDrills:   RestoreDrillEvidenceDTO{Status: EvidenceNotRecorded, Items: []RestoreDrillSummaryDTO{}},
	}
	result.Lineage = service.projectLineageEvidence(ctx, point)
	manifest, manifestPresent, manifestErr := service.loadEvidenceManifest(ctx, point.ID)
	if manifestErr != nil {
		return EvidenceDTO{}, manifestErr
	}
	if manifestPresent {
		result.Manifest = projectManifestEvidence(manifest)
	}
	result.Publication = projectPublicationEvidence(point.ConsistencyJSON, manifest, manifestPresent)
	result.RestoreDrills, err = service.projectRestoreDrillEvidence(ctx, point)
	if err != nil {
		return EvidenceDTO{}, err
	}
	return result, nil
}

func (service *Service) projectLineageEvidence(ctx context.Context, point model.RecoveryPoint) LineageEvidenceDTO {
	result := LineageEvidenceDTO{
		Status: EvidenceNotRecorded, TaskID: point.ProducingTaskID, TaskRunID: point.ProducingTaskRunID,
		TaskName: point.ProducingTaskNameSnapshot, NodeID: point.ProducingNodeIDSnapshot, NodeName: point.ProducingNodeNameSnapshot,
	}
	if point.ProducingTaskID == nil || point.ProducingTaskRunID == nil {
		return result
	}
	var run model.TaskRun
	err := service.db.WithContext(ctx).Select("id", "task_id", "trigger_type", "status", "started_at", "finished_at").
		Where("id = ? AND task_id = ?", *point.ProducingTaskRunID, *point.ProducingTaskID).Take(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		result.Status = EvidenceInvalid
		result.Trigger = ""
		result.RunStatus = ""
		return result
	}
	if err != nil {
		result.Status = EvidenceUnavailable
		return result
	}
	if !validEvidenceTrigger(run.TriggerType) || !validEvidenceTaskStatus(run.Status) {
		result.Status = EvidenceInvalid
		return result
	}
	result.Status = EvidenceRecorded
	result.Trigger = run.TriggerType
	result.RunStatus = run.Status
	result.StartedAt = utcPointer(run.StartedAt)
	result.FinishedAt = utcPointer(run.FinishedAt)
	return result
}

func (service *Service) loadEvidenceManifest(ctx context.Context, pointID string) (model.RecoveryPointManifest, bool, error) {
	var manifest model.RecoveryPointManifest
	err := service.db.WithContext(ctx).Where("recovery_point_id = ? AND is_active = ?", pointID, true).Take(&manifest).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.RecoveryPointManifest{}, false, nil
	}
	if err != nil {
		return model.RecoveryPointManifest{}, false, fmt.Errorf("load Catalog evidence manifest: %w", err)
	}
	return manifest, true, nil
}

func projectManifestEvidence(manifest model.RecoveryPointManifest) ManifestEvidenceDTO {
	completeness := backupasset.ManifestCompleteness(manifest.Completeness)
	if backupasset.ValidateOpaqueID(manifest.ID) != nil || manifest.Revision <= 0 || manifest.DigestAlgorithm != "sha256" ||
		!lowerHexLength(manifest.Digest, 64) || manifest.EntryCount < 0 || manifest.LogicalBytes < 0 ||
		(completeness != backupasset.ManifestComplete && completeness != backupasset.ManifestPartial && completeness != backupasset.ManifestUnavailable) ||
		!safeEvidenceLabel(manifest.Generator, 64, false) || !safeEvidenceLabel(manifest.GeneratorVersion, 64, false) {
		return ManifestEvidenceDTO{Status: EvidenceInvalid}
	}
	created, updated := manifest.CreatedAt.UTC(), manifest.UpdatedAt.UTC()
	return ManifestEvidenceDTO{
		Status: EvidenceRecorded, ID: manifest.ID, Revision: manifest.Revision, DigestAlgorithm: manifest.DigestAlgorithm,
		Digest: manifest.Digest, EntryCount: manifest.EntryCount, LogicalBytes: manifest.LogicalBytes,
		Generator: manifest.Generator, GeneratorVersion: manifest.GeneratorVersion, Completeness: completeness,
		CreatedAt: &created, UpdatedAt: &updated,
	}
}

func projectPublicationEvidence(raw string, manifest model.RecoveryPointManifest, manifestPresent bool) PublicationEvidenceDTO {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return PublicationEvidenceDTO{Status: EvidenceNotRecorded}
	}
	consistency, err := backupasset.DecodePublicationConsistency(raw)
	if err != nil {
		return PublicationEvidenceDTO{Status: EvidenceInvalid}
	}
	commitRecorded := false
	if manifestPresent && strings.TrimSpace(manifest.EncryptedCommitEvidence) != "" {
		payload := []byte(manifest.EncryptedCommitEvidence)
		if len(payload) > maxCommitEvidenceBytes || !json.Valid(payload) {
			return PublicationEvidenceDTO{Status: EvidenceInvalid}
		}
		commitRecorded = true
	}
	return PublicationEvidenceDTO{
		Status: EvidenceRecorded, Provider: consistency.Provider, Completion: consistency.Completion,
		FailureCode: consistency.Code, CaptureStartedAt: utcPointer(consistency.CaptureStartedAt),
		CaptureFinishedAt: utcPointer(consistency.CaptureFinishedAt), FilesProcessed: consistency.FilesProcessed,
		LogicalBytes: consistency.LogicalBytes, CommitRecorded: commitRecorded,
	}
}

func (service *Service) projectRestoreDrillEvidence(ctx context.Context, point model.RecoveryPoint) (RestoreDrillEvidenceDTO, error) {
	result := RestoreDrillEvidenceDTO{Status: EvidenceNotRecorded, Items: []RestoreDrillSummaryDTO{}}
	if point.ProducingTaskRunID == nil {
		return result, nil
	}
	var rows []model.RestoreDrillEvidence
	if err := service.db.WithContext(ctx).
		Select("task_run_id", "status", "failed_step", "confidence_eligible", "started_at", "finished_at", "duration_ms").
		Where("source_task_run_id = ?", *point.ProducingTaskRunID).
		Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return RestoreDrillEvidenceDTO{}, fmt.Errorf("load exact Catalog restore drill evidence: %w", err)
	}
	if len(rows) == 0 {
		return result, nil
	}
	items := make([]RestoreDrillSummaryDTO, 0, len(rows))
	for _, row := range rows {
		if row.TaskRunID == 0 || row.DurationMs < 0 || !validRestoreDrillStatus(row.Status) ||
			!validRestoreDrillFailedStep(row.FailedStep) {
			return RestoreDrillEvidenceDTO{Status: EvidenceInvalid, Items: []RestoreDrillSummaryDTO{}}, nil
		}
		items = append(items, RestoreDrillSummaryDTO{
			TaskRunID: row.TaskRunID, Status: row.Status, FailedStep: row.FailedStep,
			ConfidenceEligible: row.ConfidenceEligible, StartedAt: utcPointer(row.StartedAt),
			FinishedAt: utcPointer(row.FinishedAt), DurationMs: row.DurationMs,
		})
	}
	return RestoreDrillEvidenceDTO{Status: EvidenceRecorded, Items: items}, nil
}

func validEvidenceTrigger(value string) bool {
	switch value {
	case "manual", "cron", "retry", "restore", "chain", "drill":
		return true
	default:
		return false
	}
}

func validEvidenceTaskStatus(value string) bool {
	switch value {
	case "pending", "running", "success", "failed", "retrying", "canceled", "warning", "skipped":
		return true
	default:
		return false
	}
}

func validRestoreDrillStatus(value string) bool {
	switch value {
	case "pending", "running", "success", "failed", "skipped", "canceled":
		return true
	default:
		return false
	}
}

func validRestoreDrillFailedStep(value string) bool {
	switch value {
	case "", "sandbox_precheck", "restore", "pre_verify", "verify", "post_verify", "cleanup_boundary", "cleanup", "unknown":
		return true
	default:
		return false
	}
}

func safeEvidenceLabel(value string, maxLength int, allowEmpty bool) bool {
	if (!allowEmpty && strings.TrimSpace(value) == "") || len(value) > maxLength || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	return true
}
