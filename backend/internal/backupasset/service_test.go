package backupasset

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
)

func TestSanitizedDTOExcludesSecretsAndLocators(t *testing.T) {
	now := time.Date(2026, 7, 13, 2, 3, 4, 0, time.UTC)
	repositoryIdentity := "FAKE_REPOSITORY_IDENTITY_FOR_TEST_ONLY"
	retirementReason := string(RetirementCutover)
	repository := model.BackupRepository{
		ID:                 strings.Repeat("a", 32),
		ProviderKind:       string(ProviderRsync),
		RepositoryIdentity: &repositoryIdentity,
		DisplayName:        "backup-a",
		Description:        "safe description",
		VersionMode:        string(VersionHardlinkTree),
		Status:             string(RepositoryOnline),
		CapabilityRevision: 1,
		CapabilitiesJSON:   `{"list":true,"download":true}`,
		ImmutabilityLevel:  string(ImmutabilityXirangManaged),
		LastSeenAt:         &now,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	recoveryPoint := model.RecoveryPoint{
		ID:                       strings.Repeat("b", 32),
		RepositoryID:             repository.ID,
		LineageJSON:              `{"source_recovery_point_id":"` + strings.Repeat("d", 32) + `"}`,
		Semantics:                string(PointMutableHead),
		State:                    string(RecoveryPointRetired),
		PhysicalAvailability:     string(PhysicalOnline),
		HoldState:                string(HoldNone),
		ImmutabilityLevel:        string(ImmutabilityMutable),
		ObservedAt:               &now,
		RetirementReason:         &retirementReason,
		RetiredAt:                &now,
		EntryCount:               3,
		LogicalBytes:             42,
		CapabilityRevision:       1,
		CapabilitiesJSON:         `{"list":true,"download":true}`,
		EncryptedProviderLocator: "FAKE_ENCRYPTED_PROVIDER_LOCATOR_FOR_TEST_ONLY",
		EncryptedRollbackLocator: "FAKE_ENCRYPTED_ROLLBACK_LOCATOR_FOR_TEST_ONLY",
		CreatedAt:                now,
		UpdatedAt:                now,
	}

	repositoryDTO, err := ToRepositoryDTO(repository)
	if err != nil {
		t.Fatalf("convert repository DTO: %v", err)
	}
	recoveryPointDTO, err := ToRecoveryPointDTO(recoveryPoint)
	if err != nil {
		t.Fatalf("convert recovery point DTO: %v", err)
	}
	if repositoryDTO.ID != repository.ID || recoveryPointDTO.ID != recoveryPoint.ID {
		t.Fatalf("safe identity missing from DTOs: %+v %+v", repositoryDTO, recoveryPointDTO)
	}

	payload, err := json.Marshal(struct {
		Repository    RepositoryDTO    `json:"repository"`
		RecoveryPoint RecoveryPointDTO `json:"recovery_point"`
	}{repositoryDTO, recoveryPointDTO})
	if err != nil {
		t.Fatalf("marshal DTOs: %v", err)
	}
	text := string(payload)
	for _, forbidden := range []string{
		repositoryIdentity,
		recoveryPoint.EncryptedProviderLocator,
		recoveryPoint.EncryptedRollbackLocator,
		"repository_identity",
		"provider_locator",
		"rollback_locator",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized DTO JSON contains forbidden value/key %q: %s", forbidden, text)
		}
	}
}

func TestBackupAssetModelHooksEncryptLocators(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	type hookCase struct {
		name       string
		beforeSave func() error
		afterFind  func() error
		values     func() []string
	}

	binding := model.RepositoryAccessBinding{EncryptedConfig: "FAKE_ACCESS_CONFIG_FOR_TEST_ONLY"}
	link := model.TaskRepositoryLink{EncryptedLegacyLocator: "FAKE_LEGACY_LOCATOR_FOR_TEST_ONLY"}
	point := model.RecoveryPoint{
		EncryptedProviderLocator: "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY",
		EncryptedRollbackLocator: "FAKE_ROLLBACK_LOCATOR_FOR_TEST_ONLY",
	}
	manifest := model.RecoveryPointManifest{EncryptedCommitEvidence: "FAKE_COMMIT_EVIDENCE_FOR_TEST_ONLY"}
	entry := model.CatalogEntry{EncryptedProviderLocator: "FAKE_ENTRY_LOCATOR_FOR_TEST_ONLY"}
	values := []hookCase{
		{"access config", func() error { return binding.BeforeSave(nil) }, func() error { return binding.AfterFind(nil) }, func() []string { return []string{binding.EncryptedConfig} }},
		{"legacy locator", func() error { return link.BeforeSave(nil) }, func() error { return link.AfterFind(nil) }, func() []string { return []string{link.EncryptedLegacyLocator} }},
		{"recovery point locators", func() error { return point.BeforeSave(nil) }, func() error { return point.AfterFind(nil) }, func() []string { return []string{point.EncryptedProviderLocator, point.EncryptedRollbackLocator} }},
		{"commit evidence", func() error { return manifest.BeforeSave(nil) }, func() error { return manifest.AfterFind(nil) }, func() []string { return []string{manifest.EncryptedCommitEvidence} }},
		{"entry locator", func() error { return entry.BeforeSave(nil) }, func() error { return entry.AfterFind(nil) }, func() []string { return []string{entry.EncryptedProviderLocator} }},
	}

	for _, tt := range values {
		t.Run(tt.name, func(t *testing.T) {
			plain := append([]string(nil), tt.values()...)
			if err := tt.beforeSave(); err != nil {
				t.Fatalf("BeforeSave: %v", err)
			}
			for index, encrypted := range tt.values() {
				if !strings.HasPrefix(encrypted, "enc:v2:") || strings.Contains(encrypted, plain[index]) {
					t.Fatalf("value was not encrypted at rest: %q", encrypted)
				}
			}
			if err := tt.afterFind(); err != nil {
				t.Fatalf("AfterFind: %v", err)
			}
			for index, decrypted := range tt.values() {
				if decrypted != plain[index] {
					t.Fatalf("decrypt mismatch: got %q want %q", decrypted, plain[index])
				}
			}
		})
	}
}

func TestBackupAssetSettingsFoundationServiceIsDisabledByDefault(t *testing.T) {
	reader := staticSettingsReader{
		"backup_assets.enabled":                 "false",
		"backup_assets.lease_duration":          "5m",
		"backup_assets.lease_heartbeat":         "60s",
		"backup_assets.lease_absolute_deadline": "168h",
	}
	service := NewFoundationService(reader)
	if service.Enabled() {
		t.Fatal("foundation service unexpectedly enabled")
	}
	leaseConfig, err := service.LeaseConfig()
	if err != nil {
		t.Fatalf("LeaseConfig: %v", err)
	}
	if leaseConfig.Duration != 5*time.Minute || leaseConfig.Heartbeat != time.Minute || leaseConfig.AbsoluteDeadline != 168*time.Hour {
		t.Fatalf("lease settings mapping mismatch: %+v", leaseConfig)
	}
	reader["backup_assets.enabled"] = "true"
	if !service.Enabled() {
		t.Fatal("explicit true feature setting was not honored")
	}
	reader["backup_assets.enabled"] = "invalid"
	if service.Enabled() {
		t.Fatal("malformed feature setting must fail closed")
	}
}

type staticSettingsReader map[string]string

func (reader staticSettingsReader) GetEffective(key string) string {
	return reader[key]
}
