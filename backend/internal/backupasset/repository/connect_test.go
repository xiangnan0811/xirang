package repository

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type typedAssetAuditWriterSpy struct {
	input backupasset.AuditEventInput
	err   error
}

func (spy *typedAssetAuditWriterSpy) Write(_ context.Context, input backupasset.AuditEventInput) (model.BackupAssetAuditEvent, error) {
	spy.input = input
	return model.BackupAssetAuditEvent{}, spy.err
}

func TestAssetAuditSinkAdaptsFoundationWriter(t *testing.T) {
	writer := &typedAssetAuditWriterSpy{}
	sink := NewAssetAuditSink(writer)
	input := backupasset.AuditEventInput{Action: backupasset.AuditActionRepositoryConnect, Outcome: backupasset.AuditOutcomeSuccess, RepositoryID: strings.Repeat("a", 32)}
	if err := sink.Write(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if writer.input.Action != input.Action || writer.input.RepositoryID != input.RepositoryID {
		t.Fatalf("writer input=%+v", writer.input)
	}
}

func TestConnectFeatureDisabledTouchesNoDependencies(t *testing.T) {
	service, err := NewService(Dependencies{Foundation: backupasset.NewFoundationService(repositorySettings{"backup_assets.enabled": "false"})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: 1}, RequestContext{}); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("disabled connect error=%v", err)
	}
}

func TestConnectRejectsArchivedTaskBeforeProviderProbe(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	archivedAt := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("archived_at", archivedAt).Error; err != nil {
		t.Fatal(err)
	}
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("archived Task connect error=%v", err)
	}
	if prober.calls != 0 {
		t.Fatalf("archived Task reached Provider probe %d time(s)", prober.calls)
	}
}

func TestConnectRejectsTaskArchivedDuringProbeWithoutMutation(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	archivedAt := time.Date(2026, 8, 17, 9, 5, 0, 0, time.UTC)
	prober := &scriptedProber{probe: func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("archived_at", archivedAt).Error; err != nil {
			return provider.RepositoryObservation{}, err
		}
		identity, err := provider.DeriveScopedIdentity(binding.IdentitySalt, provider.ScopedIdentityDocument{
			Provider: binding.Provider, TaskID: binding.TaskID, NodeID: binding.NodeID, EndpointFacts: binding.EndpointFacts,
		})
		if err != nil {
			return provider.RepositoryObservation{}, err
		}
		return testObservation(binding.Provider, identity), nil
	}}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)

	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("Task archived during probe connect error=%v", err)
	}
	for modelType := range map[any]struct{}{
		&model.BackupRepository{}: {}, &model.RepositoryAccessBinding{}: {}, &model.TaskRepositoryLink{}: {}, &model.RecoveryPoint{}: {},
	} {
		var count int64
		if err := db.Model(modelType).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%T count=%d want=0 err=%v", modelType, count, err)
		}
	}
}

func TestConnectRejectsTaskLinkDriftDuringProbeWithoutMutation(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	probeCalls := 0
	prober := &scriptedProber{probe: func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		probeCalls++
		if probeCalls == 2 {
			unlinkedAt := time.Date(2026, 8, 17, 9, 7, 0, 0, time.UTC)
			if err := db.Model(&model.TaskRepositoryLink{}).Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).Updates(map[string]any{
				"unlinked_at": unlinkedAt,
				"updated_at":  unlinkedAt,
			}).Error; err != nil {
				return provider.RepositoryObservation{}, err
			}
		}
		identity, err := provider.DeriveScopedIdentity(binding.IdentitySalt, provider.ScopedIdentityDocument{
			Provider: binding.Provider, TaskID: binding.TaskID, NodeID: binding.NodeID, EndpointFacts: binding.EndpointFacts,
		})
		if err != nil {
			return provider.RepositoryObservation{}, err
		}
		return testObservation(binding.Provider, identity), nil
	}}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("Task link changed during probe connect error=%v", err)
	}
	for modelType, want := range map[any]int64{
		&model.BackupRepository{}: 1, &model.RepositoryAccessBinding{}: 1, &model.TaskRepositoryLink{}: 1, &model.RecoveryPoint{}: 1,
	} {
		var count int64
		if err := db.Model(modelType).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%T count=%d want=%d err=%v", modelType, count, want, err)
		}
	}
	var activeLinks int64
	if err := db.Model(&model.TaskRepositoryLink{}).Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).Count(&activeLinks).Error; err != nil || activeLinks != 0 {
		t.Fatalf("active Task links=%d want=0 err=%v", activeLinks, err)
	}
}

