package model

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestBackupAssetLifecycleSensitiveFieldsEncryptAtRestAndStayOutOfJSON(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_LIFECYCLE_MODEL_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "backup_asset_lifecycle_model.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open lifecycle model database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access lifecycle model database handle: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close lifecycle model database: %v", err)
		}
	})
	if err := db.AutoMigrate(&RecoveryPointHold{}, &BackupRepositoryImportCandidate{}); err != nil {
		t.Fatalf("migrate lifecycle sensitive models: %v", err)
	}

	now := time.Date(2026, 8, 17, 6, 0, 0, 0, time.UTC)
	hold := RecoveryPointHold{
		ID: "11111111111111111111111111111111", RecoveryPointID: "22222222222222222222222222222222",
		HoldType: "legal", State: "released", EncryptedReason: "private hold reason", CreatedBy: 1,
		ReleasedBy: uintPointer(1), ReleasedAt: &now, EncryptedReleaseReason: "private release reason",
		CreatedAt: now, UpdatedAt: now,
	}
	candidate := BackupRepositoryImportCandidate{
		ID: "33333333333333333333333333333333", RepositoryID: "44444444444444444444444444444444",
		CandidateKind: "native_snapshot", SourceFingerprint: strings.Repeat("a", 64),
		EncryptedProviderLocator: "private provider locator", EncryptedEvidence: "private evidence",
		ReviewState: "pending", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&hold).Error; err != nil {
		t.Fatalf("create encrypted lifecycle hold: %v", err)
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatalf("create encrypted import candidate: %v", err)
	}

	var rawHold struct {
		Reason        string `gorm:"column:encrypted_reason"`
		ReleaseReason string `gorm:"column:encrypted_release_reason"`
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Table("recovery_point_holds").
		Select("encrypted_reason, encrypted_release_reason").Where("id = ?", hold.ID).Scan(&rawHold).Error; err != nil {
		t.Fatalf("read raw lifecycle hold: %v", err)
	}
	var rawCandidate struct {
		Locator  string `gorm:"column:encrypted_provider_locator"`
		Evidence string `gorm:"column:encrypted_evidence"`
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Table("backup_repository_import_candidates").
		Select("encrypted_provider_locator, encrypted_evidence").Where("id = ?", candidate.ID).Scan(&rawCandidate).Error; err != nil {
		t.Fatalf("read raw import candidate: %v", err)
	}
	for name, value := range map[string]string{
		"hold reason": rawHold.Reason, "release reason": rawHold.ReleaseReason,
		"provider locator": rawCandidate.Locator, "candidate evidence": rawCandidate.Evidence,
	} {
		if !strings.HasPrefix(value, "enc:") || strings.Contains(value, "private") {
			t.Fatalf("%s is not encrypted at rest: %q", name, value)
		}
	}

	var loadedHold RecoveryPointHold
	if err := db.First(&loadedHold, "id = ?", hold.ID).Error; err != nil {
		t.Fatalf("load encrypted lifecycle hold: %v", err)
	}
	if loadedHold.EncryptedReason != "private hold reason" || loadedHold.EncryptedReleaseReason != "private release reason" {
		t.Fatalf("hold fields did not decrypt: %+v", loadedHold)
	}
	var loadedCandidate BackupRepositoryImportCandidate
	if err := db.First(&loadedCandidate, "id = ?", candidate.ID).Error; err != nil {
		t.Fatalf("load encrypted import candidate: %v", err)
	}
	if loadedCandidate.EncryptedProviderLocator != "private provider locator" || loadedCandidate.EncryptedEvidence != "private evidence" {
		t.Fatalf("candidate fields did not decrypt: %+v", loadedCandidate)
	}

	for name, value := range map[string]any{"hold": loadedHold, "candidate": loadedCandidate} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		body := string(encoded)
		for _, forbidden := range []string{"encrypted_reason", "encrypted_release_reason", "encrypted_provider_locator", "encrypted_evidence", "private"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s JSON leaked %q: %s", name, forbidden, body)
			}
		}
	}
}

func TestBackupAssetLifecycleEncryptionFailuresFailClosed(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "")
	t.Setenv("DATA_ENCRYPTION_KEY", "")
	secure.ResetForTesting()
	if err := (&RecoveryPointHold{EncryptedReason: "must not persist"}).BeforeSave(nil); err == nil {
		t.Fatal("hold encryption succeeded without the required key")
	}

	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_LIFECYCLE_MODEL_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	if err := (&BackupRepositoryImportCandidate{EncryptedProviderLocator: "enc:v2:not-valid-base64"}).AfterFind(nil); err == nil {
		t.Fatal("invalid candidate ciphertext decrypted without error")
	}
}

func uintPointer(value uint) *uint { return &value }
