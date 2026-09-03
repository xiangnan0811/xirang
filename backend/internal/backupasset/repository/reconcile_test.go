package repository

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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

func TestReconcileMutableSourceRepairWakesCatalog(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || connected.MutablePoint == nil {
		t.Fatalf("connected=%+v err=%v", connected, err)
	}

	wake := &catalogWakeRequesterSpy{accept: true}
	if err := service.SetCatalogWake(wake); err != nil {
		t.Fatal(err)
	}
	baseProbe := prober.probe
	prober.probe = func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		observation, err := baseProbe(binding)
		observation.SourceRevision = strings.Repeat("f", 64)
		return observation, err
	}
	result, err := service.Reconcile(context.Background(), connected.Repository.ID, RequestContext{})
	if err != nil || result.MutablePoint == nil {
		t.Fatalf("reconciled=%+v err=%v", result, err)
	}
	if calls := wake.calls.Load(); calls != 1 {
		t.Fatalf("Catalog wake calls=%d want=1", calls)
	}
	var point model.RecoveryPoint
	if err := db.First(&point, "id = ?", connected.MutablePoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	if point.SourceFingerprint != strings.Repeat("f", 64) {
		t.Fatalf("source fingerprint=%q want repaired revision", point.SourceFingerprint)
	}
}

