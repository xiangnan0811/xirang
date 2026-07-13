package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestReconcileMutableKeepsSingletonAndRevisesOnlyEffectiveCapabilities(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || connected.MutablePoint == nil {
		t.Fatalf("connected=%+v err=%v", connected, err)
	}
	baseProbe := prober.probe
	observedAt := time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return observedAt }
	prober.probe = func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		observation, err := baseProbe(binding)
		observation.SourceRevision = strings.Repeat("c", 64)
		observation.ObservedAt = observedAt
		return observation, err
	}
	first, err := service.Reconcile(context.Background(), connected.Repository.ID, RequestContext{})
	if err != nil || first.MutablePoint == nil || first.MutablePoint.ID != connected.MutablePoint.ID || first.Repository.CapabilityRevision != connected.Repository.CapabilityRevision {
		t.Fatalf("first reconcile=%+v err=%v", first, err)
	}
	payload, err := json.Marshal(first.MutablePoint)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "source_fingerprint") || strings.Contains(string(payload), strings.Repeat("c", 64)) {
		t.Fatalf("source fingerprint leaked in DTO: %s", payload)
	}
	var point model.RecoveryPoint
	if err := db.First(&point, "id = ?", connected.MutablePoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	if point.SourceFingerprint != strings.Repeat("c", 64) || point.ObservedAt == nil || !point.ObservedAt.Equal(observedAt) || point.State != string(backupasset.RecoveryPointObserved) {
		t.Fatalf("point after reconcile=%+v", point)
	}

	prober.probe = func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		observation, err := baseProbe(binding)
		observation.SourceRevision = strings.Repeat("d", 64)
		observation.ObservedAt = observedAt.Add(time.Minute)
		observation.Capabilities.OpenRange = false
		return observation, err
	}
	second, err := service.Reconcile(context.Background(), connected.Repository.ID, RequestContext{})
	if err != nil || second.Repository.CapabilityRevision != connected.Repository.CapabilityRevision+1 || second.MutablePoint == nil || second.MutablePoint.ID != connected.MutablePoint.ID || second.MutablePoint.CapabilityRevision != second.Repository.CapabilityRevision {
		t.Fatalf("second reconcile=%+v err=%v", second, err)
	}
	var count int64
	if err := db.Model(&model.RecoveryPoint{}).Where("repository_id = ?", connected.Repository.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("mutable point count=%d err=%v", count, err)
	}
}

func TestReconcileFailurePreservesLastSuccessfulObservation(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || connected.MutablePoint == nil {
		t.Fatalf("connected=%+v err=%v", connected, err)
	}
	var before model.RecoveryPoint
	if err := db.First(&before, "id = ?", connected.MutablePoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	failedAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return failedAt }
	probeErr := &provider.CapabilityError{Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityProviderUnavailable}}
	prober.probe = func(provider.AccessBinding) (provider.RepositoryObservation, error) {
		return provider.RepositoryObservation{}, probeErr
	}
	if _, err := service.Reconcile(context.Background(), connected.Repository.ID, RequestContext{}); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("reconcile failure=%v", err)
	}
	var repository model.BackupRepository
	if err := db.First(&repository, "id = ?", connected.Repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	var after model.RecoveryPoint
	if err := db.First(&after, "id = ?", connected.MutablePoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	if repository.Status != string(backupasset.RepositoryOffline) || repository.CapabilityRevision != connected.Repository.CapabilityRevision || repository.LastReconciledAt == nil || !repository.LastReconciledAt.Equal(failedAt) {
		t.Fatalf("repository after failed reconcile=%+v", repository)
	}
	if after.ID != before.ID || after.State != string(backupasset.RecoveryPointObserved) || after.SourceFingerprint != before.SourceFingerprint || after.ObservedAt == nil || before.ObservedAt == nil || !after.ObservedAt.Equal(*before.ObservedAt) || after.PhysicalAvailability != string(backupasset.PhysicalOffline) {
		t.Fatalf("point before=%+v after=%+v", before, after)
	}
}

func TestReconcileInvalidProviderObservationMarksRepositoryOffline(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || connected.MutablePoint == nil {
		t.Fatalf("connected=%+v err=%v", connected, err)
	}
	baseProbe := prober.probe
	prober.probe = func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		observation, probeErr := baseProbe(binding)
		observation.AdapterRevision = ""
		return observation, probeErr
	}

	if _, err := service.Reconcile(context.Background(), connected.Repository.ID, RequestContext{}); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("invalid observation error=%v", err)
	}
	var repository model.BackupRepository
	if err := db.First(&repository, "id = ?", connected.Repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	capabilities, err := decodeRepositoryCapabilities(repository.CapabilitiesJSON)
	if err != nil {
		t.Fatal(err)
	}
	if repository.Status != string(backupasset.RepositoryOffline) || capabilities.Reason == nil || capabilities.Reason.Code != backupasset.CapabilityProviderProtocolIncompatible {
		t.Fatalf("repository did not record invalid Provider observation: repository=%+v capabilities=%+v", repository, capabilities)
	}
}

func TestReconcileIdentityMismatchDoesNotUpdateRepository(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	storedIdentity := provider.ScopedIdentityPrefix(backupasset.ProviderRsync) + strings.Repeat("e", 64)
	if err := db.Model(&model.BackupRepository{}).Where("id = ?", connected.Repository.ID).Update("repository_identity", storedIdentity).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reconcile(context.Background(), connected.Repository.ID, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("identity mismatch error=%v", err)
	}
	var repository model.BackupRepository
	if err := db.First(&repository, "id = ?", connected.Repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	if repository.RepositoryIdentity == nil || *repository.RepositoryIdentity != storedIdentity || repository.CapabilityRevision != connected.Repository.CapabilityRevision {
		t.Fatalf("repository changed on identity mismatch: %+v", repository)
	}
}

func TestReconcileResticUpdatesRepositoryWithoutCreatingPoint(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("f", 64)
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, identity)}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Reconcile(context.Background(), connected.Repository.ID, RequestContext{})
	if err != nil || result.MutablePoint != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var count int64
	if err := db.Model(&model.RecoveryPoint{}).Where("repository_id = ?", connected.Repository.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("Restic point count=%d err=%v", count, err)
	}
}

