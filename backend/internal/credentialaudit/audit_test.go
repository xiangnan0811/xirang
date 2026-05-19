package credentialaudit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestWriteUsesGORMNowFuncForCreatedAt(t *testing.T) {
	fixedNow := time.Date(2026, 5, 19, 10, 30, 0, 0, time.UTC)
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{
		NowFunc: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	err = Write(db, Event{
		Username:         "system",
		Role:             "system",
		Action:           "ssh_key.export",
		Purpose:          "ssh_key_export",
		CredentialKind:   "ssh_key",
		CredentialSource: "ssh_key_export",
	})
	if err != nil {
		t.Fatalf("write audit event: %v", err)
	}

	var record model.CredentialAuditEvent
	if err := db.First(&record).Error; err != nil {
		t.Fatalf("load audit event: %v", err)
	}
	if !record.CreatedAt.Equal(fixedNow) {
		t.Fatalf("CreatedAt should use GORM NowFunc, got %v want %v", record.CreatedAt, fixedNow)
	}
	if record.CreatedAt.Location().String() != "UTC" {
		t.Fatalf("CreatedAt location = %s, want UTC", record.CreatedAt.Location())
	}
}

func TestWriteRedactsRawCommandOutputFromErrorMessage(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	err = Write(db, Event{
		Username:         "system",
		Role:             "system",
		Action:           "drill.phase",
		Purpose:          "drill",
		CredentialKind:   "node_credential",
		CredentialSource: "node.private_key",
		Outcome:          OutcomeFailure,
		ErrorMessage:     "remote command failed, 输出: FAKE_COMMAND_OUTPUT_FOR_TEST_ONLY token=FAKE_OUTPUT_TOKEN_FOR_TEST_ONLY",
	})
	if err != nil {
		t.Fatalf("write audit event: %v", err)
	}

	var record model.CredentialAuditEvent
	if err := db.First(&record).Error; err != nil {
		t.Fatalf("load audit event: %v", err)
	}
	if strings.Contains(record.ErrorMessage, "FAKE_COMMAND_OUTPUT_FOR_TEST_ONLY") || strings.Contains(record.ErrorMessage, "FAKE_OUTPUT_TOKEN_FOR_TEST_ONLY") {
		t.Fatalf("error message should redact raw command output: %q", record.ErrorMessage)
	}
	if !strings.Contains(record.ErrorMessage, "[REDACTED_OUTPUT]") {
		t.Fatalf("error message should include output redaction marker: %q", record.ErrorMessage)
	}
}

func TestWriteSanitizesMetadataAndBoundsFields(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	err = Write(db, Event{
		Username:         strings.Repeat("u", 80),
		Role:             "admin",
		Action:           "ssh_key.export",
		Purpose:          "ssh_key_export",
		CredentialKind:   "ssh_key",
		CredentialSource: "ssh_key_export",
		Outcome:          OutcomeSuccess,
		ErrorMessage:     "fixture failure",
		Metadata: map[string]any{
			"format":      "json",
			"private_key": "FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
			"note":        "fixture note",
			"command":     "fixture operation text",
		},
	})
	if err != nil {
		t.Fatalf("write audit event: %v", err)
	}

	var record model.CredentialAuditEvent
	if err := db.First(&record).Error; err != nil {
		t.Fatalf("load audit event: %v", err)
	}
	if len([]rune(record.Username)) != 64 {
		t.Fatalf("username should be bounded to 64 runes, got %d", len([]rune(record.Username)))
	}
	if strings.Contains(record.ErrorMessage, "fixture failure") && len(record.ErrorMessage) > 500 {
		t.Fatalf("error message should be bounded, got %q", record.ErrorMessage)
	}

	var metadata map[string]any
	if err := json.Unmarshal([]byte(record.Metadata), &metadata); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if metadata["format"] != "json" {
		t.Fatalf("safe metadata should be retained: %#v", metadata)
	}
	if _, ok := metadata["private_key"]; ok {
		t.Fatalf("private key metadata key should be dropped: %#v", metadata)
	}
	if _, ok := metadata["command"]; ok {
		t.Fatalf("command metadata key should be dropped: %#v", metadata)
	}
	if strings.Contains(record.Metadata, "FAKE_PRIVATE_KEY_FOR_TEST_ONLY") || strings.Contains(record.Metadata, "fixture operation text") {
		t.Fatalf("metadata should not contain sensitive-field values: %s", record.Metadata)
	}
}