func TestReconcileRejectsTaskArchivedDuringProbePreservingLastGoodFacts(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	probeCalls := 0
	archivedAt := time.Date(2026, 8, 17, 9, 10, 0, 0, time.UTC)
	prober := &scriptedProber{probe: func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		probeCalls++
		if probeCalls == 2 {
			if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("archived_at", archivedAt).Error; err != nil {
				return provider.RepositoryObservation{}, err
			}
		}
		identity, err := provider.DeriveScopedIdentity(binding.IdentitySalt, provider.ScopedIdentityDocument{
			Provider: binding.Provider, TaskID: binding.TaskID, NodeID: binding.NodeID, EndpointFacts: binding.EndpointFacts,
		})
		if err != nil {
			return provider.RepositoryObservation{}, err
		}
		observation := testObservation(binding.Provider, identity)
		observation.SourceRevision = strings.Repeat("e", 64)
		observation.ObservedAt = archivedAt
		return observation, nil
	}}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || connected.MutablePoint == nil {
		t.Fatalf("connected=%+v err=%v", connected, err)
	}
	var beforeRepository model.BackupRepository
	if err := db.First(&beforeRepository, "id = ?", connected.Repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	var beforeBinding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", connected.Repository.ID, bindingStatusActive).First(&beforeBinding).Error; err != nil {
		t.Fatal(err)
	}
	var beforePoint model.RecoveryPoint
	if err := db.First(&beforePoint, "id = ?", connected.MutablePoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return archivedAt }

	if _, err := service.Reconcile(context.Background(), connected.Repository.ID, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("Task archived during reconcile probe error=%v", err)
	}
	var afterRepository model.BackupRepository
	if err := db.First(&afterRepository, "id = ?", connected.Repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	var afterBinding model.RepositoryAccessBinding
	if err := db.First(&afterBinding, "id = ?", beforeBinding.ID).Error; err != nil {
		t.Fatal(err)
	}
	var afterPoint model.RecoveryPoint
	if err := db.First(&afterPoint, "id = ?", beforePoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	if afterRepository.Status != beforeRepository.Status || afterRepository.CapabilityRevision != beforeRepository.CapabilityRevision ||
		afterRepository.CapabilitiesJSON != beforeRepository.CapabilitiesJSON || !afterRepository.UpdatedAt.Equal(beforeRepository.UpdatedAt) {
		t.Fatalf("repository last-good facts changed: before=%+v after=%+v", beforeRepository, afterRepository)
	}
	if afterBinding.ConfigFingerprint != beforeBinding.ConfigFingerprint || afterBinding.EncryptedConfig != beforeBinding.EncryptedConfig ||
		!afterBinding.UpdatedAt.Equal(beforeBinding.UpdatedAt) {
		t.Fatalf("binding last-good facts changed: before=%+v after=%+v", beforeBinding, afterBinding)
	}
	if afterPoint.SourceFingerprint != beforePoint.SourceFingerprint || afterPoint.CapabilityRevision != beforePoint.CapabilityRevision ||
		afterPoint.CapabilitiesJSON != beforePoint.CapabilitiesJSON || !afterPoint.UpdatedAt.Equal(beforePoint.UpdatedAt) {
		t.Fatalf("point last-good facts changed: before=%+v after=%+v", beforePoint, afterPoint)
	}
}

func TestReconcileRejectsProviderNodeAndTaskLinkDriftDuringProbe(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*gorm.DB, model.Task, string) error
	}{
		{
			name: "Task Provider",
			mutate: func(db *gorm.DB, taskEntity model.Task, _ string) error {
				return db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("executor_type", "rclone").Error
			},
		},
		{
			name: "Node access lineage",
			mutate: func(db *gorm.DB, taskEntity model.Task, _ string) error {
				return db.Model(&model.Node{}).Where("id = ?", taskEntity.NodeID).Update("host", "drifted.example.invalid").Error
			},
		},
		{
			name: "Task link",
			mutate: func(db *gorm.DB, taskEntity model.Task, repositoryID string) error {
				driftedAt := time.Date(2026, 8, 17, 9, 12, 0, 0, time.UTC)
				return db.Model(&model.TaskRepositoryLink{}).Where("task_id = ? AND repository_id = ? AND unlinked_at IS NULL", taskEntity.ID, repositoryID).Updates(map[string]any{
					"unlinked_at": driftedAt,
					"updated_at":  driftedAt,
				}).Error
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newRepositoryTestDB(t)
			taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
			probeCalls := 0
			repositoryID := ""
			prober := &scriptedProber{probe: func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
				probeCalls++
				if probeCalls == 2 {
					if err := testCase.mutate(db, taskEntity, repositoryID); err != nil {
						return provider.RepositoryObservation{}, err
					}
				}
				identity, err := provider.DeriveScopedIdentity(binding.IdentitySalt, provider.ScopedIdentityDocument{
					Provider: binding.Provider, TaskID: binding.TaskID, NodeID: binding.NodeID, EndpointFacts: binding.EndpointFacts,
				})
				if err != nil {
					return provider.RepositoryObservation{}, err
				}
				observation := testObservation(binding.Provider, identity)
				observation.SourceRevision = strings.Repeat("e", 64)
				return observation, nil
			}}
			service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
			connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
			if err != nil || connected.MutablePoint == nil {
				t.Fatalf("connected=%+v err=%v", connected, err)
			}
			repositoryID = connected.Repository.ID
			var beforeRepository model.BackupRepository
			if err := db.First(&beforeRepository, "id = ?", repositoryID).Error; err != nil {
				t.Fatal(err)
			}
			var beforePoint model.RecoveryPoint
			if err := db.First(&beforePoint, "id = ?", connected.MutablePoint.ID).Error; err != nil {
				t.Fatal(err)
			}

			if _, err := service.Reconcile(context.Background(), repositoryID, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("lineage drift reconcile error=%v", err)
			}
			var afterRepository model.BackupRepository
			if err := db.First(&afterRepository, "id = ?", repositoryID).Error; err != nil {
				t.Fatal(err)
			}
			var afterPoint model.RecoveryPoint
			if err := db.First(&afterPoint, "id = ?", beforePoint.ID).Error; err != nil {
				t.Fatal(err)
			}
			if afterRepository.CapabilityRevision != beforeRepository.CapabilityRevision || afterRepository.CapabilitiesJSON != beforeRepository.CapabilitiesJSON ||
				!afterRepository.UpdatedAt.Equal(beforeRepository.UpdatedAt) {
				t.Fatalf("repository last-good facts changed: before=%+v after=%+v", beforeRepository, afterRepository)
			}
			if afterPoint.SourceFingerprint != beforePoint.SourceFingerprint || afterPoint.CapabilityRevision != beforePoint.CapabilityRevision ||
				!afterPoint.UpdatedAt.Equal(beforePoint.UpdatedAt) {
				t.Fatalf("point last-good facts changed: before=%+v after=%+v", beforePoint, afterPoint)
			}
		})
	}
}

