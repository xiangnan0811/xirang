package repository

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type blockingRclonePreflighter struct {
	started  chan RclonePortablePreflightInput
	release  chan struct{}
	evidence RcloneVersioningPreflightEvidence
}

func (fake *blockingRclonePreflighter) PreflightPortable(_ context.Context, input RclonePortablePreflightInput) (RcloneVersioningPreflightEvidence, error) {
	fake.started <- input
	<-fake.release
	return fake.evidence, nil
}

func (*blockingRclonePreflighter) PreflightNative(context.Context, RcloneNativePreflightInput) (RcloneVersioningPreflightEvidence, error) {
	return RcloneVersioningPreflightEvidence{}, errors.New("unexpected native preflight")
}

type rcloneTransactionalDriftSettings struct {
	repositorySettings
}

func (settings rcloneTransactionalDriftSettings) BackupAssetSettingsSnapshotTx(context.Context, *gorm.DB) (map[string]string, error) {
	values, err := settings.repositorySettings.BackupAssetSettingsSnapshot()
	if err == nil {
		values["backup_assets.rclone_low_level_retries"] = "4"
	}
	return values, err
}

func TestRclonePreflightFinalizeChecksTransactionSettingsFence(t *testing.T) {
	fixture := newRclonePortableVersioningWorkflowFixture(t)
	fixture.service.foundation = backupasset.NewFoundationService(rcloneTransactionalDriftSettings{
		repositorySettings: completeRepositoryFoundationSettings(true),
	})
	_, err := fixture.service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, RequestedMode: backupasset.PublicationVersionedPrefix,
	})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("final transaction settings drift error=%v", err)
	}
	if _, ok := fixture.service.rclonePreflights.get(fixture.preflight.PreflightID); ok {
		t.Fatal("replaced old proof remained activatable")
	}
}

func TestRclonePreflightActivationChecksTransactionSettingsFence(t *testing.T) {
	fixture := newRclonePortableVersioningWorkflowFixture(t)
	fixture.service.foundation = backupasset.NewFoundationService(rcloneTransactionalDriftSettings{
		repositorySettings: completeRepositoryFoundationSettings(true),
	})
	_, err := fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, PreflightID: fixture.preflight.PreflightID,
		MigrationChoice: backupasset.RcloneFirstNewPoint,
	})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("transaction settings drift error=%v", err)
	}
	var link model.TaskRepositoryLink
	if err := fixture.db.Where("task_id = ? AND unlinked_at IS NULL", fixture.task.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.PublicationMode != string(backupasset.PublicationLegacyMutable) {
		t.Fatal("configuration drift committed the activation")
	}
}

func TestRclonePreflightCandidateReplacementRejectsLateAndConcurrentResults(t *testing.T) {
	fixture := newRclonePortableVersioningWorkflowFixture(t)
	service := fixture.service
	record, ok := service.rclonePreflights.get(fixture.preflight.PreflightID)
	if !ok {
		t.Fatal("fixture preflight not ready")
	}
	fake := &blockingRclonePreflighter{started: make(chan RclonePortablePreflightInput, 1), release: make(chan struct{}), evidence: record.evidence}
	var release sync.Once
	defer release.Do(func() { close(fake.release) })
	service.rclonePreflighter = fake
	request := backupasset.RcloneVersioningPreflightRequest{TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, RequestedMode: backupasset.PublicationVersionedPrefix}
	done := make(chan error, 1)
	go func() { _, err := service.CreateRcloneVersioningPreflight(context.Background(), request); done <- err }()
	var input RclonePortablePreflightInput
	select {
	case input = <-fake.started:
	case err := <-done:
		t.Fatalf("provider was not reached: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("provider start timed out")
	}
	if input.PreflightID == "" || input.CandidateID == "" || input.CapabilityRevision != 2 || input.TaskRevision != fixture.revision ||
		input.ControlPayloadMaxBytes <= 0 || input.FullVerifyMaxBytes <= 0 || input.ManifestOptions.SpoolMaxBytes <= 0 {
		t.Fatal("provider did not receive the fixed identity, revisions and budgets")
	}
	if _, err := service.CreateRcloneVersioningPreflight(context.Background(), request); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("concurrent preflight error=%v", err)
	}
	setup, err := service.CreateRclonePortableBindingSetup(context.Background(), backupasset.RcloneBindingSetupRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision,
	})
	if err != nil || setup.BaseBindingRevision != "0" {
		t.Fatalf("replacement setup base=%q err=%v", setup.BaseBindingRevision, err)
	}
	summary, err := service.SetRclonePortableBinding(context.Background(), backupasset.RclonePortableBindingRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, ExpectedBindingRevision: rcloneSetupBaseRevision(t, setup),
		SetupID: setup.SetupID, TargetRemote: "backup", ManagedRootLocator: "backup:managed/replacement",
		BoundConfig: "[backup]\ntype = b2\naccount = FAKE_B2_ACCOUNT_FOR_TEST_ONLY\nkey = FAKE_B2_KEY_FOR_TEST_ONLY\n",
	})
	if err != nil || summary.BindingRevision != "1" {
		t.Fatalf("replacement summary=%+v err=%v", summary, err)
	}
	release.Do(func() { close(fake.release) })
	if err := <-done; !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("late old generation error=%v", err)
	}
	if _, ok := service.rclonePreflights.get(input.PreflightID); ok {
		t.Fatal("late result became activatable")
	}
	current, ok := service.rcloneCandidates.get(fixture.task.ID)
	if !ok || current.setupID != setup.SetupID || current.preflighting {
		t.Fatal("late result changed the replacement candidate")
	}
}

