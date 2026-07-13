package backupasset

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAuditFingerprintUsesIndependentKeyedDomainAndVersion(t *testing.T) {
	writer, _, db, ring := newAuditTestHarness(t, AuditConfig{})
	ctx := context.Background()
	materials, err := ring.EnsureRequiredDomains(ctx)
	if err != nil {
		t.Fatalf("EnsureRequiredDomains: %v", err)
	}
	rawPath := "/private/customer/report.txt"
	record, err := writer.Write(ctx, standardAuditEventInput(AuditActionAssetList, AuditFingerprintInput{Path: rawPath}))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	auditKey := materials[KeyDomainAuditFingerprint]
	entryKey := materials[KeyDomainEntryIdentity]
	if record.FingerprintKeyVersion == nil || *record.FingerprintKeyVersion != auditKey.Version {
		t.Fatalf("fingerprint key version=%v, want %d", record.FingerprintKeyVersion, auditKey.Version)
	}
	want := auditFingerprintDigest(auditKey.Key, "path", rawPath)
	if record.PathFingerprint != want {
		t.Fatalf("path fingerprint=%q, want keyed digest %q", record.PathFingerprint, want)
	}
	if record.PathFingerprint == auditFingerprintDigest(entryKey.Key, "path", rawPath) {
		t.Fatal("audit fingerprint reused Entry Identity key material")
	}
	bare := sha256.Sum256([]byte(rawPath))
	if record.PathFingerprint == hex.EncodeToString(bare[:]) {
		t.Fatal("audit fingerprint used a bare low-entropy hash")
	}

	var persisted model.BackupAssetAuditEvent
	if err := db.First(&persisted, record.ID).Error; err != nil {
		t.Fatalf("load persisted event: %v", err)
	}
	if strings.Contains(persisted.FieldsJSON+persisted.PathFingerprint, rawPath) {
		t.Fatal("raw path survived persistence")
	}
}

func TestAuditRecordNeverContainsRawPathNameQuerySnippetContentTicketCookieCredentialOrLocator(t *testing.T) {
	writer, _, db, _ := newAuditTestHarness(t, AuditConfig{})
	rawValues := []string{
		"/private/customer/report.txt",
		"quarterly-report.txt",
		"owner=alice",
		"first bytes of report",
		"confidential file body",
		"FAKE_TICKET_FOR_TEST_ONLY",
		"FAKE_COOKIE_FOR_TEST_ONLY",
		"FAKE_JWT_FOR_TEST_ONLY",
		"FAKE_CREDENTIAL_FOR_TEST_ONLY",
		"s3://private-bucket/object",
	}
	fields := SanitizeAuditFields(map[string]any{
		"stage":            "preview_read",
		"path":             rawValues[0],
		"name":             rawValues[1],
		"query":            rawValues[2],
		"snippet":          rawValues[3],
		"content":          rawValues[4],
		"ticket":           rawValues[5],
		"cookie":           rawValues[6],
		"jwt":              rawValues[7],
		"credential":       rawValues[8],
		"provider_locator": rawValues[9],
	})
	input := standardAuditEventInput(AuditActionPreviewRead, AuditFingerprintInput{
		Path:  rawValues[0],
		Query: rawValues[2],
	})
	input.Fields = fields
	record, err := writer.Write(context.Background(), input)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	var persisted model.BackupAssetAuditEvent
	if err := db.First(&persisted, record.ID).Error; err != nil {
		t.Fatalf("load persisted event: %v", err)
	}
	serialized, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal persisted event: %v", err)
	}
	haystack := string(serialized) + persisted.FieldsJSON + persisted.PrevHash + persisted.EntryHash
	for _, raw := range rawValues {
		if strings.Contains(haystack, raw) {
			t.Fatalf("raw audit value %q survived persistence: %s", raw, haystack)
		}
	}
	if persisted.PathFingerprint == "" || persisted.QueryFingerprint == "" {
		t.Fatalf("fingerprints missing: %+v", persisted)
	}
}

