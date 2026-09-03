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
func TestRcloneNativeHealthRejectsStalePointBeforeProviderAccess(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	_, point, _ := completeRcloneTestPoint(t, fixture)
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
		Update("capability_revision", point.CapabilityRevision+1).Error; err != nil {
		t.Fatal(err)
	}
	beforeAssume, beforeProbe, beforeList := fixture.nativeFactory.assumeCalls, fixture.nativeFactory.probeCalls, fixture.nativeFactory.listCalls
	beforeHead, beforeDescribe := fixture.nativeFactory.headVersionCalls, fixture.nativeFactory.describeKeyCalls
	result, err := fixture.service.checkRcloneNativeProviderHealth(context.Background(), fixture.repository.ID)
	if !errors.Is(err, backupasset.ErrConflict) || result.Reason != backupasset.RcloneReasonManifestMismatch {
		t.Fatalf("stale native Rclone health result=%+v err=%v, want manifest mismatch conflict", result, err)
	}
	if fixture.nativeFactory.assumeCalls != beforeAssume || fixture.nativeFactory.probeCalls != beforeProbe ||
		fixture.nativeFactory.listCalls != beforeList || fixture.nativeFactory.headVersionCalls != beforeHead ||
		fixture.nativeFactory.describeKeyCalls != beforeDescribe {
		t.Fatalf("stale native Rclone health used provider credentials/access: assume=%d/%d probe=%d/%d list=%d/%d head=%d/%d describe=%d/%d",
			fixture.nativeFactory.assumeCalls, beforeAssume, fixture.nativeFactory.probeCalls, beforeProbe,
			fixture.nativeFactory.listCalls, beforeList, fixture.nativeFactory.headVersionCalls, beforeHead,
			fixture.nativeFactory.describeKeyCalls, beforeDescribe)
	}
}
func TestRcloneNativeHealthRejectsStaleAttemptBeforeProviderAccess(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	_, point, _ := completeRcloneTestPoint(t, fixture)
	locator, err := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
	if err != nil {
		t.Fatal(err)
	}
	attempt.CapabilityRevision++
	locator.TaggedAttempt, err = provider.EncodePublicationAttempt(provider.NewRclonePublicationAttempt(attempt))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(locator)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
		Update("encrypted_provider_locator", string(payload)).Error; err != nil {
		t.Fatal(err)
	}
	beforeAssume, beforeProbe, beforeList := fixture.nativeFactory.assumeCalls, fixture.nativeFactory.probeCalls, fixture.nativeFactory.listCalls
	beforeHead, beforeDescribe := fixture.nativeFactory.headVersionCalls, fixture.nativeFactory.describeKeyCalls
	result, err := fixture.service.checkRcloneNativeProviderHealth(context.Background(), fixture.repository.ID)
	if !errors.Is(err, backupasset.ErrConflict) || result.Reason != backupasset.RcloneReasonManifestMismatch {
		t.Fatalf("stale native Rclone attempt health result=%+v err=%v, want manifest mismatch conflict", result, err)
	}
	if fixture.nativeFactory.assumeCalls != beforeAssume || fixture.nativeFactory.probeCalls != beforeProbe ||
		fixture.nativeFactory.listCalls != beforeList || fixture.nativeFactory.headVersionCalls != beforeHead ||
		fixture.nativeFactory.describeKeyCalls != beforeDescribe {
		t.Fatalf("stale native Rclone attempt health used provider credentials/access: assume=%d/%d probe=%d/%d list=%d/%d head=%d/%d describe=%d/%d",
			fixture.nativeFactory.assumeCalls, beforeAssume, fixture.nativeFactory.probeCalls, beforeProbe,
			fixture.nativeFactory.listCalls, beforeList, fixture.nativeFactory.headVersionCalls, beforeHead,
			fixture.nativeFactory.describeKeyCalls, beforeDescribe)
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
func TestRcloneNativeHealthRejectsMalformedDurableEvidenceBeforeProviderAccess(t *testing.T) {
	for _, field := range []string{"malformed_consistency", "consistency_revision", "consistency_digest", "native_commit_revision"} {
		t.Run(field, func(t *testing.T) {
			fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
			_, point, _ := completeRcloneTestPoint(t, fixture)
			switch field {
			case "malformed_consistency":
				if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
					Update("consistency_json", "{}").Error; err != nil {
					t.Fatal(err)
				}
			case "consistency_revision", "consistency_digest":
				consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
				if err != nil {
					t.Fatal(err)
				}
				if field == "consistency_revision" {
					consistency.CapabilityRevision++
				} else {
					consistency.ProviderCommitDigest = strings.Repeat("f", 64)
				}
				encoded, err := backupasset.EncodePublicationConsistency(consistency)
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
					Update("consistency_json", encoded).Error; err != nil {
					t.Fatal(err)
				}
			case "native_commit_revision":
				locator, err := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
				if err != nil {
					t.Fatal(err)
				}
				commit, err := provider.DecodeRcloneCommitV1(locator.TaggedCommit)
				if err != nil || commit.Native == nil {
					t.Fatalf("decode native health commit=%+v err=%v", commit, err)
				}
				commit.Native.CapabilityRevision++
				locator.TaggedCommit, err = provider.EncodeProviderCommit(provider.NewRcloneProviderCommit(commit))
				if err != nil {
					t.Fatal(err)
				}
				locator.ProviderCommitDigest = digestText(locator.TaggedCommit)
				locatorPayload, err := json.Marshal(locator)
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
					Update("encrypted_provider_locator", string(locatorPayload)).Error; err != nil {
					t.Fatal(err)
				}
				consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
				if err != nil {
					t.Fatal(err)
				}
				consistency.ProviderCommitDigest = locator.ProviderCommitDigest
				encoded, err := backupasset.EncodePublicationConsistency(consistency)
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
					Update("consistency_json", encoded).Error; err != nil {
					t.Fatal(err)
				}
			}
			beforeAssume, beforeProbe, beforeList := fixture.nativeFactory.assumeCalls, fixture.nativeFactory.probeCalls, fixture.nativeFactory.listCalls
			beforeHead, beforeDescribe := fixture.nativeFactory.headVersionCalls, fixture.nativeFactory.describeKeyCalls
			result, err := fixture.service.checkRcloneNativeProviderHealth(context.Background(), fixture.repository.ID)
			if !errors.Is(err, backupasset.ErrConflict) || result.Reason != backupasset.RcloneReasonManifestMismatch {
				t.Fatalf("durable evidence field=%s result=%+v err=%v, want manifest mismatch conflict", field, result, err)
			}
			if fixture.nativeFactory.assumeCalls != beforeAssume || fixture.nativeFactory.probeCalls != beforeProbe ||
				fixture.nativeFactory.listCalls != beforeList || fixture.nativeFactory.headVersionCalls != beforeHead ||
				fixture.nativeFactory.describeKeyCalls != beforeDescribe {
				t.Fatalf("durable evidence field=%s used provider credentials/access: assume=%d/%d probe=%d/%d list=%d/%d head=%d/%d describe=%d/%d",
					field, fixture.nativeFactory.assumeCalls, beforeAssume, fixture.nativeFactory.probeCalls, beforeProbe,
					fixture.nativeFactory.listCalls, beforeList, fixture.nativeFactory.headVersionCalls, beforeHead,
					fixture.nativeFactory.describeKeyCalls, beforeDescribe)
			}
		})
	}
}

func TestRcloneNativeHealthLocalConflictDoesNotPersist(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	point := seedCommittedRcloneHealthPoint(t, fixture)
	var beforeRepository model.BackupRepository
	if err := fixture.db.First(&beforeRepository, "id = ?", fixture.repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	var beforePoint model.RecoveryPoint
	if err := fixture.db.First(&beforePoint, "id = ?", point.ID).Error; err != nil {
		t.Fatal(err)
	}
	fixture.service.rcloneNativeHealthCheck = func(context.Context, string) (provider.RcloneNativeHealthResult, error) {
		return provider.RcloneNativeHealthResult{}, backupasset.ErrConflict
	}
	result, err := fixture.service.CheckRcloneNativeHealth(context.Background(), fixture.repository.ID)
	if !errors.Is(err, backupasset.ErrConflict) || result.Reason != "" {
		t.Fatalf("local health conflict result=%+v err=%v, want empty reason and ErrConflict", result, err)
	}
	var afterRepository model.BackupRepository
	if err := fixture.db.First(&afterRepository, "id = ?", fixture.repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	var afterPoint model.RecoveryPoint
	if err := fixture.db.First(&afterPoint, "id = ?", point.ID).Error; err != nil {
		t.Fatal(err)
	}
	if afterRepository.Status != beforeRepository.Status || afterRepository.CapabilitiesJSON != beforeRepository.CapabilitiesJSON ||
		afterRepository.LastReconciledAt != nil || afterRepository.LastSeenAt != nil ||
		afterPoint.State != beforePoint.State || afterPoint.PhysicalAvailability != beforePoint.PhysicalAvailability ||
		afterPoint.CapabilitiesJSON != beforePoint.CapabilitiesJSON {
		t.Fatalf("local health conflict persisted mutation: before repository=%+v point=%+v after repository=%+v point=%+v",
			beforeRepository, beforePoint, afterRepository, afterPoint)
	}
}

func TestRcloneNativeHealthRejectsExpectedCapabilityAndImmutabilityTOCTOU(t *testing.T) {
	for _, field := range []string{"capability_revision", "immutability_level"} {
		t.Run(field, func(t *testing.T) {
			fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
			point := seedCommittedRcloneHealthPoint(t, fixture)
			expectedRevision := fixture.repository.CapabilityRevision
			result := provider.RcloneNativeHealthResult{
				Reason: backupasset.RcloneReasonReady, EvidenceDigest: strings.Repeat("e", 64),
			}
			fixture.service.rcloneNativeHealthCheck = func(context.Context, string) (provider.RcloneNativeHealthResult, error) {
				if field == "capability_revision" {
					if err := fixture.db.Model(&model.BackupRepository{}).Where("id = ?", fixture.repository.ID).
						Update("capability_revision", expectedRevision+1).Error; err != nil {
						t.Fatalf("mutate repository capability revision: %v", err)
					}
				} else {
					if err := fixture.db.Model(&model.BackupRepository{}).Where("id = ?", fixture.repository.ID).
						Update("immutability_level", string(backupasset.ImmutabilityMutable)).Error; err != nil {
						t.Fatalf("mutate repository immutability: %v", err)
					}
				}
				return result, nil
			}
			if _, err := fixture.service.CheckRcloneNativeHealth(context.Background(), fixture.repository.ID); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("TOCTOU field=%s health error=%v, want ErrConflict", field, err)
			}
			var repository model.BackupRepository
			if err := fixture.db.First(&repository, "id = ?", fixture.repository.ID).Error; err != nil {
				t.Fatal(err)
			}
			var afterPoint model.RecoveryPoint
			if err := fixture.db.First(&afterPoint, "id = ?", point.ID).Error; err != nil {
				t.Fatal(err)
			}
			if repository.Status != string(backupasset.RepositoryOnline) ||
				afterPoint.State != string(backupasset.RecoveryPointCommitted) ||
				afterPoint.PhysicalAvailability != string(backupasset.PhysicalUnknown) {
				t.Fatalf("TOCTOU field=%s mutated health state repository=%+v point=%+v", field, repository, afterPoint)
			}
		})
	}
}