func TestRclonePreflightRejectsNodeDriftAfterProviderIO(t *testing.T) {
	fixture := newRclonePortableVersioningWorkflowFixture(t)
	service := fixture.service
	record, ok := service.rclonePreflights.get(fixture.preflight.PreflightID)
	if !ok {
		t.Fatal("fixture preflight not ready")
	}
	fake := &blockingRclonePreflighter{started: make(chan RclonePortablePreflightInput, 1), release: make(chan struct{}), evidence: record.evidence}
	var release sync.Once
	defer release.Do(func() { close(fake.release) })
	service.rclonePreflighter = fake
	request := backupasset.RcloneVersioningPreflightRequest{TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, RequestedMode: backupasset.PublicationVersionedPrefix}
	done := make(chan error, 1)
	go func() { _, err := service.CreateRcloneVersioningPreflight(context.Background(), request); done <- err }()
	var input RclonePortablePreflightInput
	select {
	case input = <-fake.started:
	case err := <-done:
		t.Fatalf("provider was not reached: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("provider start timed out")
	}
	if err := fixture.db.Model(&model.Node{}).Where("id = ?", fixture.task.NodeID).Update("host", "changed.invalid").Error; err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(fake.release) })
	if err := <-done; !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("node drift error=%v", err)
	}
	if _, ok := service.rclonePreflights.get(input.PreflightID); ok {
		t.Fatal("node drift installed a ready record")
	}
}

func TestRclonePreflightRejectsTaskBindingAndConfigDrift(t *testing.T) {
	for _, drift := range []string{"task", "binding", "config"} {
		t.Run(drift, func(t *testing.T) {
			fixture := newRclonePortableVersioningWorkflowFixture(t)
			service := fixture.service
			settings := completeRepositoryFoundationSettings(true)
			service.foundation = backupasset.NewFoundationService(settings)
			record, ok := service.rclonePreflights.get(fixture.preflight.PreflightID)
			if !ok {
				t.Fatal("fixture preflight unavailable")
			}
			fake := &blockingRclonePreflighter{started: make(chan RclonePortablePreflightInput, 1), release: make(chan struct{}), evidence: record.evidence}
			var release sync.Once
			defer release.Do(func() { close(fake.release) })
			service.rclonePreflighter = fake
			request := backupasset.RcloneVersioningPreflightRequest{TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, RequestedMode: backupasset.PublicationVersionedPrefix}
			done := make(chan error, 1)
			go func() { _, err := service.CreateRcloneVersioningPreflight(context.Background(), request); done <- err }()
			var input RclonePortablePreflightInput
			select {
			case input = <-fake.started:
			case err := <-done:
				t.Fatalf("provider was not reached: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("provider start timed out")
			}
			switch drift {
			case "task":
				if err := fixture.db.Model(&model.Task{}).Where("id = ?", fixture.task.ID).Update("rsync_source", "/changed").Error; err != nil {
					t.Fatal(err)
				}
			case "binding":
				if err := fixture.db.Model(&model.RepositoryAccessBinding{}).Where("id = ?", record.candidate.plan.binding.ID).Update("config_fingerprint", strings.Repeat("f", 64)).Error; err != nil {
					t.Fatal(err)
				}
			case "config":
				settings["backup_assets.rclone_low_level_retries"] = "4"
			}
			release.Do(func() { close(fake.release) })
			if err := <-done; !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("%s drift error=%v", drift, err)
			}
			if _, ok := service.rclonePreflights.get(input.PreflightID); ok {
				t.Fatal("drift installed ready evidence")
			}
		})
	}
}