func TestConnectRejectsProviderAndNodeDriftDuringProbeWithoutMutation(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*gorm.DB, model.Task) error
	}{
		{
			name: "Task Provider",
			mutate: func(db *gorm.DB, taskEntity model.Task) error {
				return db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("executor_type", "rclone").Error
			},
		},
		{
			name: "Node access lineage",
			mutate: func(db *gorm.DB, taskEntity model.Task) error {
				return db.Model(&model.Node{}).Where("id = ?", taskEntity.NodeID).Update("host", "drifted.example.invalid").Error
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newRepositoryTestDB(t)
			taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
			prober := &scriptedProber{probe: func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
				if err := testCase.mutate(db, taskEntity); err != nil {
					return provider.RepositoryObservation{}, err
				}
				identity, err := provider.DeriveScopedIdentity(binding.IdentitySalt, provider.ScopedIdentityDocument{
					Provider: binding.Provider, TaskID: binding.TaskID, NodeID: binding.NodeID, EndpointFacts: binding.EndpointFacts,
				})
				if err != nil {
					return provider.RepositoryObservation{}, err
				}
				return testObservation(binding.Provider, identity), nil
			}}
			service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)

			if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("lineage drift connect error=%v", err)
			}
			for modelType := range map[any]struct{}{
				&model.BackupRepository{}: {}, &model.RepositoryAccessBinding{}: {}, &model.TaskRepositoryLink{}: {}, &model.RecoveryPoint{}: {},
			} {
				var count int64
				if err := db.Model(modelType).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("%T count=%d want=0 err=%v", modelType, count, err)
				}
			}
		})
	}
}