func TestAuditWriterMaintainsEntryChain(t *testing.T) {
	writer, _, db, _ := newAuditTestHarness(t, AuditConfig{SegmentMaxEvents: 100, SegmentMaxAge: 24 * time.Hour})
	ctx := context.Background()
	for index := 0; index < 3; index++ {
		input := standardAuditEventInput(AuditActionAssetList, AuditFingerprintInput{})
		input.ItemCount = int64(index + 1)
		if _, err := writer.Write(ctx, input); err != nil {
			t.Fatalf("Write(%d): %v", index, err)
		}
	}
	var events []model.BackupAssetAuditEvent
	if err := db.Order("segment_sequence ASC").Find(&events).Error; err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("event count=%d, want 3", len(events))
	}
	for index, event := range events {
		if event.SegmentNo != 1 || event.SegmentSequence != int64(index+1) {
			t.Fatalf("event sequence drifted: %+v", event)
		}
		wantPrev := ""
		if index > 0 {
			wantPrev = events[index-1].EntryHash
		}
		if event.PrevHash != wantPrev {
			t.Fatalf("event %d prev hash=%q, want %q", index, event.PrevHash, wantPrev)
		}
	}
	var checkpoint model.BackupAssetAuditCheckpoint
	if err := db.First(&checkpoint, "segment_no = ?", 1).Error; err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.EntryCount != 3 || checkpoint.FirstEntryHash != events[0].EntryHash || checkpoint.LastEntryHash != events[2].EntryHash {
		t.Fatalf("checkpoint does not anchor event chain: %+v", checkpoint)
	}
	if err := writer.Verify(ctx); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestAuditWriterClosesSegmentByCountAndAge(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		writer, _, db, _ := newAuditTestHarness(t, AuditConfig{SegmentMaxEvents: 2, SegmentMaxAge: 24 * time.Hour})
		for range 2 {
			if _, err := writer.Write(context.Background(), standardAuditEventInput(AuditActionAssetList, AuditFingerprintInput{})); err != nil {
				t.Fatalf("Write: %v", err)
			}
		}
		var checkpoint model.BackupAssetAuditCheckpoint
		if err := db.First(&checkpoint, "segment_no = ?", 1).Error; err != nil {
			t.Fatalf("load checkpoint: %v", err)
		}
		if checkpoint.Status != string(AuditSegmentClosed) || checkpoint.ClosedAt == nil || checkpoint.CheckpointHash == "" {
			t.Fatalf("count limit did not close segment: %+v", checkpoint)
		}
	})

	t.Run("age", func(t *testing.T) {
		writer, clock, db, _ := newAuditTestHarness(t, AuditConfig{SegmentMaxEvents: 100, SegmentMaxAge: time.Hour})
		if _, err := writer.Write(context.Background(), standardAuditEventInput(AuditActionAssetList, AuditFingerprintInput{})); err != nil {
			t.Fatalf("first Write: %v", err)
		}
		clock.Advance(2 * time.Hour)
		if _, err := writer.Write(context.Background(), standardAuditEventInput(AuditActionAssetSearch, AuditFingerprintInput{})); err != nil {
			t.Fatalf("second Write: %v", err)
		}
		var checkpoints []model.BackupAssetAuditCheckpoint
		if err := db.Order("segment_no ASC").Find(&checkpoints).Error; err != nil {
			t.Fatalf("load checkpoints: %v", err)
		}
		if len(checkpoints) != 2 || checkpoints[0].Status != string(AuditSegmentClosed) || checkpoints[1].Status != string(AuditSegmentOpen) {
			t.Fatalf("age limit did not roll segment: %+v", checkpoints)
		}
	})
}