func TestRclonePreflightActivationReservationSurvivesCandidateExpiry(t *testing.T) {
	fixture := newRclonePortableVersioningWorkflowFixture(t)
	service := fixture.service
	candidate, ok := service.rcloneCandidates.get(fixture.task.ID)
	if !ok {
		t.Fatal("fixture candidate unavailable")
	}
	var nanos atomic.Int64
	nanos.Store(fixture.now.UnixNano())
	now := func() time.Time { return time.Unix(0, nanos.Load()).UTC() }
	service.now = now
	service.rcloneCandidates.now = now
	service.rclonePreflights.now = now
	setup, err := service.CreateRclonePortableBindingSetup(context.Background(), backupasset.RcloneBindingSetupRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision,
	})
	if err != nil || setup.BaseBindingRevision != "0" {
		t.Fatalf("reservation replacement setup base=%q err=%v", setup.BaseBindingRevision, err)
	}
	replacement := backupasset.RclonePortableBindingRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, ExpectedBindingRevision: rcloneSetupBaseRevision(t, setup),
		SetupID: setup.SetupID, TargetRemote: "backup", ManagedRootLocator: "backup:managed/replacement",
		BoundConfig: "[backup]\ntype = b2\naccount = FAKE_B2_ACCOUNT_FOR_TEST_ONLY\nkey = FAKE_B2_KEY_FOR_TEST_ONLY\n",
	}
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	service.admission = &rsyncVersioningTransitioner{beforePersist: func() error {
		close(started)
		<-release
		return nil
	}}
	activation := backupasset.RcloneVersioningActivationRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision,
		PreflightID: fixture.preflight.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint,
	}
	done := make(chan error, 1)
	go func() { _, err := service.ActivateRcloneVersioning(context.Background(), activation); done <- err }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("activation did not reach drain: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("activation drain timed out")
	}
	if _, err := service.ActivateRcloneVersioning(context.Background(), activation); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("concurrent activation error=%v", err)
	}
	if _, err := service.SetRclonePortableBinding(context.Background(), replacement); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("setter replaced candidate during activation reservation: %v", err)
	}
	if _, err := service.SetRclonePortableBinding(context.Background(), replacement); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("reservation-rejected setup was revived: %v", err)
	}
	nanos.Store(fixture.preflight.ExpiresAt.Add(time.Second).UnixNano())
	candidate.setupID = strings.Repeat("d", 32)
	candidate.expiresAt = now().Add(time.Hour)
	if err := service.replaceRcloneCandidate(candidate); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("replacement during expired reservation error=%v", err)
	}
	if _, err := service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, RequestedMode: backupasset.PublicationVersionedPrefix,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("preflight during expired reservation error=%v", err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-done; !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("expired activation error=%v", err)
	}
	if err := service.replaceRcloneCandidate(candidate); err != nil {
		t.Fatalf("reservation not released: %v", err)
	}
}