func TestReconcileRejectsBindingTaskProviderDriftBeforeProbe(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("6", 64)
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, identity)}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("executor_type", "rclone").Error; err != nil {
		t.Fatal(err)
	}

	if _, err := service.Reconcile(context.Background(), connected.Repository.ID, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("reconcile after binding Task Provider drift error=%v", err)
	}
	if prober.calls != 1 {
		t.Fatalf("Provider drift reached reconcile probe: calls=%d", prober.calls)
	}
}

func TestReconcileReturnsAndRollsBackTransactionFailure(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected reconcile repository update failure")
	if err := db.Callback().Update().Before("gorm:update").Register("test:fail_reconcile_repository_update", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.BackupRepository{}).TableName() {
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reconcile(context.Background(), connected.Repository.ID, RequestContext{}); !errors.Is(err, injected) {
		t.Fatalf("reconcile transaction error=%v", err)
	}
}

func TestRepositoryMethodsEmitTypedAuditActionsAndStages(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
	spy := &auditSpy{}
	service.audit = spy
	requestContext := RequestContext{
		Actor:         backupasset.AuditActor{UserID: 7, Username: "audit-user", Role: "admin"},
		CorrelationID: "corr-repository-audit",
	}
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, requestContext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(context.Background(), RepositoryListRequest{}, VisibilityScope{Role: "admin", UserID: 7}, requestContext); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Detail(context.Background(), connected.Repository.ID, VisibilityScope{Role: "admin", UserID: 7}, requestContext); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reconcile(context.Background(), connected.Repository.ID, requestContext); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Disconnect(context.Background(), connected.Repository.ID, requestContext); err != nil {
		t.Fatal(err)
	}

	want := []struct {
		action backupasset.AuditAction
		stage  string
	}{
		{backupasset.AuditActionRepositoryConnect, "commit"},
		{backupasset.AuditActionRepositoryList, "list"},
		{backupasset.AuditActionRepositoryList, "detail"},
		{backupasset.AuditActionRepositoryReconcile, "commit"},
		{backupasset.AuditActionRepositoryDisconnect, "commit"},
	}
	if len(spy.inputs) != len(want) {
		t.Fatalf("audit inputs=%+v", spy.inputs)
	}
	for index, expected := range want {
		input := spy.inputs[index]
		if input.Action != expected.action || input.Outcome != backupasset.AuditOutcomeSuccess ||
			input.Fields[backupasset.AuditFieldStage] != expected.stage ||
			input.Fields[backupasset.AuditFieldCorrelationID] != requestContext.CorrelationID {
			t.Fatalf("audit[%d]=%+v want action=%s stage=%s", index, input, expected.action, expected.stage)
		}
	}
}

func TestDisconnectPreservesMutableEvidenceAndReconnectsWithRetainedSalt(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || connected.MutablePoint == nil {
		t.Fatalf("connected=%+v err=%v", connected, err)
	}
	var before model.RecoveryPoint
	if err := db.First(&before, "id = ?", connected.MutablePoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	disconnected, err := service.Disconnect(context.Background(), connected.Repository.ID, RequestContext{})
	if err != nil || disconnected.Repository.Status != backupasset.RepositoryDisconnected || disconnected.MutablePoint == nil || disconnected.MutablePoint.ID != connected.MutablePoint.ID {
		t.Fatalf("disconnected=%+v err=%v", disconnected, err)
	}
	var after model.RecoveryPoint
	if err := db.First(&after, "id = ?", connected.MutablePoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.State != string(backupasset.RecoveryPointObserved) || after.PhysicalAvailability != string(backupasset.PhysicalOffline) || after.SourceFingerprint != before.SourceFingerprint || after.ObservedAt == nil || before.ObservedAt == nil || !after.ObservedAt.Equal(*before.ObservedAt) || after.EncryptedProviderLocator != before.EncryptedProviderLocator {
		t.Fatalf("point before=%+v after=%+v", before, after)
	}
	var links int64
	if err := db.Model(&model.TaskRepositoryLink{}).Where("repository_id = ?", connected.Repository.ID).Count(&links).Error; err != nil || links != 1 {
		t.Fatalf("links=%d err=%v", links, err)
	}
	reconnected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || reconnected.Repository.ID != connected.Repository.ID || reconnected.MutablePoint == nil || reconnected.MutablePoint.ID != connected.MutablePoint.ID ||
		reconnected.Repository.CapabilityRevision != disconnected.Repository.CapabilityRevision+1 || reconnected.MutablePoint.CapabilityRevision != reconnected.Repository.CapabilityRevision {
		t.Fatalf("reconnected=%+v err=%v", reconnected, err)
	}
	var active, revoked int64
	if err := db.Model(&model.RepositoryAccessBinding{}).Where("repository_id = ? AND status = ?", connected.Repository.ID, bindingStatusActive).Count(&active).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.RepositoryAccessBinding{}).Where("repository_id = ? AND status = ?", connected.Repository.ID, bindingStatusRevoked).Count(&revoked).Error; err != nil {
		t.Fatal(err)
	}
	if active != 1 || revoked != 1 {
		t.Fatalf("active=%d revoked=%d", active, revoked)
	}
}
