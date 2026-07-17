package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

func TestRcloneNativeHealthCandidatesAreBoundedToActiveCommittedLineage(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	point := seedCommittedRcloneHealthPoint(t, fixture)

	candidates, err := fixture.service.ListRcloneNativeHealthCandidates(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0] != fixture.repository.ID {
		t.Fatalf("native health candidates=%v", candidates)
	}

	if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).
		Update("unlinked_at", fixture.now).Error; err != nil {
		t.Fatal(err)
	}
	candidates, err = fixture.service.ListRcloneNativeHealthCandidates(context.Background(), 1)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("unlinked native health candidates=%v err=%v point=%s", candidates, err, point.ID)
	}
}

func TestRcloneNativeHealthPersistsSuccessAndPermanentRiskWithoutMutableFallback(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
		point := seedCommittedRcloneHealthPoint(t, fixture)
		fixture.service.rcloneNativeHealthCheck = func(context.Context, string) (provider.RcloneNativeHealthResult, error) {
			return provider.RcloneNativeHealthResult{
				Reason: backupasset.RcloneReasonReady, EvidenceDigest: strings.Repeat("e", 64),
				VerifiedReferenceCount: 2, VerifiedBytes: 10,
			}, nil
		}
		result, err := fixture.service.CheckRcloneNativeHealth(context.Background(), fixture.repository.ID)
		if err != nil || result.Reason != backupasset.RcloneReasonReady {
			t.Fatalf("native health result=%+v err=%v", result, err)
		}
		var repository model.BackupRepository
		if err := fixture.db.First(&repository, "id = ?", fixture.repository.ID).Error; err != nil {
			t.Fatal(err)
		}
		if repository.Status != string(backupasset.RepositoryOnline) || repository.LastReconciledAt == nil ||
			!repository.LastReconciledAt.Equal(fixture.now) {
			t.Fatalf("healthy native repository=%+v", repository)
		}
		if err := fixture.db.First(&point, "id = ?", point.ID).Error; err != nil {
			t.Fatal(err)
		}
		if point.State != string(backupasset.RecoveryPointCommitted) || point.PhysicalAvailability != string(backupasset.PhysicalOnline) {
			t.Fatalf("healthy native point=%+v", point)
		}
	})

	t.Run("kms risk", func(t *testing.T) {
		fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
		point := seedCommittedRcloneHealthPoint(t, fixture)
		fixture.service.rcloneNativeHealthCheck = func(context.Context, string) (provider.RcloneNativeHealthResult, error) {
			return provider.RcloneNativeHealthResult{Reason: backupasset.RcloneReasonKMSKeyUnavailable}, errors.New("FAKE_KMS_HEALTH_FAILURE_FOR_TEST_ONLY")
		}
		result, err := fixture.service.CheckRcloneNativeHealth(context.Background(), fixture.repository.ID)
		if err == nil || result.Reason != backupasset.RcloneReasonKMSKeyUnavailable {
			t.Fatalf("native health result=%+v err=%v", result, err)
		}
		var repository model.BackupRepository
		if err := fixture.db.First(&repository, "id = ?", fixture.repository.ID).Error; err != nil {
			t.Fatal(err)
		}
		if repository.Status != string(backupasset.RepositoryDegraded) {
			t.Fatalf("at-risk native repository=%+v", repository)
		}
		if err := fixture.db.First(&point, "id = ?", point.ID).Error; err != nil {
			t.Fatal(err)
		}
		if point.State != string(backupasset.RecoveryPointDegraded) || point.PhysicalAvailability != string(backupasset.PhysicalUnknown) {
			t.Fatalf("at-risk native point=%+v", point)
		}
		if _, prepareErr := fixture.service.Prepare(context.Background(), fixture.run()); !errors.Is(prepareErr, backupasset.ErrConflict) {
			t.Fatalf("at-risk native writer error=%v, want conflict before provider mutation", prepareErr)
		}
	})
}

func TestRcloneNativeHealthAvailabilityFailureDoesNotRewriteCommittedTruth(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	point := seedCommittedRcloneHealthPoint(t, fixture)
	fixture.service.rcloneNativeHealthCheck = func(context.Context, string) (provider.RcloneNativeHealthResult, error) {
		return provider.RcloneNativeHealthResult{Reason: backupasset.RcloneReasonProviderTimeout}, context.DeadlineExceeded
	}
	if _, err := fixture.service.CheckRcloneNativeHealth(context.Background(), fixture.repository.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("native availability health error=%v", err)
	}
	var repository model.BackupRepository
	if err := fixture.db.First(&repository, "id = ?", fixture.repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	if repository.Status != string(backupasset.RepositoryOffline) {
		t.Fatalf("offline native repository=%+v", repository)
	}
	if err := fixture.db.First(&point, "id = ?", point.ID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointCommitted) || point.PhysicalAvailability != string(backupasset.PhysicalOffline) {
		t.Fatalf("availability failure rewrote native point truth=%+v", point)
	}
}

func seedCommittedRcloneHealthPoint(t *testing.T, fixture *rclonePublicationFixture) model.RecoveryPoint {
	t.Helper()
	committedAt := fixture.now.Add(-time.Minute)
	point := model.RecoveryPoint{
		ID: strings.Repeat("9", 32), RepositoryID: fixture.repository.ID,
		Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointCommitted),
		CommittedAt: &committedAt, LineageJSON: `{}`, ConsistencyJSON: `{}`, FidelityJSON: `{}`,
		CapabilitiesJSON: fixture.repository.CapabilitiesJSON, CapabilityRevision: fixture.repository.CapabilityRevision,
		ImmutabilityLevel:    string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone),
		CreatedAt: committedAt, UpdatedAt: committedAt,
	}
	if err := fixture.db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	return point
}