func TestConnectRejectsRetainedBindingTaskArchivedDuringProbeWithoutMutation(t *testing.T) {
	db := newRepositoryTestDB(t)
	firstTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	secondTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"FAKE_SECOND_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("9", 64)
	probeCalls := 0
	prober := &scriptedProber{probe: func(provider.AccessBinding) (provider.RepositoryObservation, error) {
		probeCalls++
		if probeCalls == 3 {
			archivedAt := time.Date(2026, 8, 17, 9, 15, 0, 0, time.UTC)
			if err := db.Model(&model.Task{}).Where("id = ?", firstTask.ID).Update("archived_at", archivedAt).Error; err != nil {
				return provider.RepositoryObservation{}, err
			}
		}
		return testObservation(backupasset.ProviderRestic, identity), nil
	}}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	first, err := service.Connect(context.Background(), ConnectRequest{TaskID: firstTask.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var beforeRepository model.BackupRepository
	if err := db.First(&beforeRepository, "id = ?", first.Repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	var beforeBinding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", first.Repository.ID, bindingStatusActive).First(&beforeBinding).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("retained binding Task archived during probe connect error=%v", err)
	}
	var afterRepository model.BackupRepository
	if err := db.First(&afterRepository, "id = ?", first.Repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	var afterBinding model.RepositoryAccessBinding
	if err := db.First(&afterBinding, "id = ?", beforeBinding.ID).Error; err != nil {
		t.Fatal(err)
	}
	if afterRepository.CapabilityRevision != beforeRepository.CapabilityRevision || afterRepository.CapabilitiesJSON != beforeRepository.CapabilitiesJSON ||
		!afterRepository.UpdatedAt.Equal(beforeRepository.UpdatedAt) || afterBinding.EncryptedConfig != beforeBinding.EncryptedConfig ||
		afterBinding.ConfigFingerprint != beforeBinding.ConfigFingerprint || !afterBinding.UpdatedAt.Equal(beforeBinding.UpdatedAt) {
		t.Fatalf("last-good facts changed: repository before=%+v after=%+v binding before=%+v after=%+v", beforeRepository, afterRepository, beforeBinding, afterBinding)
	}
}

func TestConnectRejectsRetainedProbeOwnerNodeAndSSHCredentialDriftWithoutMutation(t *testing.T) {
	for _, driftKind := range []string{"node_access", "ssh_private_key"} {
		t.Run(driftKind, func(t *testing.T) {
			db := newRepositoryTestDB(t)
			firstTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
			secondTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"FAKE_SECOND_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
			var keyID uint
			if driftKind == "ssh_private_key" {
				key := model.SSHKey{
					Name: "retained-probe-owner-key", Username: "reader", KeyType: "auto",
					PrivateKey: "FAKE_RETAINED_PROBE_PRIVATE_KEY_FOR_TEST_ONLY", Fingerprint: "SHA256:retained-probe-owner",
				}
				if err := db.Create(&key).Error; err != nil {
					t.Fatal(err)
				}
				keyID = key.ID
				if err := db.Model(&model.Node{}).Where("id = ?", firstTask.NodeID).Updates(map[string]any{
					"auth_type": "key", "ssh_key_id": key.ID,
				}).Error; err != nil {
					t.Fatal(err)
				}
			}

			identity := provider.NativeResticIdentityPrefix + strings.Repeat("8", 64)
			probeCalls := 0
			prober := &scriptedProber{probe: func(provider.AccessBinding) (provider.RepositoryObservation, error) {
				probeCalls++
				if probeCalls == 3 {
					switch driftKind {
					case "node_access":
						if err := db.Model(&model.Node{}).Where("id = ?", firstTask.NodeID).Update("host", "drifted-retained-node.example.invalid").Error; err != nil {
							return provider.RepositoryObservation{}, err
						}
					case "ssh_private_key":
						var key model.SSHKey
						if err := db.First(&key, keyID).Error; err != nil {
							return provider.RepositoryObservation{}, err
						}
						key.PrivateKey = "FAKE_DRIFTED_RETAINED_PRIVATE_KEY_FOR_TEST_ONLY"
						if err := db.Save(&key).Error; err != nil {
							return provider.RepositoryObservation{}, err
						}
					}
				}
				return testObservation(backupasset.ProviderRestic, identity), nil
			}}
			service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
			first, err := service.Connect(context.Background(), ConnectRequest{TaskID: firstTask.ID}, RequestContext{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{}); err != nil {
				t.Fatal(err)
			}

			var beforeRepository model.BackupRepository
			if err := db.First(&beforeRepository, "id = ?", first.Repository.ID).Error; err != nil {
				t.Fatal(err)
			}
			var beforeBinding model.RepositoryAccessBinding
			if err := db.Where("repository_id = ? AND status = ?", first.Repository.ID, bindingStatusActive).First(&beforeBinding).Error; err != nil {
				t.Fatal(err)
			}
			var beforeLinks []model.TaskRepositoryLink
			if err := db.Order("id").Find(&beforeLinks).Error; err != nil {
				t.Fatal(err)
			}
			var beforePoints []model.RecoveryPoint
			if err := db.Order("id").Find(&beforePoints).Error; err != nil {
				t.Fatal(err)
			}

			if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("retained probe-owner %s drift connect error=%v", driftKind, err)
			}
			var afterRepository model.BackupRepository
			if err := db.First(&afterRepository, "id = ?", first.Repository.ID).Error; err != nil {
				t.Fatal(err)
			}
			var afterBinding model.RepositoryAccessBinding
			if err := db.First(&afterBinding, "id = ?", beforeBinding.ID).Error; err != nil {
				t.Fatal(err)
			}
			var afterLinks []model.TaskRepositoryLink
			if err := db.Order("id").Find(&afterLinks).Error; err != nil {
				t.Fatal(err)
			}
			var afterPoints []model.RecoveryPoint
			if err := db.Order("id").Find(&afterPoints).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(afterRepository, beforeRepository) || !reflect.DeepEqual(afterBinding, beforeBinding) ||
				!reflect.DeepEqual(afterLinks, beforeLinks) || !reflect.DeepEqual(afterPoints, beforePoints) {
				t.Fatal("repository graph facts changed after retained probe-owner lineage conflict")
			}
		})
	}
}

func TestConnectRetainedProbeOwnerAllowsSSHUsageMetadataAdvance(t *testing.T) {
	db := newRepositoryTestDB(t)
	firstTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	secondTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"FAKE_SECOND_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	key := model.SSHKey{
		Name: "retained-probe-usage-key", Username: "reader", KeyType: "auto",
		PrivateKey: "FAKE_RETAINED_USAGE_PRIVATE_KEY_FOR_TEST_ONLY", Fingerprint: "SHA256:retained-probe-usage",
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Node{}).Where("id = ?", firstTask.NodeID).Updates(map[string]any{
		"auth_type": "key", "ssh_key_id": key.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	usedAt := time.Date(2026, 8, 17, 9, 28, 0, 0, time.UTC)
	probeCalls := 0
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("6", 64)
	prober := &scriptedProber{probe: func(provider.AccessBinding) (provider.RepositoryObservation, error) {
		probeCalls++
		if probeCalls == 3 {
			if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).Update("last_used_at", usedAt).Error; err != nil {
				return provider.RepositoryObservation{}, err
			}
		}
		return testObservation(backupasset.ProviderRestic, identity), nil
	}}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: firstTask.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{}); err != nil {
		t.Fatalf("SSH usage metadata advance blocked retained reconnect: %v", err)
	}
	var persisted model.SSHKey
	if err := db.First(&persisted, key.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.LastUsedAt == nil || !persisted.LastUsedAt.Equal(usedAt) {
		t.Fatalf("SSH usage metadata=%v want=%v", persisted.LastUsedAt, usedAt)
	}
}

func TestConnectRejectsActiveBindingReplacementDuringRetainedProbeWithoutMutation(t *testing.T) {
	db := newRepositoryTestDB(t)
	firstTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	secondTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"FAKE_REPLACEMENT_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("4", 64)
	replacementService := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, &scriptedProber{
		observation: testObservation(backupasset.ProviderRestic, identity),
	})
	probeCalls := 0
	repositoryID := ""
	var replacementRepository model.BackupRepository
	var replacementBindings []model.RepositoryAccessBinding
	var replacementLinks []model.TaskRepositoryLink
	var replacementPoints []model.RecoveryPoint
	prober := &scriptedProber{probe: func(provider.AccessBinding) (provider.RepositoryObservation, error) {
		probeCalls++
		if probeCalls == 3 {
			if _, err := replacementService.Connect(context.Background(), ConnectRequest{
				TaskID: secondTask.ID, RepositoryID: repositoryID, ReplaceAccess: true,
			}, RequestContext{}); err != nil {
				return provider.RepositoryObservation{}, err
			}
			if err := db.First(&replacementRepository, "id = ?", repositoryID).Error; err != nil {
				return provider.RepositoryObservation{}, err
			}
			if err := db.Order("id").Find(&replacementBindings).Error; err != nil {
				return provider.RepositoryObservation{}, err
			}
			if err := db.Order("id").Find(&replacementLinks).Error; err != nil {
				return provider.RepositoryObservation{}, err
			}
			if err := db.Order("id").Find(&replacementPoints).Error; err != nil {
				return provider.RepositoryObservation{}, err
			}
		}
		return testObservation(backupasset.ProviderRestic, identity), nil
	}}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	first, err := service.Connect(context.Background(), ConnectRequest{TaskID: firstTask.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	repositoryID = first.Repository.ID
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("active binding replaced during retained probe connect error=%v", err)
	}
	var afterRepository model.BackupRepository
	if err := db.First(&afterRepository, "id = ?", repositoryID).Error; err != nil {
		t.Fatal(err)
	}
	var afterBindings []model.RepositoryAccessBinding
	if err := db.Order("id").Find(&afterBindings).Error; err != nil {
		t.Fatal(err)
	}
	var afterLinks []model.TaskRepositoryLink
	if err := db.Order("id").Find(&afterLinks).Error; err != nil {
		t.Fatal(err)
	}
	var afterPoints []model.RecoveryPoint
	if err := db.Order("id").Find(&afterPoints).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterRepository, replacementRepository) || !reflect.DeepEqual(afterBindings, replacementBindings) ||
		!reflect.DeepEqual(afterLinks, replacementLinks) || !reflect.DeepEqual(afterPoints, replacementPoints) {
		t.Fatal("stale retained reconnect changed independently committed replacement facts")
	}
	var active model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", repositoryID, bindingStatusActive).First(&active).Error; err != nil {
		t.Fatal(err)
	}
	document, err := decodeBindingDocument(active.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if document.TaskID != secondTask.ID {
		t.Fatal("independently committed replacement binding was not preserved")
	}
}

func TestConnectRsyncIsProbeFirstIdempotentAndEncryptedAtRest(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	first, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID, DisplayName: "backup"}, RequestContext{CorrelationID: "corr-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID, DisplayName: "ignored"}, RequestContext{CorrelationID: "corr-2"})
	if err != nil || second.Repository.ID != first.Repository.ID || second.MutablePoint == nil || first.MutablePoint == nil || second.MutablePoint.ID != first.MutablePoint.ID {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	if prober.calls != 2 {
		t.Fatalf("probe calls=%d", prober.calls)
	}
	for modelType, want := range map[any]int64{&model.BackupRepository{}: 1, &model.RepositoryAccessBinding{}: 1, &model.TaskRepositoryLink{}: 1, &model.RecoveryPoint{}: 1} {
		var count int64
		if err := db.Model(modelType).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%T count=%d want=%d err=%v", modelType, count, want, err)
		}
	}
	var encrypted string
	if err := db.Raw("SELECT encrypted_config FROM repository_access_bindings LIMIT 1").Scan(&encrypted).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encrypted, "enc:v2:") || strings.Contains(encrypted, taskEntity.RsyncTarget) {
		t.Fatalf("binding not encrypted at rest: %q", encrypted)
	}
}

func TestLifecycleReconnectRetiredHeadDoesNotReactivate(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
	settings := completeRepositoryFoundationSettings(true)
	settings["backup_assets.retention_reconcile_interval"] = "5m"
	settings["backup_assets.retention_batch_size"] = "100"
	settings["backup_assets.retention_drain_timeout"] = "30s"
	service.foundation = backupasset.NewFoundationService(settings)
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || connected.MutablePoint == nil {
		t.Fatalf("connected=%+v err=%v", connected, err)
	}
	if _, err := service.Disconnect(context.Background(), connected.Repository.ID, RequestContext{}); err != nil {
		t.Fatal(err)
	}

	retiredAt := time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC)
	retirementReason := backupasset.RetirementWithdrawn
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", connected.MutablePoint.ID).Updates(map[string]any{
		"state":             backupasset.RecoveryPointRetired,
		"retired_at":        retiredAt,
		"retirement_reason": retirementReason,
	}).Error; err != nil {
		t.Fatal(err)
	}
	var before model.RecoveryPoint
	if err := db.First(&before, "id = ?", connected.MutablePoint.ID).Error; err != nil {
		t.Fatal(err)
	}

	reconnected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatalf("reconnect retired head: %v", err)
	}
	if reconnected.Repository.Status != backupasset.RepositoryOnline || reconnected.MutablePoint != nil {
		t.Fatalf("reconnected=%+v", reconnected)
	}
	var after model.RecoveryPoint
	if err := db.First(&after, "id = ?", connected.MutablePoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.State != string(backupasset.RecoveryPointRetired) || after.RetiredAt == nil || !after.RetiredAt.Equal(retiredAt) ||
		after.RetirementReason == nil || *after.RetirementReason != string(retirementReason) || after.SourceFingerprint != before.SourceFingerprint ||
		after.ObservedAt == nil || before.ObservedAt == nil || !after.ObservedAt.Equal(*before.ObservedAt) ||
		after.EncryptedProviderLocator != before.EncryptedProviderLocator {
		t.Fatalf("retired point before=%+v after=%+v", before, after)
	}
	var pointCount int64
	if err := db.Model(&model.RecoveryPoint{}).Where("repository_id = ?", connected.Repository.ID).Count(&pointCount).Error; err != nil || pointCount != 1 {
		t.Fatalf("recovery point count=%d err=%v", pointCount, err)
	}
}