func TestRclonePreflightSettlingPersistsOnlyTemporaryProgress(t *testing.T) {
	fixture := newRcloneNativeVersioningWorkflowFixture(t)
	credentialExpiry := fixture.now.Add(time.Hour)
	fixture.service.rclonePreflighter = &rcloneVersioningPreflighterStub{evidence: RcloneVersioningPreflightEvidence{
		Settling: true, SettlingObservedAt: fixture.now, Mode: backupasset.PublicationNativeObjectVersions,
		CredentialExpiresAt: &credentialExpiry, EncryptionProfile: backupasset.RcloneEncryptionSSES3,
		KMSKeyStatus: backupasset.RcloneKMSNotApplicable,
	}}
	request := backupasset.RcloneVersioningPreflightRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, RequestedMode: backupasset.PublicationNativeObjectVersions,
	}
	result, err := fixture.service.CreateRcloneVersioningPreflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.State != backupasset.RcloneStateCapabilitySettling ||
		result.Summary.ReasonCode != backupasset.RcloneReasonCapabilitySettling || result.Summary.CapabilityRevision != "0" {
		t.Fatalf("settling summary=%+v", result.Summary)
	}
	summary, err := fixture.service.RcloneVersioningSummary(context.Background(), fixture.task.ID)
	if err != nil || summary.State != backupasset.RcloneStateCapabilitySettling {
		t.Fatalf("reopened settling summary=%+v error=%v", summary, err)
	}
	activation := backupasset.RcloneVersioningActivationRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, PreflightID: result.PreflightID,
		MigrationChoice: backupasset.RcloneFirstNewPoint,
	}
	if _, err := fixture.service.ActivateRcloneVersioning(context.Background(), activation); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("settling token activated: %v", err)
	}
	restarted, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry,
		Keyring: fixture.service.keyring, Now: fixture.service.now, Admission: fixture.service.admission,
		History: fixture.service.history, Metrics: fixture.service.metrics, Publication: fixture.service.publication,
		RclonePreflight: fixture.service.rclonePreflighter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ActivateRcloneVersioning(context.Background(), activation); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("restart accepted old token: %v", err)
	}
}

func TestRclonePreflightNativeInsufficientInitialWindowDoesNotCallProvider(t *testing.T) {
	fixture := newRcloneNativeVersioningWorkflowFixture(t)
	candidate, ok := fixture.service.rcloneCandidates.get(fixture.task.ID)
	if !ok {
		t.Fatal("native candidate unavailable")
	}
	candidate.setupID = strings.Repeat("a", 32)
	candidate.expiresAt = fixture.now.Add(15 * time.Minute)
	candidate.nativeObserved = false
	candidate.latestPreflightID = ""
	candidate.preflighting = false
	if err := fixture.service.replaceRcloneCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	stub := fixture.service.rclonePreflighter.(*rcloneVersioningPreflighterStub)
	before := stub.nativeCalls
	_, err := fixture.service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, RequestedMode: backupasset.PublicationNativeObjectVersions,
	})
	if !errors.Is(err, backupasset.ErrConflict) || stub.nativeCalls != before {
		t.Fatalf("short candidate error=%v provider calls=%d", err, stub.nativeCalls-before)
	}
}

func TestRclonePreflightReadyExpiryHonorsNativeCredentialMargin(t *testing.T) {
	fixture := newRcloneNativeVersioningWorkflowFixture(t)
	stub := fixture.service.rclonePreflighter.(*rcloneVersioningPreflighterStub)
	expiry := fixture.now.Add(10 * time.Minute)
	stub.evidence.CredentialExpiresAt = &expiry
	request := backupasset.RcloneVersioningPreflightRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, RequestedMode: backupasset.PublicationNativeObjectVersions,
	}
	result, err := fixture.service.CreateRcloneVersioningPreflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ExpiresAt.Equal(expiry.Add(-rcloneNativePreflightSessionMargin)) {
		t.Fatal("ready expiry exceeded the native credential margin")
	}
	expiry = fixture.now.Add(rcloneNativePreflightSessionMargin)
	if _, err := fixture.service.CreateRcloneVersioningPreflight(context.Background(), request); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("insufficient credential margin error=%v", err)
	}
	if _, ok := fixture.service.rclonePreflights.get(result.PreflightID); ok {
		t.Fatal("previous ready proof remained active after replacement")
	}
}

func TestRclonePreflightMissingProviderIsTypedUnavailable(t *testing.T) {
	fixture := newRclonePortableVersioningWorkflowFixture(t)
	fixture.service.rclonePreflighter = nil
	_, err := fixture.service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, RequestedMode: backupasset.PublicationVersionedPrefix,
	})
	var capability *provider.CapabilityError
	if !errors.As(err, &capability) || capability.Reason.Code != backupasset.CapabilityProviderUnavailable {
		t.Fatalf("missing provider error=%v", err)
	}
}