func TestReconcileRejectsRepositoryAndBindingRefreshDuringProbePreservingCommittedFacts(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	probeCalls := 0
	repositoryID := ""
	independentAt := time.Date(2026, 8, 17, 9, 45, 0, 0, time.UTC)
	var committedRepository model.BackupRepository
	var committedBinding model.RepositoryAccessBinding
	var committedPoint model.RecoveryPoint
	prober := scopedObservationProber(backupasset.ProviderRsync)
	baseProbe := prober.probe
	prober.probe = func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		probeCalls++
		if probeCalls == 2 {
			if err := db.First(&committedRepository, "id = ?", repositoryID).Error; err != nil {
				return provider.RepositoryObservation{}, err
			}
			committedRepository.CapabilityRevision += 7
			committedRepository.CapabilitiesJSON = `{"list":true,"open_sequential":true,"open_range":false}`
			committedRepository.UpdatedAt = independentAt
			if err := db.Save(&committedRepository).Error; err != nil {
				return provider.RepositoryObservation{}, err
			}
			if err := db.Where("repository_id = ? AND status = ?", repositoryID, bindingStatusActive).First(&committedBinding).Error; err != nil {
				return provider.RepositoryObservation{}, err
			}
			document, err := decodeBindingDocument(committedBinding.EncryptedConfig)
			if err != nil {
				return provider.RepositoryObservation{}, err
			}
			document.AdapterRevision = "independent-reader:v9"
			payload, err := encodeBindingDocument(document)
			if err != nil {
				return provider.RepositoryObservation{}, err
			}
			committedBinding.EncryptedConfig = payload
			committedBinding.ConfigFingerprint = strings.Repeat("c", 64)
			committedBinding.UpdatedAt = independentAt
			if err := db.Save(&committedBinding).Error; err != nil {
				return provider.RepositoryObservation{}, err
			}
			if err := db.First(&committedRepository, "id = ?", repositoryID).Error; err != nil {
				return provider.RepositoryObservation{}, err
			}
			if err := db.First(&committedBinding, "id = ?", committedBinding.ID).Error; err != nil {
				return provider.RepositoryObservation{}, err
			}
			if err := db.Where("repository_id = ?", repositoryID).First(&committedPoint).Error; err != nil {
				return provider.RepositoryObservation{}, err
			}
		}
		observation, err := baseProbe(binding)
		observation.SourceRevision = strings.Repeat("d", 64)
		observation.ObservedAt = independentAt.Add(time.Minute)
		return observation, err
	}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || connected.MutablePoint == nil {
		t.Fatalf("connected=%+v err=%v", connected, err)
	}
	repositoryID = connected.Repository.ID

	if _, err := service.Reconcile(context.Background(), repositoryID, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("repository/binding refreshed during reconcile probe error=%v", err)
	}
	var afterRepository model.BackupRepository
	if err := db.First(&afterRepository, "id = ?", repositoryID).Error; err != nil {
		t.Fatal(err)
	}
	var afterBinding model.RepositoryAccessBinding
	if err := db.First(&afterBinding, "id = ?", committedBinding.ID).Error; err != nil {
		t.Fatal(err)
	}
	var afterPoint model.RecoveryPoint
	if err := db.First(&afterPoint, "id = ?", committedPoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterRepository, committedRepository) || !reflect.DeepEqual(afterBinding, committedBinding) ||
		!reflect.DeepEqual(afterPoint, committedPoint) {
		t.Fatal("stale reconcile changed independently committed repository graph facts")
	}
}