func TestAuditCheckpointLinksAdjacentSegments(t *testing.T) {
	writer, _, db, _ := newAuditTestHarness(t, AuditConfig{SegmentMaxEvents: 1, SegmentMaxAge: 24 * time.Hour})
	for range 2 {
		if _, err := writer.Write(context.Background(), standardAuditEventInput(AuditActionAssetList, AuditFingerprintInput{})); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	var checkpoints []model.BackupAssetAuditCheckpoint
	if err := db.Order("segment_no ASC").Find(&checkpoints).Error; err != nil {
		t.Fatalf("load checkpoints: %v", err)
	}
	if len(checkpoints) != 2 || checkpoints[0].CheckpointHash == "" || checkpoints[1].PreviousCheckpointHash != checkpoints[0].CheckpointHash {
		t.Fatalf("adjacent checkpoints are not linked: %+v", checkpoints)
	}
	if err := writer.Verify(context.Background()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestAuditDetailPurgeRetainsVerifiableCheckpoint(t *testing.T) {
	writer, _, db, _ := newAuditTestHarness(t, AuditConfig{SegmentMaxEvents: 1, SegmentMaxAge: 24 * time.Hour})
	if _, err := writer.Write(context.Background(), standardAuditEventInput(AuditActionAssetList, AuditFingerprintInput{})); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var before model.BackupAssetAuditCheckpoint
	if err := db.First(&before, "segment_no = ?", 1).Error; err != nil {
		t.Fatalf("load checkpoint before purge: %v", err)
	}
	if err := writer.PurgeSegmentDetails(context.Background(), 1); err != nil {
		t.Fatalf("PurgeSegmentDetails: %v", err)
	}
	var eventCount int64
	if err := db.Model(&model.BackupAssetAuditEvent{}).Where("segment_no = ?", 1).Count(&eventCount).Error; err != nil {
		t.Fatalf("count events: %v", err)
	}
	var after model.BackupAssetAuditCheckpoint
	if err := db.First(&after, "segment_no = ?", 1).Error; err != nil {
		t.Fatalf("load checkpoint after purge: %v", err)
	}
	if eventCount != 0 || after.Status != string(AuditSegmentDetailsPurged) || after.DetailsPurgedAt == nil {
		t.Fatalf("purge state mismatch: count=%d checkpoint=%+v", eventCount, after)
	}
	if after.CheckpointHash != before.CheckpointHash || after.FirstEntryHash != before.FirstEntryHash || after.LastEntryHash != before.LastEntryHash || after.EntryCount != before.EntryCount {
		t.Fatal("detail purge changed retained checkpoint anchor")
	}
	if err := writer.Verify(context.Background()); err != nil {
		t.Fatalf("Verify after purge: %v", err)
	}
}

func TestAuditVerifierDetectsEntryAndCheckpointTamper(t *testing.T) {
	t.Run("entry", func(t *testing.T) {
		writer, _, db, _ := newAuditTestHarness(t, AuditConfig{SegmentMaxEvents: 100, SegmentMaxAge: 24 * time.Hour})
		record, err := writer.Write(context.Background(), standardAuditEventInput(AuditActionAssetList, AuditFingerprintInput{}))
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := db.Model(&model.BackupAssetAuditEvent{}).Where("id = ?", record.ID).Update("byte_count", 99).Error; err != nil {
			t.Fatalf("tamper event: %v", err)
		}
		if err := writer.Verify(context.Background()); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("tampered event got %v, want ErrInvalidState", err)
		}
	})

	t.Run("checkpoint", func(t *testing.T) {
		writer, _, db, _ := newAuditTestHarness(t, AuditConfig{SegmentMaxEvents: 1, SegmentMaxAge: 24 * time.Hour})
		if _, err := writer.Write(context.Background(), standardAuditEventInput(AuditActionAssetList, AuditFingerprintInput{})); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := db.Model(&model.BackupAssetAuditCheckpoint{}).Where("segment_no = ?", 1).Update("checkpoint_hash", strings.Repeat("f", 64)).Error; err != nil {
			t.Fatalf("tamper checkpoint: %v", err)
		}
		if err := writer.Verify(context.Background()); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("tampered checkpoint got %v, want ErrInvalidState", err)
		}
	})
}

func TestAuditConcurrentWritersProduceUniqueSequence(t *testing.T) {
	writer, _, db, _ := newAuditTestHarness(t, AuditConfig{SegmentMaxEvents: 1000, SegmentMaxAge: 24 * time.Hour})
	const writers = 24
	var wg sync.WaitGroup
	errorsCh := make(chan error, writers)
	for index := 0; index < writers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			input := standardAuditEventInput(AuditActionAssetList, AuditFingerprintInput{})
			input.Fields = map[AuditField]any{AuditFieldCorrelationID: fmt.Sprintf("corr_%d", index)}
			if _, err := writer.Write(context.Background(), input); err != nil {
				errorsCh <- err
			}
		}(index)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatalf("concurrent Write: %v", err)
	}
	var events []model.BackupAssetAuditEvent
	if err := db.Order("segment_sequence ASC").Find(&events).Error; err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) != writers {
		t.Fatalf("event count=%d, want %d", len(events), writers)
	}
	for index, event := range events {
		if event.SegmentNo != 1 || event.SegmentSequence != int64(index+1) {
			t.Fatalf("non-unique/non-contiguous sequence at %d: %+v", index, event)
		}
	}
	if err := writer.Verify(context.Background()); err != nil {
		t.Fatalf("Verify concurrent chain: %v", err)
	}
}

