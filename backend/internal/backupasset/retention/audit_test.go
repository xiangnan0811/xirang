package retention

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
)

func TestAuditRetentionPrunesEligibleClosedSegmentDetailsAndKeepsCheckpointContinuity(t *testing.T) {
	writer, clock, db := newRetentionAuditHarness(t, backupasset.AuditConfig{SegmentMaxEvents: 1, SegmentMaxAge: 24 * time.Hour})
	if _, err := writer.Write(context.Background(), retentionAuditEventInput()); err != nil {
		t.Fatalf("write first segment: %v", err)
	}
	if _, err := writer.Write(context.Background(), retentionAuditEventInput()); err != nil {
		t.Fatalf("write second segment: %v", err)
	}
	clock.now = clock.now.Add(200 * 24 * time.Hour)

	service, err := NewAuditRetention(AuditRetentionDependencies{
		DB: db, Writer: writer, Now: clock.Now,
		Config: func() (AuditRetentionConfig, error) {
			return AuditRetentionConfig{DetailRetentionDays: 180, CheckpointRetentionDays: 2555}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewAuditRetention: %v", err)
	}
	purged, err := service.PurgeEligibleDetails(context.Background(), 10)
	if err != nil || purged != 1 {
		t.Fatalf("PurgeEligibleDetails=%d err=%v, want 1 eligible closed segment", purged, err)
	}

	var events int64
	if err := db.Model(&model.BackupAssetAuditEvent{}).Where("segment_no = ?", 1).Count(&events).Error; err != nil {
		t.Fatalf("count purged segment events: %v", err)
	}
	var checkpoints []model.BackupAssetAuditCheckpoint
	if err := db.Order("segment_no ASC").Find(&checkpoints).Error; err != nil {
		t.Fatalf("load checkpoints: %v", err)
	}
	if events != 0 || len(checkpoints) != 2 || checkpoints[0].Status != string(backupasset.AuditSegmentDetailsPurged) ||
		checkpoints[0].DetailsPurgedAt == nil || checkpoints[0].CheckpointHash == "" ||
		checkpoints[1].PreviousCheckpointHash != checkpoints[0].CheckpointHash ||
		checkpoints[1].EntryCount == 0 {
		t.Fatalf("checkpoint continuity after purge: events=%d checkpoints=%+v", events, checkpoints)
	}
	if err := writer.Verify(context.Background()); err != nil {
		t.Fatalf("Verify after eligible detail purge: %v", err)
	}
}

func TestAuditRetentionSkipsOpenRecentAndAlreadyPurgedSegments(t *testing.T) {
	writer, clock, db := newRetentionAuditHarness(t, backupasset.AuditConfig{SegmentMaxEvents: 100, SegmentMaxAge: 24 * time.Hour})
	if _, err := writer.Write(context.Background(), retentionAuditEventInput()); err != nil {
		t.Fatalf("write open segment: %v", err)
	}
	service, err := NewAuditRetention(AuditRetentionDependencies{
		DB: db, Writer: writer, Now: clock.Now,
		Config: func() (AuditRetentionConfig, error) {
			return AuditRetentionConfig{DetailRetentionDays: 180, CheckpointRetentionDays: 2555}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	purged, err := service.PurgeEligibleDetails(context.Background(), 10)
	if err != nil || purged != 0 {
		t.Fatalf("open segment purged=%d err=%v, want 0", purged, err)
	}
	var openEvents int64
	if err := db.Model(&model.BackupAssetAuditEvent{}).Count(&openEvents).Error; err != nil || openEvents != 1 {
		t.Fatalf("open segment events=%d err=%v, want retained 1", openEvents, err)
	}
}

func TestAuditRetentionUsesDynamicDetailRetentionDays(t *testing.T) {
	writer, clock, db := newRetentionAuditHarness(t, backupasset.AuditConfig{SegmentMaxEvents: 1, SegmentMaxAge: 24 * time.Hour})
	if _, err := writer.Write(context.Background(), retentionAuditEventInput()); err != nil {
		t.Fatalf("write closed segment: %v", err)
	}
	if _, err := writer.Write(context.Background(), retentionAuditEventInput()); err != nil {
		t.Fatalf("open next segment: %v", err)
	}
	clock.now = clock.now.Add(10 * 24 * time.Hour)
	days := 30
	service, err := NewAuditRetention(AuditRetentionDependencies{
		DB: db, Writer: writer, Now: clock.Now,
		Config: func() (AuditRetentionConfig, error) {
			return AuditRetentionConfig{DetailRetentionDays: days, CheckpointRetentionDays: 2555}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	purged, err := service.PurgeEligibleDetails(context.Background(), 10)
	if err != nil || purged != 0 {
		t.Fatalf("10-day-old segment with 30-day policy purged=%d err=%v, want 0", purged, err)
	}
	days = 5
	purged, err = service.PurgeEligibleDetails(context.Background(), 10)
	if err != nil || purged != 1 {
		t.Fatalf("dynamic 5-day policy purged=%d err=%v, want 1", purged, err)
	}
	if err := writer.Verify(context.Background()); err != nil {
		t.Fatalf("Verify after dynamic purge: %v", err)
	}
}

func TestAuditRetentionNeverDeletesCheckpointsOrIndependentEvidence(t *testing.T) {
	writer, clock, db := newRetentionAuditHarness(t, backupasset.AuditConfig{SegmentMaxEvents: 1, SegmentMaxAge: 24 * time.Hour})
	if _, err := writer.Write(context.Background(), retentionAuditEventInput()); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(context.Background(), retentionAuditEventInput()); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(4000 * 24 * time.Hour)
	service, err := NewAuditRetention(AuditRetentionDependencies{
		DB: db, Writer: writer, Now: clock.Now,
		Config: func() (AuditRetentionConfig, error) {
			return AuditRetentionConfig{DetailRetentionDays: 1, CheckpointRetentionDays: 1}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PurgeEligibleDetails(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	var checkpoints int64
	if err := db.Model(&model.BackupAssetAuditCheckpoint{}).Count(&checkpoints).Error; err != nil || checkpoints != 2 {
		t.Fatalf("checkpoints=%d err=%v, want both retained", checkpoints, err)
	}
	if err := writer.Verify(context.Background()); err != nil {
		t.Fatalf("Verify after checkpoint-retention setting: %v", err)
	}
}

func TestSettledAuditMissingCandidateLeaseFailsClosed(t *testing.T) {
	type durableState struct {
		attempt        model.RecoveryPointLifecycleAttempt
		point          model.RecoveryPoint
		claim          model.RecoveryPointLifecycleEffectClaim
		claimFound     bool
		tombstone      model.RecoveryPointLifecycleTombstone
		tombstoneFound bool
		lease          model.RecoveryPointLease
		leaseFound     bool
	}
	loadState := func(t *testing.T, fixture *claimedExpiryFixture) durableState {
		t.Helper()
		var state durableState
		if err := fixture.db.First(&state.attempt, "id = ?", fixture.attempt.ID).Error; err != nil {
			t.Fatalf("load settled-audit attempt: %v", err)
		}
		if err := fixture.db.First(&state.point, "id = ?", fixture.pointID).Error; err != nil {
			t.Fatalf("load settled-audit point: %v", err)
		}
		loaded := fixture.db.Where("attempt_id = ?", fixture.attempt.ID).Limit(1).Find(&state.claim)
		if loaded.Error != nil {
			t.Fatalf("load settled-audit claim: %v", loaded.Error)
		}
		state.claimFound = loaded.RowsAffected == 1
		loaded = fixture.db.Where("recovery_point_id = ? AND terminal_operation = ?",
			fixture.pointID, state.attempt.Operation).Limit(1).Find(&state.tombstone)
		if loaded.Error != nil {
			t.Fatalf("load settled-audit tombstone: %v", loaded.Error)
		}
		state.tombstoneFound = loaded.RowsAffected == 1
		if state.attempt.LeaseID != nil {
			loaded = fixture.db.Where("id = ?", *state.attempt.LeaseID).Limit(1).Find(&state.lease)
			if loaded.Error != nil {
				t.Fatalf("load settled-audit lease: %v", loaded.Error)
			}
			state.leaseFound = loaded.RowsAffected == 1
		}
		return state
	}
	countSlots := func(t *testing.T, fixture *claimedExpiryFixture) int64 {
		t.Helper()
		var count int64
		if err := fixture.db.Model(&model.RecoveryPointLifecycleAuditSlot{}).
			Where("attempt_id = ?", fixture.attempt.ID).Count(&count).Error; err != nil {
			t.Fatalf("count settled-audit slots: %v", err)
		}
		return count
	}
	removeCandidateLease := func(t *testing.T, fixture *claimedExpiryFixture) {
		t.Helper()
		leaseID := fixture.attempt.LeaseID
		if backupasset.ValidateOpaqueID(leaseID) != nil {
			t.Fatalf("settled-audit fixture lease ID=%q, want valid", leaseID)
		}
		removed := fixture.db.Where("id = ?", leaseID).Delete(&model.RecoveryPointLease{})
		if removed.Error != nil {
			t.Fatalf("remove settled-audit candidate lease: %v", removed.Error)
		}
		if removed.RowsAffected != 1 {
			t.Fatalf("remove settled-audit candidate lease rows=%d, want 1", removed.RowsAffected)
		}
	}
	setRetryAtNil := func(t *testing.T, fixture *claimedExpiryFixture) {
		t.Helper()
		if err := fixture.db.Model(&model.RecoveryPointLifecycleAttempt{}).
			Where("id = ?", fixture.attempt.ID).Update("retry_at", nil).Error; err != nil {
			t.Fatalf("clear settled-audit retry gate: %v", err)
		}
	}

	type candidateCase struct {
		name     string
		terminal bool
		reason   backupasset.LifecycleBlockedReason
		status   string
	}
	candidates := []candidateCase{
		{name: "terminal-proof", terminal: true, status: "deleted"},
		{name: "blocked", reason: backupasset.LifecycleBlockedProviderWORM, status: "blocked"},
		{name: "identity-conflict", reason: backupasset.LifecycleBlockedProviderIdentityConflict, status: "identity_conflict"},
	}
	setupCandidate := func(t *testing.T, base uint64, candidate candidateCase, keepLease bool) *claimedExpiryFixture {
		t.Helper()
		var fixture *claimedExpiryFixture
		if candidate.terminal {
			fixture = seedProviderDeleteProofFirstFixture(t, base, "in_flight")
		} else {
			fixture = newClaimedExpiryFixture(t, base)
			current := fixture.attempt
			for steps := 0; current.Phase != backupasset.LifecyclePhaseProviderDelete && steps < 8; steps++ {
				var err error
				current, err = fixture.coordinator.Advance(context.Background(), current.ID)
				if err != nil {
					t.Fatalf("advance settled-audit %s fixture: %v", candidate.name, err)
				}
			}
			if current.Phase != backupasset.LifecyclePhaseProviderDelete {
				t.Fatalf("settled-audit %s fixture phase=%q, want provider_delete", candidate.name, current.Phase)
			}
			blocked, err := fixture.coordinator.block(context.Background(), current.ID, candidate.reason)
			if err != nil {
				t.Fatalf("block settled-audit %s fixture: %v", candidate.name, err)
			}
			if blocked.Phase != backupasset.LifecyclePhaseBlocked || blocked.BlockedReason != candidate.reason {
				t.Fatalf("settled-audit %s fixture attempt=%+v, want blocked/%q", candidate.name, blocked, candidate.reason)
			}
			fixture.attempt = blocked
		}
		setRetryAtNil(t, fixture)
		if !keepLease {
			removeCandidateLease(t, fixture)
		}
		state := loadState(t, fixture)
		if state.leaseFound != keepLease {
			t.Fatalf("settled-audit %s fixture lease_found=%t, want %t", candidate.name, state.leaseFound, keepLease)
		}
		if candidate.terminal {
			if !state.claimFound || !state.tombstoneFound {
				t.Fatalf("terminal-proof settled-audit fixture claim/tombstone=%t/%t, want present", state.claimFound, state.tombstoneFound)
			}
		} else if state.claimFound || state.tombstoneFound {
			t.Fatalf("observational settled-audit %s fixture claim/tombstone=%t/%t, want absent", candidate.name, state.claimFound, state.tombstoneFound)
		}
		if countSlots(t, fixture) != 0 {
			t.Fatalf("settled-audit %s fixture unexpectedly has settled-audit slots", candidate.name)
		}
		return fixture
	}

	operations := []struct{ name string }{{name: "emit"}, {name: "schedule"}}
	for candidateIndex, candidate := range candidates {
		candidate := candidate
		for operationIndex, operation := range operations {
			operation := operation

			t.Run("positive control/"+candidate.name+"/"+operation.name, func(t *testing.T) {
				base := uint64(7600 + candidateIndex*20 + operationIndex)
				fixture := setupCandidate(t, base, candidate, true)
				before := loadState(t, fixture)
				audit := &recordingSettledAudit{}
				fixture.coordinator.audit = audit

				switch operation.name {
				case "emit":
					result, err := fixture.coordinator.emitSettledDeletionAuditTx(context.Background(), fixture.attempt.ID)
					wantResult := settledAuditEmitted
					if candidate.terminal {
						wantResult = settledAuditTerminalEmitted
					}
					if err != nil || result != wantResult || len(audit.events) != 1 ||
						countSlots(t, fixture) != 1 ||
						audit.events[0].Fields[backupasset.AuditFieldStatus] != candidate.status {
						t.Fatalf("valid %s settled-audit candidate result=%d error=%v writes=%d slots=%d events=%+v",
							candidate.name, result, err, len(audit.events), countSlots(t, fixture), audit.events)
					}
				case "schedule":
					scheduled, err := fixture.coordinator.scheduleSettledAuditRetry(context.Background(), fixture.attempt.ID)
					if err != nil || scheduled.RetryAt == nil || !scheduled.RetryAt.After(fixture.clock) ||
						scheduled.TransitionRevision != before.attempt.TransitionRevision ||
						len(audit.events) != 0 || countSlots(t, fixture) != 0 {
						t.Fatalf("valid %s settled-audit schedule attempt=%+v error=%v writes=%d slots=%d",
							candidate.name, scheduled, err, len(audit.events), countSlots(t, fixture))
					}
					after := loadState(t, fixture)
					if after.attempt.RetryAt == nil || !after.attempt.RetryAt.After(fixture.clock) ||
						after.attempt.TransitionRevision != before.attempt.TransitionRevision {
						t.Fatalf("valid %s settled-audit scheduled durable attempt=%+v, want future retry with unchanged revision",
							candidate.name, after.attempt)
					}
				}
			})
		}
	}

	for candidateIndex, candidate := range candidates {
		candidate := candidate
		for operationIndex, operation := range operations {
			operation := operation
			t.Run("missing lease/"+candidate.name+"/"+operation.name, func(t *testing.T) {
				base := uint64(7800 + candidateIndex*20 + operationIndex)
				fixture := setupCandidate(t, base, candidate, false)
				before := loadState(t, fixture)
				if before.leaseFound {
					t.Fatal("missing-lease fixture unexpectedly retained its referenced lease")
				}
				audit := &recordingSettledAudit{}
				fixture.coordinator.audit = audit

				var err error
				switch operation.name {
				case "emit":
					_, err = fixture.coordinator.emitSettledDeletionAuditTx(context.Background(), fixture.attempt.ID)
				case "schedule":
					_, err = fixture.coordinator.scheduleSettledAuditRetry(context.Background(), fixture.attempt.ID)
				}
				if err == nil || !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
					t.Fatalf("missing candidate lease %s/%s error=%v, want typed identity conflict",
						candidate.name, operation.name, err)
				}
				after := loadState(t, fixture)
				claimChanged := before.claimFound != after.claimFound ||
					(before.claimFound && !reflect.DeepEqual(before.claim, after.claim))
				tombstoneChanged := before.tombstoneFound != after.tombstoneFound ||
					(before.tombstoneFound && !reflect.DeepEqual(before.tombstone, after.tombstone))
				leaseChanged := before.leaseFound != after.leaseFound ||
					(before.leaseFound && !reflect.DeepEqual(before.lease, after.lease))
				if !reflect.DeepEqual(before.attempt, after.attempt) ||
					!reflect.DeepEqual(before.point, after.point) ||
					claimChanged || tombstoneChanged || leaseChanged ||
					len(audit.events) != 0 || countSlots(t, fixture) != 0 ||
					fixture.deleter.prepareCalls != 0 || fixture.deleter.calls != 0 ||
					fixture.deleter.verifyCalls != 0 {
					t.Fatalf("missing candidate lease %s/%s mutated durable state or performed work: attempt=%t point=%t claim=%t tombstone=%t lease=%t writes=%d slots=%d provider_prepare=%d provider_execute=%d provider_verify=%d",
						candidate.name, operation.name,
						!reflect.DeepEqual(before.attempt, after.attempt),
						!reflect.DeepEqual(before.point, after.point),
						claimChanged, tombstoneChanged, leaseChanged,
						len(audit.events), countSlots(t, fixture),
						fixture.deleter.prepareCalls, fixture.deleter.calls, fixture.deleter.verifyCalls)
				}
			})
		}
	}
}

func TestSettledAuditDuplicateValidationScansLaterSlots(t *testing.T) {
	tests := []struct {
		name       string
		laterID    func(uint64) string
		laterState string
	}{
		{
			name:       "malformed-later-slot",
			laterID:    func(uint64) string { return "malformed-settled-slot" },
			laterState: "blocked",
		},
		{
			name:       "conflicting-later-terminal",
			laterID:    func(base uint64) string { return testOpaqueID(base + 11) },
			laterState: "deleted",
		},
	}
	for index, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			base := uint64(7000 + index*20)
			fixture := newClaimedExpiryFixture(t, base)
			current := fixture.attempt
			for steps := 0; current.Phase != backupasset.LifecyclePhaseProviderDelete && steps < 8; steps++ {
				var err error
				current, err = fixture.coordinator.Advance(context.Background(), current.ID)
				if err != nil {
					t.Fatalf("advance settled-audit fixture: %v", err)
				}
			}
			if current.Phase != backupasset.LifecyclePhaseProviderDelete {
				t.Fatalf("settled-audit fixture phase=%q, want provider_delete", current.Phase)
			}
			blocked, err := fixture.coordinator.block(
				context.Background(), current.ID, backupasset.LifecycleBlockedProviderWORM,
			)
			if err != nil {
				t.Fatalf("block settled-audit fixture: %v", err)
			}
			if blocked.RetryAt == nil {
				t.Fatal("blocked settled-audit fixture has no retry_at")
			}

			matching := model.RecoveryPointLifecycleAuditSlot{
				ID: testOpaqueID(base + 10), AttemptID: blocked.ID, Status: "blocked",
				EmittedAt: fixture.clock, CreatedAt: fixture.clock,
			}
			later := model.RecoveryPointLifecycleAuditSlot{
				ID: test.laterID(base), AttemptID: blocked.ID, Status: test.laterState,
				EmittedAt: fixture.clock.Add(time.Second), CreatedAt: fixture.clock.Add(time.Second),
			}
			if matching.ID >= later.ID {
				t.Fatalf("test slots are not ordered by matching-then-later ID: %q >= %q", matching.ID, later.ID)
			}
			if err := fixture.db.Create(&[]model.RecoveryPointLifecycleAuditSlot{matching, later}).Error; err != nil {
				t.Fatalf("seed settled-audit slots: %v", err)
			}

			audit := &recordingSettledAudit{}
			fixture.coordinator.audit = audit
			beforeRetryAt := blocked.RetryAt.UTC()
			if _, err := fixture.coordinator.emitSettledDeletionAuditTx(context.Background(), blocked.ID); err == nil ||
				!errors.Is(err, backupasset.ErrInvalidState) {
				t.Fatalf("emit duplicate with %s slot error=%v, want invalid state", test.name, err)
			}
			if len(audit.events) != 0 {
				t.Fatalf("emit duplicate with %s slot wrote %d audit events, want zero", test.name, len(audit.events))
			}

			if _, err := fixture.coordinator.scheduleSettledAuditRetry(context.Background(), blocked.ID); err == nil ||
				!errors.Is(err, backupasset.ErrInvalidState) {
				t.Fatalf("schedule duplicate with %s slot error=%v, want invalid state", test.name, err)
			}
			var persisted model.RecoveryPointLifecycleAttempt
			if err := fixture.db.First(&persisted, "id = ?", blocked.ID).Error; err != nil {
				t.Fatalf("reload settled-audit attempt: %v", err)
			}
			if persisted.RetryAt == nil || !persisted.RetryAt.Equal(beforeRetryAt) {
				t.Fatalf("retry_at after rejected %s slots=%v, want unchanged %s", test.name, persisted.RetryAt, beforeRetryAt)
			}
		})
	}
}

type retentionAuditClock struct{ now time.Time }

func (clock *retentionAuditClock) Now() time.Time { return clock.now }

func newRetentionAuditHarness(t *testing.T, config backupasset.AuditConfig) (*backupasset.AuditWriter, *retentionAuditClock, *gorm.DB) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_AUDIT_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	clock := &retentionAuditClock{now: time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)}
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf(
		"file:%s?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate&_loc=UTC",
		strings.ReplaceAll(t.Name(), "/", "_"),
	)), &gorm.Config{NowFunc: clock.Now, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open audit retention database: %v", err)
	}
	if err := db.AutoMigrate(&model.WrappedDomainKey{}, &model.BackupAssetAuditCheckpoint{}, &model.BackupAssetAuditEvent{}); err != nil {
		t.Fatalf("migrate audit retention tables: %v", err)
	}
	for _, statement := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_wrapped_domain_keys_domain_version ON wrapped_domain_keys(domain, version)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_wrapped_domain_keys_active ON wrapped_domain_keys(domain) WHERE state = 'active'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_asset_audit_events_segment_sequence ON backup_asset_audit_events(segment_no, segment_sequence)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("create audit retention index: %v", err)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	ring := backupasset.NewKeyring(db, clock.Now)
	writer, err := backupasset.NewAuditWriter(db, ring, clock.Now, config)
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	return writer, clock, db
}

func retentionAuditEventInput() backupasset.AuditEventInput {
	return backupasset.AuditEventInput{
		Actor:           backupasset.AuditActor{UserID: 41, Username: "admin", Role: "admin"},
		Action:          backupasset.AuditActionAssetList,
		Outcome:         backupasset.AuditOutcomeSuccess,
		RepositoryID:    strings.Repeat("a", 32),
		RecoveryPointID: strings.Repeat("b", 32),
		EntryID:         strings.Repeat("c", 64),
		ItemCount:       1,
		ByteCount:       128,
		Range:           backupasset.NewRangeSummary(1, 128),
		Fields:          map[backupasset.AuditField]any{backupasset.AuditFieldCorrelationID: "corr_retention"},
	}
}