func TestConnectPersistsNativeSnapshotModeForRestic(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "restic", "/backup/repository", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, provider.NativeResticIdentityPrefix+strings.Repeat("a", 64))}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatalf("connect Restic repository: %v", err)
	}
	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.PublicationMode != string(backupasset.PublicationNativeSnapshot) {
		t.Fatalf("publication mode = %q", link.PublicationMode)
	}
}

func TestConnectWithoutReplacementProbesAndRefreshesRetainedBinding(t *testing.T) {
	db := newRepositoryTestDB(t)
	oldSecret := "FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"
	newSecret := "FAKE_NEW_RESTIC_PASSWORD_FOR_TEST_ONLY"
	taskEntity := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"`+oldSecret+`"}`)
	nativeIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("a", 64)
	revision := "restic-reader:v1"
	prober := &scriptedProber{probe: func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		if string(binding.Secret) != oldSecret {
			t.Fatalf("connect without replace probed unretained access secret")
		}
		observation := testObservation(backupasset.ProviderRestic, nativeIdentity)
		observation.AdapterRevision = revision
		return observation, nil
	}}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	first, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("executor_config", `{"repository_password":"`+newSecret+`"}`).Error; err != nil {
		t.Fatal(err)
	}
	revision = "restic-reader:v2"
	second, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Repository.CapabilityRevision != first.Repository.CapabilityRevision+1 {
		t.Fatalf("adapter revision did not advance capability revision: first=%d second=%d", first.Repository.CapabilityRevision, second.Repository.CapabilityRevision)
	}
	var binding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", first.Repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	document, err := decodeBindingDocument(binding.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if document.Secret != oldSecret || document.AdapterRevision != revision {
		t.Fatalf("retained binding was replaced or observation metadata stayed stale: %+v", document)
	}
}

func TestConnectProbeFailureWritesNothing(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := &scriptedProber{err: errors.New("provider unavailable")}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err == nil {
		t.Fatal("probe failure accepted")
	}
	var count int64
	if err := db.Model(&model.BackupRepository{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("repository count=%d err=%v", count, err)
	}
}

func TestConnectRejectsScopedIdentityThatDoesNotMatchBinding(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRsync, provider.ScopedIdentityPrefix(backupasset.ProviderRsync)+strings.Repeat("d", 64))}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("mismatched scoped identity error=%v", err)
	}
	var count int64
	if err := db.Model(&model.BackupRepository{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("repository count=%d err=%v", count, err)
	}
}

func TestConnectSameTaskDifferentIdentityConflictsWithoutMutation(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	first, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	newTarget := t.TempDir()
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("rsync_target", newTarget).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("identity conflict error=%v", err)
	}
	var repository model.BackupRepository
	if err := db.First(&repository, "id = ?", first.Repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	if repository.RepositoryIdentity == nil || !strings.HasPrefix(*repository.RepositoryIdentity, provider.ScopedIdentityPrefix(backupasset.ProviderRsync)) {
		t.Fatalf("identity changed: %+v", repository)
	}
}

func TestConnectRequiresDedicatedActivationForManagedRsyncBinding(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}

	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	document := managedRsyncBindingDocumentV2{
		Version:                   managedRsyncBindingDocumentVersion,
		Provider:                  backupasset.ProviderRsync,
		IdentityClass:             provider.IdentityXirangManagedRepository,
		TaskID:                    taskEntity.ID,
		NodeID:                    taskEntity.NodeID,
		RepositoryID:              connected.Repository.ID,
		TaskRepositoryLinkID:      link.ID,
		LayoutRevision:            managedRsyncLayoutRevisionV1,
		ManagedRootLocator:        t.TempDir(),
		RootMarkerDigest:          strings.Repeat("a", 64),
		ManagedRootIdentityDigest: strings.Repeat("b", 64),
		PublicationMode:           backupasset.PublicationVersionedFullCopy,
		PreflightID:               strings.Repeat("c", 32),
		PreflightDigest:           strings.Repeat("d", 64),
		IdentitySalt:              strings.Repeat("42", provider.IdentitySaltBytes),
	}
	payload, err := encodeManagedRsyncBindingDocumentV2(document)
	if err != nil {
		t.Fatal(err)
	}
	var binding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", connected.Repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	binding.EncryptedConfig = payload
	if err := db.Save(&binding).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("normal connect with managed binding error=%v, want conflict", err)
	}
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.PublicationMode != string(backupasset.PublicationLegacyMutable) || link.EncryptedLegacyLocator != taskEntity.RsyncTarget {
		t.Fatalf("normal connect mutated legacy link: %+v", link)
	}
}

func TestPrepareManagedRsyncActivationRequiresRevisionPreflightAndFence(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	var task model.Task
	if err := db.First(&task, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", task.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	document := managedRsyncBindingDocumentV2{
		Version:                   managedRsyncBindingDocumentVersion,
		Provider:                  backupasset.ProviderRsync,
		IdentityClass:             provider.IdentityXirangManagedRepository,
		TaskID:                    task.ID,
		NodeID:                    task.NodeID,
		RepositoryID:              connected.Repository.ID,
		TaskRepositoryLinkID:      link.ID,
		LayoutRevision:            managedRsyncLayoutRevisionV1,
		ManagedRootLocator:        t.TempDir(),
		RootMarkerDigest:          strings.Repeat("a", 64),
		ManagedRootIdentityDigest: strings.Repeat("b", 64),
		PublicationMode:           backupasset.PublicationVersionedFullCopy,
		PreflightID:               strings.Repeat("c", 32),
		PreflightDigest:           strings.Repeat("d", 64),
		IdentitySalt:              strings.Repeat("42", provider.IdentitySaltBytes),
	}
	revision, err := managedRsyncTaskRevision(task)
	if err != nil {
		t.Fatal(err)
	}
	request := managedRsyncActivationRequest{
		TaskID:                  task.ID,
		ExpectedTaskRevision:    revision,
		PreflightID:             strings.Repeat("c", 32),
		PreflightIdentityDigest: document.RootMarkerDigest,
		PreflightFenceDigest:    strings.Repeat("d", 64),
		Binding:                 document,
	}
	plan, err := service.prepareManagedRsyncActivation(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Task.ID != task.ID || plan.Repository.ID != connected.Repository.ID || plan.Link.ID != link.ID || plan.Binding != document {
		t.Fatalf("activation plan=%+v", plan)
	}

	tests := []struct {
		name   string
		mutate func(*managedRsyncActivationRequest)
	}{
		{"revision", func(value *managedRsyncActivationRequest) { value.ExpectedTaskRevision++ }},
		{"preflight identity", func(value *managedRsyncActivationRequest) { value.PreflightIdentityDigest = strings.Repeat("e", 64) }},
		{"preflight fence", func(value *managedRsyncActivationRequest) { value.PreflightFenceDigest = "not-a-digest" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := request
			tt.mutate(&candidate)
			if _, err := service.prepareManagedRsyncActivation(context.Background(), candidate); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("activation gate error=%v, want conflict", err)
			}
		})
	}
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", task.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.PublicationMode != string(backupasset.PublicationLegacyMutable) || link.EncryptedLegacyLocator != task.RsyncTarget {
		t.Fatalf("activation validation mutated legacy link: %+v", link)
	}
}

func TestConnectSharedResticIdentityReusesRepositoryWithoutLineageExpansion(t *testing.T) {
	db := newRepositoryTestDB(t)
	firstTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-a", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	secondTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-b", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := "restic-native:v1:" + strings.Repeat("c", 64)
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, identity)}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	first, err := service.Connect(context.Background(), ConnectRequest{TaskID: firstTask.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{})
	if err != nil || first.Repository.ID != second.Repository.ID || first.MutablePoint != nil || second.MutablePoint != nil {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	var repositories, bindings, links, points int64
	db.Model(&model.BackupRepository{}).Count(&repositories)
	db.Model(&model.RepositoryAccessBinding{}).Count(&bindings)
	db.Model(&model.TaskRepositoryLink{}).Count(&links)
	db.Model(&model.RecoveryPoint{}).Count(&points)
	if repositories != 1 || bindings != 1 || links != 2 || points != 0 {
		t.Fatalf("repositories=%d bindings=%d links=%d points=%d", repositories, bindings, links, points)
	}
	var binding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", first.Repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	document, err := decodeBindingDocument(binding.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if document.AdapterRevision != "test-reader:v1" || document.NativeRepositoryID != strings.Repeat("c", 64) {
		t.Fatalf("Restic binding facts=%+v", document)
	}
}

func TestConnectSharedResticRejectsArchivedRetainedBindingWithoutReplacement(t *testing.T) {
	db := newRepositoryTestDB(t)
	firstTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-a", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	secondTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-b", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("8", 64)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, &scriptedProber{observation: testObservation(backupasset.ProviderRestic, identity)})
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: firstTask.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	archivedAt := time.Date(2026, 7, 13, 15, 0, 0, 0, time.UTC)
	if err := db.Model(&model.Task{}).Where("id = ?", firstTask.ID).Update("archived_at", archivedAt).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("connect with archived retained binding error=%v", err)
	}
	var links int64
	if err := db.Model(&model.TaskRepositoryLink{}).Where("repository_id = ?", connected.Repository.ID).Count(&links).Error; err != nil || links != 1 {
		t.Fatalf("links=%d err=%v", links, err)
	}
}

func TestConnectRejectsRetainedBindingAfterTaskProviderDrift(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("9", 64)
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, identity)}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("executor_type", "rclone").Error; err != nil {
		t.Fatal(err)
	}

	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("connect after Task Provider drift error=%v", err)
	}
	if prober.calls != 1 {
		t.Fatalf("Provider drift reached retained probe: calls=%d", prober.calls)
	}
}

func TestConnectSharedResticRejectsProviderDriftedRetainedBinding(t *testing.T) {
	db := newRepositoryTestDB(t)
	firstTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-a", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	secondTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-b", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("7", 64)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, &scriptedProber{observation: testObservation(backupasset.ProviderRestic, identity)})
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: firstTask.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", firstTask.ID).Update("executor_type", "rclone").Error; err != nil {
		t.Fatal(err)
	}

	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("shared connect with Provider-drifted retained binding error=%v", err)
	}
	var links int64
	if err := db.Model(&model.TaskRepositoryLink{}).Where("repository_id = ?", connected.Repository.ID).Count(&links).Error; err != nil || links != 1 {
		t.Fatalf("links=%d err=%v", links, err)
	}
}

func TestConnectSharedResticRejectsNodeDriftedRetainedBinding(t *testing.T) {
	db := newRepositoryTestDB(t)
	firstTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-a", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	secondTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-b", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("5", 64)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, &scriptedProber{observation: testObservation(backupasset.ProviderRestic, identity)})
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: firstTask.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", firstTask.ID).Update("node_id", secondTask.NodeID).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("shared connect with Node-drifted retained binding error=%v", err)
	}
	var links int64
	if err := db.Model(&model.TaskRepositoryLink{}).Where("repository_id = ?", connected.Repository.ID).Count(&links).Error; err != nil || links != 1 {
		t.Fatalf("links=%d err=%v", links, err)
	}
}

func TestConnectPropagatesCorrelationIntoRemoteCredentialAuditContext(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	prober := &scriptedProber{probe: func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		runtimeAccess, ok := binding.AdapterData.(provider.ResticRuntimeAccess)
		if !ok || runtimeAccess.Command == nil {
			t.Fatalf("missing remote command runtime: %+v", binding.AdapterData)
		}
		audit := runtimeAccess.Command.Audit
		if audit.CorrelationID != "corr-remote" || audit.UserID != 42 || audit.Username != "admin-user" || audit.Role != "admin" || audit.TaskID == nil || *audit.TaskID != taskEntity.ID || audit.Action != "" {
			t.Fatalf("remote audit context=%+v", audit)
		}
		return testObservation(backupasset.ProviderRestic, provider.NativeResticIdentityPrefix+strings.Repeat("7", 64)), nil
	}}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	_, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{
		CorrelationID: "corr-remote", Actor: backupasset.AuditActor{UserID: 42, Username: "admin-user", Role: "admin"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConnectMutableTasksNeverMergeEvenWithMatchingEndpoint(t *testing.T) {
	db := newRepositoryTestDB(t)
	target := t.TempDir()
	firstTask := seedTask(t, db, "rsync", target, "")
	secondTask := seedTask(t, db, "rsync", target, "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	first, err := service.Connect(context.Background(), ConnectRequest{TaskID: firstTask.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Repository.ID == second.Repository.ID || first.MutablePoint == nil || second.MutablePoint == nil || first.MutablePoint.ID == second.MutablePoint.ID {
		t.Fatalf("mutable Tasks merged: first=%+v second=%+v", first, second)
	}
	var repositories, points int64
	if err := db.Model(&model.BackupRepository{}).Count(&repositories).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.RecoveryPoint{}).Count(&points).Error; err != nil {
		t.Fatal(err)
	}
	if repositories != 2 || points != 2 {
		t.Fatalf("repositories=%d points=%d", repositories, points)
	}
}

func TestConnectReplacesOnlyExplicitlyTargetedAccessBinding(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	first, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	var original model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", first.Repository.ID, bindingStatusActive).First(&original).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID, RepositoryID: first.Repository.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var beforeReplace int64
	if err := db.Model(&model.RepositoryAccessBinding{}).Where("repository_id = ?", first.Repository.ID).Count(&beforeReplace).Error; err != nil || beforeReplace != 1 {
		t.Fatalf("bindings before replace=%d err=%v", beforeReplace, err)
	}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID, RepositoryID: first.Repository.ID, ReplaceAccess: true}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var bindings []model.RepositoryAccessBinding
	if err := db.Where("repository_id = ?", first.Repository.ID).Order("created_at ASC").Find(&bindings).Error; err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 || bindings[0].ID != original.ID || bindings[0].Status != bindingStatusRevoked || bindings[0].RevokedAt == nil || bindings[1].Status != bindingStatusActive || bindings[1].ID == original.ID {
		t.Fatalf("bindings after replace=%+v", bindings)
	}
}

func TestConnectRollsBackEveryRowWhenLinkInsertFails(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	injected := errors.New("injected link insert failure")
	if err := db.Callback().Create().Before("gorm:create").Register("test:fail_repository_link", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.TaskRepositoryLink{}).TableName() {
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); !errors.Is(err, injected) {
		t.Fatalf("connect error=%v", err)
	}
	for modelType := range map[any]struct{}{
		&model.BackupRepository{}: {}, &model.RepositoryAccessBinding{}: {}, &model.TaskRepositoryLink{}: {}, &model.RecoveryPoint{}: {},
	} {
		var count int64
		if err := db.Model(modelType).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%T count=%d err=%v", modelType, count, err)
		}
	}
}

func TestConnectRetriesKnownUniquenessRaceWithoutReprobing(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	var injected atomic.Bool
	if err := db.Callback().Create().Before("gorm:create").Register("test:inject_repository_identity_race", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.BackupRepository{}).TableName() && injected.CompareAndSwap(false, true) {
			_ = tx.AddError(errors.New("UNIQUE constraint failed: backup_repositories.provider_kind, backup_repositories.repository_identity"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	result, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || result.Repository.ID == "" || result.MutablePoint == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if prober.calls != 1 {
		t.Fatalf("probe calls=%d want=1", prober.calls)
	}
}

func TestConnectConstraintClassifierIsIndexScoped(t *testing.T) {
	for _, message := range []string{
		"UNIQUE constraint failed: backup_repositories.provider_kind, backup_repositories.repository_identity",
		`duplicate key value violates unique constraint "idx_backup_repositories_provider_identity" (SQLSTATE 23505)`,
		"UNIQUE constraint failed: repository_access_bindings.repository_id",
		`duplicate key value violates unique constraint "idx_repository_access_bindings_active" (SQLSTATE 23505)`,
		"UNIQUE constraint failed: task_repository_links.task_id",
		`duplicate key value violates unique constraint "idx_task_repository_links_active_task" (SQLSTATE 23505)`,
		"UNIQUE constraint failed: recovery_points.repository_id",
		`duplicate key value violates unique constraint "idx_recovery_points_mutable_head" (SQLSTATE 23505)`,
	} {
		if !isConnectConstraintConflict(errors.New(message)) {
			t.Fatalf("known connect constraint not classified: %s", message)
		}
	}
	for _, message := range []string{
		"UNIQUE constraint failed: users.email",
		"FOREIGN KEY constraint failed",
		`duplicate key value violates unique constraint "unrelated_index" (SQLSTATE 23505)`,
	} {
		if isConnectConstraintConflict(errors.New(message)) {
			t.Fatalf("unrelated constraint classified: %s", message)
		}
	}
}

func TestConnectClampsRecordLimitToValidMetadataCeiling(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	service.foundation = backupasset.NewFoundationService(repositorySettings{
		"backup_assets.enabled":                       "true",
		"backup_assets.provider_operation_timeout":    "5s",
		"backup_assets.provider_max_concurrency":      "1",
		"backup_assets.provider_metadata_limit_bytes": "65536",
	})
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	if err := prober.limits.Validate(); err != nil || prober.limits.MaxRecordBytes != 65536 {
		t.Fatalf("probe limits=%+v err=%v", prober.limits, err)
	}
}