func TestAuditConcurrentCheckpointCreationIsRetryable(t *testing.T) {
	writer, clock, db, _ := newAuditTestHarness(t, AuditConfig{SegmentMaxEvents: 100, SegmentMaxAge: 24 * time.Hour})
	if _, err := writer.Write(context.Background(), standardAuditEventInput(AuditActionAssetList, AuditFingerprintInput{})); err != nil {
		t.Fatalf("Write: %v", err)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		_, createErr := createAuditCheckpoint(tx, clock.Now())
		return createErr
	})
	if !errors.Is(err, ErrConflict) || !retryableAuditWrite(err) {
		t.Fatalf("concurrent checkpoint creation got %v, want retryable ErrConflict", err)
	}
}

func TestRangeSummaryIsBounded(t *testing.T) {
	negative := NewRangeSummary(-1, -1)
	if negative.Count != 0 || negative.Bytes != 0 {
		t.Fatalf("negative range summary was not clamped: %+v", negative)
	}
	huge := NewRangeSummary(math.MaxInt64, math.MaxInt64)
	if huge.Count != MaxAuditRangeCount || huge.Bytes != MaxAuditRangeBytes {
		t.Fatalf("huge range summary was not bounded: %+v", huge)
	}
}

type auditTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *auditTestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *auditTestClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func newAuditTestHarness(t *testing.T, config AuditConfig) (*AuditWriter, *auditTestClock, *gorm.DB, *Keyring) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_AUDIT_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	clock := &auditTestClock{now: time.Date(2026, 7, 13, 7, 8, 9, 0, time.UTC)}
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{NowFunc: clock.Now, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open audit database: %v", err)
	}
	if err := db.AutoMigrate(
		&model.WrappedDomainKey{},
		&model.BackupAssetAuditCheckpoint{},
		&model.BackupAssetAuditEvent{},
	); err != nil {
		t.Fatalf("migrate audit tables: %v", err)
	}
	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_wrapped_domain_keys_domain_version ON wrapped_domain_keys(domain, version)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_wrapped_domain_keys_active ON wrapped_domain_keys(domain) WHERE state = 'active'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_asset_audit_events_segment_sequence ON backup_asset_audit_events(segment_no, segment_sequence)`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("create audit test index: %v", err)
		}
	}
	ring := NewKeyring(db, clock.Now)
	writer, err := NewAuditWriter(db, ring, clock.Now, config)
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	return writer, clock, db, ring
}

func standardAuditEventInput(action AuditAction, fingerprints AuditFingerprintInput) AuditEventInput {
	return AuditEventInput{
		Actor: AuditActor{
			UserID:   41,
			Username: "admin",
			Role:     "admin",
		},
		Action:          action,
		Outcome:         AuditOutcomeSuccess,
		RepositoryID:    strings.Repeat("a", 32),
		RecoveryPointID: strings.Repeat("b", 32),
		EntryID:         strings.Repeat("c", 64),
		ItemCount:       1,
		ByteCount:       128,
		Range:           NewRangeSummary(1, 128),
		Fingerprints:    fingerprints,
		Fields: map[AuditField]any{
			AuditFieldStage:         "catalog_read",
			AuditFieldCorrelationID: "corr_123",
		},
	}
}

func auditFingerprintDigest(key []byte, scope, value string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(scope))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