func TestReconcileStaleProbeFailureDoesNotDowngradeNewerAuthority(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		probeError bool
	}{
		{name: "provider probe error", probeError: true},
		{name: "invalid observation"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newRepositoryTestDB(t)
			taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
			prober := scopedObservationProber(backupasset.ProviderRsync)
			service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
			connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
			if err != nil || connected.MutablePoint == nil {
				t.Fatalf("connected=%+v err=%v", connected, err)
			}
			newerAt := time.Date(2026, 8, 17, 10, 20, 0, 0, time.UTC)
			baseProbe := prober.probe
			var committedRepository model.BackupRepository
			var committedBinding model.RepositoryAccessBinding
			var committedPoint model.RecoveryPoint
			prober.probe = func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
				observation, probeErr := baseProbe(binding)
				if probeErr != nil {
					return provider.RepositoryObservation{}, probeErr
				}
				if err := db.First(&committedRepository, "id = ?", connected.Repository.ID).Error; err != nil {
					return provider.RepositoryObservation{}, err
				}
				newerCapabilities := observation.Capabilities
				newerCapabilities.OpenRange = !newerCapabilities.OpenRange
				capabilitiesJSON, err := json.Marshal(newerCapabilities)
				if err != nil {
					return provider.RepositoryObservation{}, err
				}
				committedRepository.Status = string(backupasset.RepositoryOnline)
				committedRepository.CapabilityRevision += 7
				committedRepository.CapabilitiesJSON = string(capabilitiesJSON)
				committedRepository.LastSeenAt = &newerAt
				committedRepository.LastReconciledAt = &newerAt
				committedRepository.UpdatedAt = newerAt
				if err := db.Save(&committedRepository).Error; err != nil {
					return provider.RepositoryObservation{}, err
				}
				if err := db.Where("repository_id = ? AND status = ?", connected.Repository.ID, bindingStatusActive).
					First(&committedBinding).Error; err != nil {
					return provider.RepositoryObservation{}, err
				}
				document, err := decodeBindingDocument(committedBinding.EncryptedConfig)
				if err != nil {
					return provider.RepositoryObservation{}, err
				}
				document.AdapterRevision = "newer-reader:v11"
				payload, err := encodeBindingDocument(document)
				if err != nil {
					return provider.RepositoryObservation{}, err
				}
				committedBinding.EncryptedConfig = payload
				committedBinding.ConfigFingerprint = strings.Repeat("c", 64)
				committedBinding.UpdatedAt = newerAt
				if err := db.Save(&committedBinding).Error; err != nil {
					return provider.RepositoryObservation{}, err
				}
				if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", connected.MutablePoint.ID).Updates(map[string]any{
					"source_fingerprint":    strings.Repeat("d", 64),
					"capability_revision":   committedRepository.CapabilityRevision,
					"capabilities_json":     committedRepository.CapabilitiesJSON,
					"physical_availability": string(backupasset.PhysicalOnline),
					"observed_at":           newerAt,
					"updated_at":            newerAt,
				}).Error; err != nil {
					return provider.RepositoryObservation{}, err
				}
				if err := db.First(&committedRepository, "id = ?", committedRepository.ID).Error; err != nil {
					return provider.RepositoryObservation{}, err
				}
				if err := db.First(&committedBinding, "id = ?", committedBinding.ID).Error; err != nil {
					return provider.RepositoryObservation{}, err
				}
				if err := db.First(&committedPoint, "id = ?", connected.MutablePoint.ID).Error; err != nil {
					return provider.RepositoryObservation{}, err
				}
				if testCase.probeError {
					return provider.RepositoryObservation{}, &provider.CapabilityError{
						Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityProviderUnavailable},
					}
				}
				observation.AdapterRevision = ""
				return observation, nil
			}
			spy := &auditSpy{}
			service.audit = spy

			_, reconcileErr := service.Reconcile(context.Background(), connected.Repository.ID,
				RequestContext{Actor: backupasset.AuditActor{UserID: 7, Username: "admin", Role: "admin"}, CorrelationID: "stale-reconcile"})
			var afterRepository model.BackupRepository
			if err := db.First(&afterRepository, "id = ?", connected.Repository.ID).Error; err != nil {
				t.Fatal(err)
			}
			var afterBinding model.RepositoryAccessBinding
			if err := db.First(&afterBinding, "id = ?", committedBinding.ID).Error; err != nil {
				t.Fatal(err)
			}
			var afterPoint model.RecoveryPoint
			if err := db.First(&afterPoint, "id = ?", committedPoint.ID).Error; err != nil {
				t.Fatal(err)
			}
			preserved := reflect.DeepEqual(afterRepository, committedRepository) && reflect.DeepEqual(afterBinding, committedBinding) &&
				reflect.DeepEqual(afterPoint, committedPoint)
			if !errors.Is(reconcileErr, backupasset.ErrConflict) || !preserved || len(spy.inputs) != 0 {
				t.Fatalf("stale failure error=%v preserved=%v failure_audits=%d", reconcileErr, preserved, len(spy.inputs))
			}
		})
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

func TestReconcileResticLocksRepositoryBeforeTaskLineage(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "restic", "sftp:user@example.invalid:/repository", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("d", 64)
	recording := false
	probeCalls := 0
	prober := &scriptedProber{probe: func(provider.AccessBinding) (provider.RepositoryObservation, error) {
		probeCalls++
		if probeCalls == 2 {
			recording = true
		}
		return testObservation(backupasset.ProviderRestic, identity), nil
	}}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}

	type lockOrderContextKey struct{}
	marker := &struct{}{}
	ctx := context.WithValue(context.Background(), lockOrderContextKey{}, marker)
	var events []string
	callbackName := "test:reconcile_restic_lock_order_" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Context.Value(lockOrderContextKey{}) != marker || !recording {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		switch tx.Statement.Schema.Table {
		case (model.BackupRepository{}).TableName(), "tasks", "nodes",
			"ssh_keys", (model.TaskRepositoryLink{}).TableName(),
			(model.RepositoryAccessBinding{}).TableName():
			events = append(events, tx.Statement.Schema.Table)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	if _, err := service.Reconcile(ctx, connected.Repository.ID, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0] != (model.BackupRepository{}).TableName() {
		t.Fatalf("Restic Reconcile lock sequence=%v, want Repository first", events)
	}
	repositoryLocks := 0
	for _, table := range events {
		if table == (model.BackupRepository{}).TableName() {
			repositoryLocks++
		}
	}
	if repositoryLocks != 1 {
		t.Fatalf("Restic Reconcile Repository lock count=%d sequence=%v, want one prelock", repositoryLocks, events)
	}
	for index, table := range events {
		if table == "tasks" {
			if index == 0 {
				t.Fatalf("Restic Reconcile Task lock preceded Repository: %v", events)
			}
			return
		}
	}
	t.Fatalf("Restic Reconcile did not lock Task after Repository: %v", events)
}
