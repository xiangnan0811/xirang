package settings

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setRecoveryTargetRootTestEncryption(t *testing.T) {
	t.Helper()
	key := base64.StdEncoding.EncodeToString([]byte("FAKE_RECOVERY_TARGET_ROOT_KEY_FOR_TEST_ONLY"))
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", key)
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", key)
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
}

func setupRecoveryTargetRootTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "recovery-target-root.db") +
		"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON&_synchronous=NORMAL&_txlock=immediate&_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.Node{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedRecoveryTargetRootNode(t *testing.T, db *gorm.DB, id uint, archived bool) {
	t.Helper()
	node := model.Node{
		ID: id, Name: "recovery-node-" + strconv.FormatUint(uint64(id), 10),
		Host: "recovery-node.invalid", Port: 22, Username: "tester", AuthType: "password",
		BackupDir: "recovery-backup-" + strconv.FormatUint(uint64(id), 10), Archived: archived,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
}

func registerRecoveryTargetRootForTest(
	t *testing.T,
	db *gorm.DB,
	service *Service,
	definition RecoveryTargetRootDefinition,
) RecoveryTargetRootResolution {
	t.Helper()
	definition = completeRecoveryTargetRootDefinitionForTest(definition)
	var result RecoveryTargetRootResolution
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = service.RegisterRecoveryTargetRootTx(context.Background(), tx, definition)
		return err
	}); err != nil {
		t.Fatalf("register recovery target root: %v", err)
	}
	return result
}

func completeRecoveryTargetRootDefinitionForTest(definition RecoveryTargetRootDefinition) RecoveryTargetRootDefinition {
	if definition.AuthorityRevision == "" {
		digest, _ := RecoveryTargetRootLocatorDigest(definition.NodeID, definition.RootID, definition.Locator)
		definition.AuthorityRevision = digest[:32]
	}
	if definition.RootObservationRevision == "" {
		definition.RootObservationRevision = "test-root-observation-" + definition.RootID
	}
	if definition.Policy.OverlapPolicyBinding == "" {
		definition.Policy.OverlapPolicyBinding = "test-overlap-policy-" + definition.RootID
	}
	return definition
}

func resolveRecoveryTargetRootForTest(
	t *testing.T,
	db *gorm.DB,
	service *Service,
	nodeID uint,
	rootID string,
) (RecoveryTargetRootResolution, error) {
	t.Helper()
	var result RecoveryTargetRootResolution
	var resolveErr error
	if err := db.Transaction(func(tx *gorm.DB) error {
		result, resolveErr = service.ResolveRecoveryTargetRootTx(context.Background(), tx, nodeID, rootID)
		return nil
	}); err != nil {
		t.Fatalf("resolve transaction: %v", err)
	}
	return result, resolveErr
}

func recoveryTargetRootTestKey(nodeID uint, rootID string) string {
	return RecoveryTargetRootKeyPrefix + strconv.FormatUint(uint64(nodeID), 10) + "." + rootID
}

func rawRecoveryTargetRootValue(t *testing.T, db *gorm.DB, key string) string {
	t.Helper()
	var value string
	if err := db.Table("system_settings").Select("value").Where("key = ?", key).Scan(&value).Error; err != nil {
		t.Fatal(err)
	}
	return value
}

func encryptRecoveryTargetRootDocumentForTest(t *testing.T, document string) string {
	t.Helper()
	encrypted, err := secure.EncryptString(document)
	if err != nil {
		t.Fatal(err)
	}
	return encrypted
}

func mutateRecoveryTargetRootDocumentForTest(
	t *testing.T,
	document string,
	mutate func(map[string]any),
) string {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(document), &decoded); err != nil {
		t.Fatal(err)
	}
	mutate(decoded)
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func recoveryTargetRootDocumentForTest(t *testing.T, ciphertext string) map[string]any {
	t.Helper()
	document, err := secure.DecryptString(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(document), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func recoveryTargetRootDocumentStringForTest(t *testing.T, value map[string]any, field string) string {
	t.Helper()
	resolved, ok := value[field].(string)
	if !ok || resolved == "" {
		t.Fatalf("recovery target root document has no usable %q", field)
	}
	return resolved
}

func TestRecoveryTargetRootV2RejectsIncompleteAuthority(t *testing.T) {
	setRecoveryTargetRootTestEncryption(t)
	db := setupRecoveryTargetRootTestDB(t)
	seedRecoveryTargetRootNode(t, db, 101, false)
	service := NewService(db)
	definition := RecoveryTargetRootDefinition{
		NodeID: 101, RootID: "root-a", SafeLabel: "FAKE_V2_ROOT_LABEL_FOR_TEST_ONLY",
		Locator: "/srv/FAKE_V2_ROOT_FOR_TEST_ONLY",
	}
	t.Run("registration rejects omitted authority", func(t *testing.T) {
		var registrationErr error
		if err := db.Transaction(func(tx *gorm.DB) error {
			_, registrationErr = service.RegisterRecoveryTargetRootTx(context.Background(), tx, definition)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(registrationErr, ErrRecoveryTargetRootInvalid) {
			t.Fatalf("incomplete registration error=%v, want ErrRecoveryTargetRootInvalid", registrationErr)
		}
	})
	registerRecoveryTargetRootForTest(t, db, service, definition)
	key := recoveryTargetRootTestKey(definition.NodeID, definition.RootID)
	validCiphertext := rawRecoveryTargetRootValue(t, db, key)
	validDocument, err := secure.DecryptString(validCiphertext)
	if err != nil {
		t.Fatal(err)
	}

	tamperedCiphertext := validCiphertext
	last := tamperedCiphertext[len(tamperedCiphertext)-1]
	replacement := byte('A')
	if last == replacement {
		replacement = 'B'
	}
	tamperedCiphertext = tamperedCiphertext[:len(tamperedCiphertext)-1] + string(replacement)
	tests := []struct {
		name  string
		value string
	}{
		{name: "schema version one", value: encryptRecoveryTargetRootDocumentForTest(t,
			mutateRecoveryTargetRootDocumentForTest(t, validDocument, func(document map[string]any) {
				document["schema_version"] = 1
			}))},
		{name: "legacy ciphertext envelope", value: strings.Replace(validCiphertext, "enc:v2:", "enc:v1:", 1)},
		{name: "tampered ciphertext", value: tamperedCiphertext},
		{name: "unknown field", value: encryptRecoveryTargetRootDocumentForTest(t,
			mutateRecoveryTargetRootDocumentForTest(t, validDocument, func(document map[string]any) {
				document["unknown"] = "FAKE_V2_UNKNOWN_FIELD_FOR_TEST_ONLY"
			}))},
		{name: "duplicate field", value: encryptRecoveryTargetRootDocumentForTest(t,
			strings.Replace(validDocument, `"root_id":"root-a"`, `"root_id":"root-a","root_id":"root-a"`, 1))},
		{name: "missing authority revision", value: encryptRecoveryTargetRootDocumentForTest(t,
			mutateRecoveryTargetRootDocumentForTest(t, validDocument, func(document map[string]any) {
				delete(document, "authority_revision")
			}))},
		{name: "missing root observation revision", value: encryptRecoveryTargetRootDocumentForTest(t,
			mutateRecoveryTargetRootDocumentForTest(t, validDocument, func(document map[string]any) {
				delete(document, "root_observation_revision")
			}))},
		{name: "missing reserve bytes", value: encryptRecoveryTargetRootDocumentForTest(t,
			mutateRecoveryTargetRootDocumentForTest(t, validDocument, func(document map[string]any) {
				delete(document, "reserve_bytes")
			}))},
		{name: "missing reserve inodes", value: encryptRecoveryTargetRootDocumentForTest(t,
			mutateRecoveryTargetRootDocumentForTest(t, validDocument, func(document map[string]any) {
				delete(document, "reserve_inodes")
			}))},
		{name: "null reserve bytes", value: encryptRecoveryTargetRootDocumentForTest(t,
			mutateRecoveryTargetRootDocumentForTest(t, validDocument, func(document map[string]any) {
				document["reserve_bytes"] = nil
			}))},
		{name: "null reserve inodes", value: encryptRecoveryTargetRootDocumentForTest(t,
			mutateRecoveryTargetRootDocumentForTest(t, validDocument, func(document map[string]any) {
				document["reserve_inodes"] = nil
			}))},
		{name: "missing overlap policy binding", value: encryptRecoveryTargetRootDocumentForTest(t,
			mutateRecoveryTargetRootDocumentForTest(t, validDocument, func(document map[string]any) {
				delete(document, "overlap_policy_binding")
			}))},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if err := db.Model(&model.SystemSetting{}).Where("key = ?", key).UpdateColumn("value", testCase.value).Error; err != nil {
				t.Fatal(err)
			}
			_, resolveErr := resolveRecoveryTargetRootForTest(t, db, service, definition.NodeID, definition.RootID)
			if !errors.Is(resolveErr, ErrRecoveryTargetRootUnavailable) || resolveErr.Error() != ErrRecoveryTargetRootUnavailable.Error() {
				t.Fatalf("resolve error=%v, want only ErrRecoveryTargetRootUnavailable", resolveErr)
			}
			if err := db.Model(&model.SystemSetting{}).Where("key = ?", key).UpdateColumn("value", validCiphertext).Error; err != nil {
				t.Fatal(err)
			}
		})
	}

	sibling := RecoveryTargetRootDefinition{
		NodeID: definition.NodeID, RootID: "root-b", SafeLabel: "FAKE_V2_SIBLING_LABEL_FOR_TEST_ONLY",
		Locator: "/srv/FAKE_V2_SIBLING_ROOT_FOR_TEST_ONLY",
	}
	registerRecoveryTargetRootForTest(t, db, service, sibling)
	siblingCiphertext := rawRecoveryTargetRootValue(t, db, recoveryTargetRootTestKey(sibling.NodeID, sibling.RootID))
	if err := db.Model(&model.SystemSetting{}).Where("key = ?", key).UpdateColumn("value", siblingCiphertext).Error; err != nil {
		t.Fatal(err)
	}
	if _, resolveErr := resolveRecoveryTargetRootForTest(t, db, service, definition.NodeID, definition.RootID); !errors.Is(resolveErr, ErrRecoveryTargetRootUnavailable) || resolveErr.Error() != ErrRecoveryTargetRootUnavailable.Error() {
		t.Fatalf("substituted payload error=%v, want only ErrRecoveryTargetRootUnavailable", resolveErr)
	}
	if err := db.Model(&model.SystemSetting{}).Where("key = ?", key).UpdateColumn("value", validCiphertext).Error; err != nil {
		t.Fatal(err)
	}

	currentKey := base64.StdEncoding.EncodeToString([]byte("FAKE_RECOVERY_TARGET_ROOT_KEY_FOR_TEST_ONLY"))
	substitutedKey := base64.StdEncoding.EncodeToString([]byte("FAKE_SUBSTITUTED_TARGET_ROOT_KEY_FOR_TEST_ONLY"))
	t.Setenv("DATA_ENCRYPTION_KEY", substitutedKey)
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", substitutedKey)
	secure.ResetForTesting()
	if _, resolveErr := resolveRecoveryTargetRootForTest(t, db, service, definition.NodeID, definition.RootID); !errors.Is(resolveErr, ErrRecoveryTargetRootUnavailable) || resolveErr.Error() != ErrRecoveryTargetRootUnavailable.Error() {
		t.Fatalf("substituted key error=%v, want only ErrRecoveryTargetRootUnavailable", resolveErr)
	}
	t.Setenv("DATA_ENCRYPTION_KEY", currentKey)
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", currentKey)
	secure.ResetForTesting()
}

func TestRecoveryTargetRootV2RotationSemantics(t *testing.T) {
	setRecoveryTargetRootTestEncryption(t)
	db := setupRecoveryTargetRootTestDB(t)
	seedRecoveryTargetRootNode(t, db, 102, false)
	service := NewService(db)
	definition := completeRecoveryTargetRootDefinitionForTest(RecoveryTargetRootDefinition{
		NodeID: 102, RootID: "root-a", SafeLabel: "FAKE_V2_ROTATION_LABEL_FOR_TEST_ONLY",
		Locator: "/srv/FAKE_V2_ROTATION_ROOT_FOR_TEST_ONLY",
	})
	registerRecoveryTargetRootForTest(t, db, service, definition)
	key := recoveryTargetRootTestKey(definition.NodeID, definition.RootID)
	baselineCiphertext := rawRecoveryTargetRootValue(t, db, key)
	baseline := recoveryTargetRootDocumentForTest(t, baselineCiphertext)
	if got := baseline["schema_version"]; got != float64(2) {
		t.Fatalf("schema_version=%v, want 2", got)
	}
	baselineAuthorityRevision := recoveryTargetRootDocumentStringForTest(t, baseline, "authority_revision")
	_ = recoveryTargetRootDocumentStringForTest(t, baseline, "root_observation_revision")
	_ = recoveryTargetRootDocumentStringForTest(t, baseline, "overlap_policy_binding")
	if _, ok := baseline["reserve_bytes"].(float64); !ok {
		t.Fatalf("reserve_bytes=%T, want JSON number", baseline["reserve_bytes"])
	}
	if _, ok := baseline["reserve_inodes"].(float64); !ok {
		t.Fatalf("reserve_inodes=%T, want JSON number", baseline["reserve_inodes"])
	}

	registerRecoveryTargetRootForTest(t, db, service, definition)
	if got := rawRecoveryTargetRootValue(t, db, key); got != baselineCiphertext {
		t.Fatal("exact replay rewrote the encrypted authority record")
	}

	safeLabelOnly := definition
	safeLabelOnly.SafeLabel = "FAKE_V2_ROTATION_NEW_LABEL_FOR_TEST_ONLY"
	registerRecoveryTargetRootForTest(t, db, service, safeLabelOnly)
	safeLabelDocument := recoveryTargetRootDocumentForTest(t, rawRecoveryTargetRootValue(t, db, key))
	if got := recoveryTargetRootDocumentStringForTest(t, safeLabelDocument, "authority_revision"); got != baselineAuthorityRevision {
		t.Fatalf("safe-label-only authority revision=%q, want preserved opaque revision", got)
	}

	securityRotations := []struct {
		name     string
		revision string
		mutate   func(*RecoveryTargetRootDefinition)
	}{
		{name: "locator", revision: strings.Repeat("b", 32), mutate: func(value *RecoveryTargetRootDefinition) {
			value.Locator = "/srv/FAKE_V2_ROTATION_ROOT_NEXT_FOR_TEST_ONLY"
		}},
		{name: "root observation", revision: strings.Repeat("c", 32), mutate: func(value *RecoveryTargetRootDefinition) {
			value.RootObservationRevision = "test-root-observation-rotated"
		}},
		{name: "reserve", revision: strings.Repeat("d", 32), mutate: func(value *RecoveryTargetRootDefinition) {
			value.Policy.ReserveBytes = 4096
		}},
		{name: "policy", revision: strings.Repeat("e", 32), mutate: func(value *RecoveryTargetRootDefinition) {
			value.Policy.OverlapPolicyBinding = "test-overlap-policy-rotated"
		}},
	}
	priorAuthorityRevision := baselineAuthorityRevision
	prior := safeLabelOnly
	for _, rotation := range securityRotations {
		t.Run(rotation.name, func(t *testing.T) {
			next := prior
			rotation.mutate(&next)
			next.AuthorityRevision = rotation.revision
			registerRecoveryTargetRootForTest(t, db, service, next)
			document := recoveryTargetRootDocumentForTest(t, rawRecoveryTargetRootValue(t, db, key))
			if got := recoveryTargetRootDocumentStringForTest(t, document, "authority_revision"); got != rotation.revision || got == priorAuthorityRevision {
				t.Fatalf("%s rotation authority revision=%q, want new %q", rotation.name, got, rotation.revision)
			}
			priorAuthorityRevision = rotation.revision
			prior = next
		})
	}
}

func TestRecoveryTargetRootPolicyJSONOmitsPrivateReserveValues(t *testing.T) {
	encoded, err := json.Marshal(RecoveryTargetRootPolicy{
		ReserveBytes: 4096, ReserveInodes: 8, OverlapPolicyBinding: "FAKE_PRIVATE_POLICY_BINDING_FOR_TEST_ONLY",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{}` {
		t.Fatalf("private policy values leaked to JSON: %s", encoded)
	}
}

func TestRecoveryTargetRootV2ConcurrentMutation(t *testing.T) {
	setRecoveryTargetRootTestEncryption(t)
	db := setupRecoveryTargetRootTestDB(t)
	seedRecoveryTargetRootNode(t, db, 103, false)
	service := NewService(db)
	definition := completeRecoveryTargetRootDefinitionForTest(RecoveryTargetRootDefinition{
		NodeID: 103, RootID: "root-a", SafeLabel: "FAKE_V2_CONCURRENT_LABEL_FOR_TEST_ONLY",
		Locator: "/srv/FAKE_V2_CONCURRENT_ROOT_FOR_TEST_ONLY",
	})
	registerRecoveryTargetRootForTest(t, db, service, definition)
	key := recoveryTargetRootTestKey(definition.NodeID, definition.RootID)
	baseline := recoveryTargetRootDocumentForTest(t, rawRecoveryTargetRootValue(t, db, key))
	baselineAuthorityRevision := recoveryTargetRootDocumentStringForTest(t, baseline, "authority_revision")

	results := make(chan error, 2)
	start := make(chan struct{})
	for index, locator := range []string{
		"/srv/FAKE_V2_CONCURRENT_ROOT_ONE_FOR_TEST_ONLY",
		"/srv/FAKE_V2_CONCURRENT_ROOT_TWO_FOR_TEST_ONLY",
	} {
		locator := locator
		revision := strings.Repeat("b", 31) + strconv.Itoa(index+1)
		go func() {
			<-start
			results <- db.Transaction(func(tx *gorm.DB) error {
				mutation := definition
				mutation.Locator = locator
				mutation.AuthorityRevision = revision
				_, err := service.RegisterRecoveryTargetRootTx(context.Background(), tx, mutation)
				return err
			})
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent authority mutation: %v", err)
		}
	}

	document := recoveryTargetRootDocumentForTest(t, rawRecoveryTargetRootValue(t, db, key))
	if got := document["schema_version"]; got != float64(2) {
		t.Fatalf("schema_version=%v, want 2 after concurrent mutation", got)
	}
	if got := recoveryTargetRootDocumentStringForTest(t, document, "authority_revision"); got == baselineAuthorityRevision {
		t.Fatal("concurrent security mutation retained the original authority revision")
	}
	if got := recoveryTargetRootDocumentStringForTest(t, document, "canonical_locator"); got != "/srv/FAKE_V2_CONCURRENT_ROOT_ONE_FOR_TEST_ONLY" && got != "/srv/FAKE_V2_CONCURRENT_ROOT_TWO_FOR_TEST_ONLY" {
		t.Fatalf("concurrent mutation persisted unrecognized locator")
	}
}

func TestRecoveryTargetRootRegistryPersistsPrivateV2Records(t *testing.T) {
	setRecoveryTargetRootTestEncryption(t)
	db := setupRecoveryTargetRootTestDB(t)
	seedRecoveryTargetRootNode(t, db, 41, false)
	seedRecoveryTargetRootNode(t, db, 42, false)
	service := NewService(db)

	definitions := []RecoveryTargetRootDefinition{
		{NodeID: 41, RootID: "root-b", SafeLabel: "FAKE_RECOVERY_ROOT_B_LABEL_FOR_TEST_ONLY", Locator: "/srv/FAKE_RECOVERY_ROOT_B_FOR_TEST_ONLY"},
		{NodeID: 41, RootID: "root-a", SafeLabel: "FAKE_RECOVERY_ROOT_A_LABEL_FOR_TEST_ONLY", Locator: "/srv/FAKE_RECOVERY_ROOT_A_FOR_TEST_ONLY"},
		{NodeID: 42, RootID: "root-a", SafeLabel: "FAKE_RECOVERY_ROOT_C_LABEL_FOR_TEST_ONLY", Locator: "/srv/FAKE_RECOVERY_ROOT_C_FOR_TEST_ONLY"},
	}
	resolutions := make([]RecoveryTargetRootResolution, 0, len(definitions))
	for _, definition := range definitions {
		resolutions = append(resolutions, registerRecoveryTargetRootForTest(t, db, service, definition))
	}

	var rows []model.SystemSetting
	if err := db.Where("key LIKE ?", RecoveryTargetRootKeyPrefix+"%").Order("key ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(definitions) {
		t.Fatalf("private row count=%d, want %d", len(rows), len(definitions))
	}
	before := make(map[string]string, len(rows))
	for _, row := range rows {
		before[row.Key] = row.Value
		if !strings.HasPrefix(row.Value, "enc:v2:") {
			t.Fatalf("private row %q is not current v2 ciphertext", row.Key)
		}
		for _, definition := range definitions {
			if strings.Contains(row.Value, definition.Locator) || strings.Contains(row.Value, definition.SafeLabel) {
				t.Fatalf("private row %q contains recognizable plaintext", row.Key)
			}
		}
	}

	for index, definition := range definitions {
		resolved, err := resolveRecoveryTargetRootForTest(t, db, service, definition.NodeID, definition.RootID)
		if err != nil {
			t.Fatalf("resolve %d/%s: %v", definition.NodeID, definition.RootID, err)
		}
		wantDigest, err := RecoveryTargetRootLocatorDigest(definition.NodeID, definition.RootID, definition.Locator)
		if err != nil {
			t.Fatal(err)
		}
		if resolved != resolutions[index] || resolved.NodeID != definition.NodeID || resolved.RootID != definition.RootID ||
			resolved.SafeLabel != definition.SafeLabel || resolved.Locator != definition.Locator || resolved.LocatorDigest != wantDigest {
			t.Fatalf("resolution=%+v registration=%+v definition=%+v", resolved, resolutions[index], definition)
		}
		encoded, err := json.Marshal(resolved)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), definition.Locator) || strings.Contains(string(encoded), wantDigest) {
			t.Fatalf("private resolution fields leaked to JSON: %s", encoded)
		}
	}

	summaries, err := service.ListRecoveryTargetRoots(context.Background(), 41)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 || summaries[0].RootID != "root-a" || summaries[1].RootID != "root-b" {
		t.Fatalf("safe summaries are not root-ID sorted: %+v", summaries)
	}
	encodedSummaries, err := json.Marshal(summaries)
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if strings.Contains(string(encodedSummaries), definition.Locator) ||
			strings.Contains(string(encodedSummaries), resolutions[0].LocatorDigest) {
			t.Fatalf("safe summaries leaked private fields: %s", encodedSummaries)
		}
	}

	registerRecoveryTargetRootForTest(t, db, service, definitions[1])
	if got := rawRecoveryTargetRootValue(t, db, recoveryTargetRootTestKey(41, "root-a")); got != before[recoveryTargetRootTestKey(41, "root-a")] {
		t.Fatal("identical registration rewrote the private row")
	}

	rotated := definitions[1]
	rotated.SafeLabel = "FAKE_ROTATED_RECOVERY_ROOT_A_LABEL_FOR_TEST_ONLY"
	rotated.Locator = "/srv/FAKE_ROTATED_RECOVERY_ROOT_A_FOR_TEST_ONLY"
	rotatedResolution := registerRecoveryTargetRootForTest(t, db, service, rotated)
	if rotatedResolution.Locator != rotated.Locator || rotatedResolution.LocatorDigest == resolutions[1].LocatorDigest {
		t.Fatalf("rotation did not produce one new exact tuple: %+v", rotatedResolution)
	}
	if got := rawRecoveryTargetRootValue(t, db, recoveryTargetRootTestKey(41, "root-a")); got == before[recoveryTargetRootTestKey(41, "root-a")] {
		t.Fatal("rotation did not replace the selected row")
	}
	for _, key := range []string{recoveryTargetRootTestKey(41, "root-b"), recoveryTargetRootTestKey(42, "root-a")} {
		if got := rawRecoveryTargetRootValue(t, db, key); got != before[key] {
			t.Fatalf("rotation changed sibling/cross-node row %q", key)
		}
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		return service.DeleteRecoveryTargetRootTx(context.Background(), tx, 41, "root-b")
	}); err != nil {
		t.Fatalf("delete exact root: %v", err)
	}
	if _, err := resolveRecoveryTargetRootForTest(t, db, service, 41, "root-b"); !errors.Is(err, ErrRecoveryTargetRootNotFound) {
		t.Fatalf("deleted root resolve error=%v", err)
	}
	if resolved, err := resolveRecoveryTargetRootForTest(t, db, service, 42, "root-a"); err != nil || resolved.Locator != definitions[2].Locator {
		t.Fatalf("cross-node sibling changed after delete: resolution=%+v error=%v", resolved, err)
	}
}

func TestRecoveryTargetRootRegistryRejectsInvalidDefinitions(t *testing.T) {
	setRecoveryTargetRootTestEncryption(t)
	db := setupRecoveryTargetRootTestDB(t)
	seedRecoveryTargetRootNode(t, db, 51, false)
	seedRecoveryTargetRootNode(t, db, 52, true)
	service := NewService(db)
	baseline := completeRecoveryTargetRootDefinitionForTest(RecoveryTargetRootDefinition{
		NodeID: 51, RootID: "root-a", SafeLabel: "FAKE_VALID_RECOVERY_ROOT_LABEL_FOR_TEST_ONLY",
		Locator: "/srv/FAKE_VALID_RECOVERY_ROOT_FOR_TEST_ONLY",
	})
	registerRecoveryTargetRootForTest(t, db, service, baseline)
	baselineKey := recoveryTargetRootTestKey(baseline.NodeID, baseline.RootID)
	baselineValue := rawRecoveryTargetRootValue(t, db, baselineKey)

	tests := []struct {
		name    string
		wantErr error
		mutate  func(*RecoveryTargetRootDefinition)
	}{
		{name: "zero node", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.NodeID = 0 }},
		{name: "missing node", wantErr: ErrRecoveryTargetRootNotFound, mutate: func(value *RecoveryTargetRootDefinition) { value.NodeID = 999 }},
		{name: "archived node", wantErr: ErrRecoveryTargetRootNotFound, mutate: func(value *RecoveryTargetRootDefinition) { value.NodeID = 52 }},
		{name: "empty root ID", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.RootID = "" }},
		{name: "uppercase root ID", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.RootID = "Root-a" }},
		{name: "root ID begins punctuation", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.RootID = "-root" }},
		{name: "root ID ends punctuation", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.RootID = "root_" }},
		{name: "root ID contains dot", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.RootID = "root.a" }},
		{name: "overlong root ID", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.RootID = strings.Repeat("a", 33) }},
		{name: "empty label", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.SafeLabel = "" }},
		{name: "control label", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.SafeLabel = "FAKE_LABEL_\x1f_FOR_TEST_ONLY" }},
		{name: "overlong label", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.SafeLabel = strings.Repeat("a", 129) }},
		{name: "empty locator", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.Locator = "" }},
		{name: "filesystem root", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.Locator = "/" }},
		{name: "relative locator", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.Locator = "srv/recovery" }},
		{name: "cleaned but unequal", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.Locator = "/srv/target/../recovery" }},
		{name: "double slash", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.Locator = "/srv//recovery" }},
		{name: "trailing slash", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.Locator = "/srv/recovery/" }},
		{name: "dot component", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.Locator = "/srv/./recovery" }},
		{name: "dot-dot component", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.Locator = "/srv/../recovery" }},
		{name: "backslash", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.Locator = "/srv/FAKE\\recovery" }},
		{name: "NUL", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.Locator = "/srv/FAKE_\x00_FOR_TEST_ONLY" }},
		{name: "control locator", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.Locator = "/srv/FAKE_\x1f_FOR_TEST_ONLY" }},
		{name: "invalid UTF-8", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.Locator = string([]byte{'/', 0xff}) }},
		{name: "overlong locator", wantErr: ErrRecoveryTargetRootInvalid, mutate: func(value *RecoveryTargetRootDefinition) { value.Locator = "/" + strings.Repeat("a", 4096) }},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			definition := baseline
			testCase.mutate(&definition)
			var registerErr error
			if err := db.Transaction(func(tx *gorm.DB) error {
				_, registerErr = service.RegisterRecoveryTargetRootTx(context.Background(), tx, definition)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(registerErr, testCase.wantErr) || registerErr.Error() != testCase.wantErr.Error() {
				t.Fatalf("registration error=%v, want safe %v", registerErr, testCase.wantErr)
			}
			var count int64
			if err := db.Model(&model.SystemSetting{}).Where("key LIKE ?", RecoveryTargetRootKeyPrefix+"%").Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 1 || rawRecoveryTargetRootValue(t, db, baselineKey) != baselineValue {
				t.Fatalf("invalid registration mutated private rows: count=%d", count)
			}
		})
	}
}

func TestRecoveryTargetRootRegistryRejectsInvalidStoredRecords(t *testing.T) {
	setRecoveryTargetRootTestEncryption(t)
	tests := []struct {
		name  string
		value func(t *testing.T, validDocument, validCiphertext, siblingCiphertext string) string
	}{
		{name: "empty", value: func(_ *testing.T, _, _, _ string) string { return "" }},
		{name: "plaintext", value: func(_ *testing.T, valid, _, _ string) string { return valid }},
		{name: "valid legacy v1", value: func(_ *testing.T, _, valid, _ string) string { return strings.Replace(valid, "enc:v2:", "enc:v1:", 1) }},
		{name: "corrupt v2", value: func(_ *testing.T, _, _, _ string) string {
			return "enc:v2:FAKE_CORRUPT_RECOVERY_ROOT_CIPHERTEXT_FOR_TEST_ONLY"
		}},
		{name: "legacy schema", value: func(t *testing.T, valid, _, _ string) string {
			return encryptRecoveryTargetRootDocumentForTest(t, mutateRecoveryTargetRootDocumentForTest(t, valid, func(value map[string]any) { value["schema_version"] = 1 }))
		}},
		{name: "unknown field", value: func(t *testing.T, valid, _, _ string) string {
			return encryptRecoveryTargetRootDocumentForTest(t, mutateRecoveryTargetRootDocumentForTest(t, valid, func(value map[string]any) { value["unknown"] = "FAKE_UNKNOWN_FOR_TEST_ONLY" }))
		}},
		{name: "duplicate field", value: func(t *testing.T, valid, _, _ string) string {
			duplicate := strings.Replace(valid, `"root_id":"root-a"`, `"root_id":"root-a","root_id":"root-a"`, 1)
			if duplicate == valid {
				t.Fatal("valid document did not contain the expected root_id member")
			}
			return encryptRecoveryTargetRootDocumentForTest(t, duplicate)
		}},
		{name: "missing field", value: func(t *testing.T, valid, _, _ string) string {
			return encryptRecoveryTargetRootDocumentForTest(t, mutateRecoveryTargetRootDocumentForTest(t, valid, func(value map[string]any) { delete(value, "safe_label") }))
		}},
		{name: "trailing document", value: func(t *testing.T, valid, _, _ string) string {
			return encryptRecoveryTargetRootDocumentForTest(t, valid+` {}`)
		}},
		{name: "oversized plaintext", value: func(t *testing.T, valid, _, _ string) string {
			oversized := mutateRecoveryTargetRootDocumentForTest(t, valid, func(value map[string]any) { value["safe_label"] = strings.Repeat("x", 9<<10) })
			return encryptRecoveryTargetRootDocumentForTest(t, oversized)
		}},
		{name: "wrong node", value: func(t *testing.T, valid, _, _ string) string {
			return encryptRecoveryTargetRootDocumentForTest(t, mutateRecoveryTargetRootDocumentForTest(t, valid, func(value map[string]any) { value["node_id"] = 62 }))
		}},
		{name: "wrong root", value: func(t *testing.T, valid, _, _ string) string {
			return encryptRecoveryTargetRootDocumentForTest(t, mutateRecoveryTargetRootDocumentForTest(t, valid, func(value map[string]any) { value["root_id"] = "root-z" }))
		}},
		{name: "swapped ciphertext", value: func(_ *testing.T, _, _, sibling string) string { return sibling }},
		{name: "wrong digest", value: func(t *testing.T, valid, _, _ string) string {
			return encryptRecoveryTargetRootDocumentForTest(t, mutateRecoveryTargetRootDocumentForTest(t, valid, func(value map[string]any) { value["locator_digest"] = strings.Repeat("0", 64) }))
		}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			db := setupRecoveryTargetRootTestDB(t)
			seedRecoveryTargetRootNode(t, db, 61, false)
			service := NewService(db)
			definition := RecoveryTargetRootDefinition{
				NodeID: 61, RootID: "root-a", SafeLabel: "FAKE_STORED_ROOT_A_LABEL_FOR_TEST_ONLY",
				Locator: "/srv/FAKE_STORED_ROOT_A_FOR_TEST_ONLY",
			}
			sibling := RecoveryTargetRootDefinition{
				NodeID: 61, RootID: "root-b", SafeLabel: "FAKE_STORED_ROOT_B_LABEL_FOR_TEST_ONLY",
				Locator: "/srv/FAKE_STORED_ROOT_B_FOR_TEST_ONLY",
			}
			registerRecoveryTargetRootForTest(t, db, service, definition)
			registerRecoveryTargetRootForTest(t, db, service, sibling)
			key := recoveryTargetRootTestKey(definition.NodeID, definition.RootID)
			validCiphertext := rawRecoveryTargetRootValue(t, db, key)
			validDocument, err := secure.DecryptString(validCiphertext)
			if err != nil {
				t.Fatal(err)
			}
			siblingCiphertext := rawRecoveryTargetRootValue(t, db, recoveryTargetRootTestKey(sibling.NodeID, sibling.RootID))
			invalidValue := testCase.value(t, validDocument, validCiphertext, siblingCiphertext)
			if err := db.Model(&model.SystemSetting{}).Where("key = ?", key).UpdateColumn("value", invalidValue).Error; err != nil {
				t.Fatal(err)
			}

			resolved, resolveErr := resolveRecoveryTargetRootForTest(t, db, service, definition.NodeID, definition.RootID)
			if !errors.Is(resolveErr, ErrRecoveryTargetRootUnavailable) || resolveErr.Error() != ErrRecoveryTargetRootUnavailable.Error() {
				t.Fatalf("resolve error=%v, want safe private-state error", resolveErr)
			}
			if resolved.Locator != "" || resolved.LocatorDigest != "" || resolved.SafeLabel != "" {
				t.Fatalf("invalid record returned private resolution: %+v", resolved)
			}
			summaries, listErr := service.ListRecoveryTargetRoots(context.Background(), definition.NodeID)
			if !errors.Is(listErr, ErrRecoveryTargetRootUnavailable) || listErr.Error() != ErrRecoveryTargetRootUnavailable.Error() || len(summaries) != 0 {
				t.Fatalf("list returned partial/private state: summaries=%+v error=%v", summaries, listErr)
			}
			for _, private := range []string{definition.Locator, definition.SafeLabel, key, invalidValue} {
				if private != "" && (strings.Contains(resolveErr.Error(), private) || strings.Contains(listErr.Error(), private)) {
					t.Fatalf("private record material leaked through error")
				}
			}
		})
	}
}

func TestSettingsListAllRecoveryTargetRootsIsBoundedAndSafe(t *testing.T) {
	setRecoveryTargetRootTestEncryption(t)
	db := setupRecoveryTargetRootTestDB(t)
	seedRecoveryTargetRootNode(t, db, 81, false)
	seedRecoveryTargetRootNode(t, db, 82, false)
	seedRecoveryTargetRootNode(t, db, 83, true)
	service := NewService(db)
	registerRecoveryTargetRootForTest(t, db, service, RecoveryTargetRootDefinition{
		NodeID: 82, RootID: "root-b", SafeLabel: "FAKE_ALL_ROOT_B_LABEL_FOR_TEST_ONLY", Locator: "/srv/FAKE_ALL_ROOT_B_FOR_TEST_ONLY",
	})
	registerRecoveryTargetRootForTest(t, db, service, RecoveryTargetRootDefinition{
		NodeID: 81, RootID: "root-c", SafeLabel: "FAKE_ALL_ROOT_C_LABEL_FOR_TEST_ONLY", Locator: "/srv/FAKE_ALL_ROOT_C_FOR_TEST_ONLY",
	})
	registerRecoveryTargetRootForTest(t, db, service, RecoveryTargetRootDefinition{
		NodeID: 81, RootID: "root-a", SafeLabel: "FAKE_ALL_ROOT_A_LABEL_FOR_TEST_ONLY", Locator: "/srv/FAKE_ALL_ROOT_A_FOR_TEST_ONLY",
	})

	roots, err := service.ListAllRecoveryTargetRoots(context.Background())
	if err != nil {
		t.Fatalf("list all recovery roots: %v", err)
	}
	want := []RecoveryTargetRootReference{{NodeID: 81, RootID: "root-a"}, {NodeID: 81, RootID: "root-c"}, {NodeID: 82, RootID: "root-b"}}
	if !reflect.DeepEqual(roots, want) {
		t.Fatalf("all recovery roots=%+v, want %+v", roots, want)
	}
	encoded, err := json.Marshal(roots)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"FAKE_ALL_ROOT_A_LABEL_FOR_TEST_ONLY", "/srv/FAKE_ALL_ROOT_A_FOR_TEST_ONLY"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("all-root catalog leaked private value %q: %s", private, encoded)
		}
	}
	t.Run("inactive node row fails closed", func(t *testing.T) {
		if err := db.Model(&model.Node{}).Where("id = ?", 82).UpdateColumn("archived", true).Error; err != nil {
			t.Fatal(err)
		}
		if roots, err := service.ListAllRecoveryTargetRoots(context.Background()); !errors.Is(err, ErrRecoveryTargetRootUnavailable) || len(roots) != 0 {
			t.Fatalf("inactive-node catalog roots=%+v error=%v", roots, err)
		}
		if err := db.Model(&model.Node{}).Where("id = ?", 82).UpdateColumn("archived", false).Error; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("malformed duplicate identity fails closed", func(t *testing.T) {
		valid := rawRecoveryTargetRootValue(t, db, recoveryTargetRootTestKey(81, "root-a"))
		row := model.SystemSetting{Key: RecoveryTargetRootKeyPrefix + "081.root-a", Value: valid}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if roots, err := service.ListAllRecoveryTargetRoots(context.Background()); !errors.Is(err, ErrRecoveryTargetRootUnavailable) || len(roots) != 0 {
			t.Fatalf("malformed duplicate catalog roots=%+v error=%v", roots, err)
		}
		if err := db.Delete(&row).Error; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("limit plus one fails instead of truncating", func(t *testing.T) {
		limitDB := setupRecoveryTargetRootTestDB(t)
		seedRecoveryTargetRootNode(t, limitDB, 91, false)
		rows := make([]model.SystemSetting, 0, 1025)
		for index := 0; index < 1025; index++ {
			rows = append(rows, model.SystemSetting{
				Key:   RecoveryTargetRootKeyPrefix + "91.r" + strconv.FormatInt(int64(index), 10),
				Value: "enc:v2:FAKE_LIMIT_ROW_FOR_TEST_ONLY",
			})
		}
		if err := limitDB.CreateInBatches(rows, 128).Error; err != nil {
			t.Fatal(err)
		}
		limitService := NewService(limitDB)
		if roots, err := limitService.ListAllRecoveryTargetRoots(context.Background()); !errors.Is(err, ErrRecoveryTargetRootUnavailable) || len(roots) != 0 {
			t.Fatalf("limit+1 catalog roots=%d error=%v", len(roots), err)
		}
	})
}

func TestRecoveryTargetRootRegistryKeysNeverUseGenericSettings(t *testing.T) {
	setRecoveryTargetRootTestEncryption(t)
	db := setupRecoveryTargetRootTestDB(t)
	seedRecoveryTargetRootNode(t, db, 71, false)
	service := NewService(db)
	definition := RecoveryTargetRootDefinition{
		NodeID: 71, RootID: "root-a", SafeLabel: "FAKE_GENERIC_ROOT_LABEL_FOR_TEST_ONLY",
		Locator: "/srv/FAKE_GENERIC_ROOT_FOR_TEST_ONLY",
	}
	registerRecoveryTargetRootForTest(t, db, service, definition)
	key := recoveryTargetRootTestKey(definition.NodeID, definition.RootID)
	original := rawRecoveryTargetRootValue(t, db, key)
	malformedKey := RecoveryTargetRootKeyPrefix + "malformed"
	if err := db.Create(&model.SystemSetting{Key: malformedKey, Value: "enc:v2:FAKE_MALFORMED_INTERNAL_ROW_FOR_TEST_ONLY"}).Error; err != nil {
		t.Fatal(err)
	}
	receiptKey := RecoveryTargetRootReceiptKeyPrefix + strings.Repeat("a", 64)
	receiptValue := "FAKE_PRIVATE_RECOVERY_TARGET_ROOT_RECEIPT_FOR_TEST_ONLY"
	if err := db.Create(&model.SystemSetting{Key: receiptKey, Value: receiptValue}).Error; err != nil {
		t.Fatal(err)
	}
	downgradeReceiptKey := RecoveryDowngradeReceiptKeyPrefix + strings.Repeat("b", 64)
	downgradeReceiptValue := "FAKE_PRIVATE_RECOVERY_DOWNGRADE_RECEIPT_FOR_TEST_ONLY"
	if err := db.Create(&model.SystemSetting{Key: downgradeReceiptKey, Value: downgradeReceiptValue}).Error; err != nil {
		t.Fatal(err)
	}

	internalKeys := []string{
		ProcessingContentPipelineRevisionKey,
		ProcessingOCRPipelineRevisionKey,
		key,
		malformedKey,
		RecoveryTargetRootKeyPrefix + ".",
		receiptKey,
		RecoveryTargetRootReceiptKeyPrefix + ".",
		downgradeReceiptKey,
		RecoveryDowngradeReceiptKeyPrefix + ".",
	}
	for _, internalKey := range internalKeys {
		if !IsInternalSettingKey(internalKey) {
			t.Fatalf("internal key was not classified: %q", internalKey)
		}
	}
	for _, lookalike := range []string{
		"backup_assets.internal.recovery_target_root.v1",
		"backup_assets.internal.recovery_target_root.v10.71.root-a",
		"backup_assets.internal.recovery_target_root.v1x.71.root-a",
		"backup_assets.internal.recovery_target_roots.v1.71.root-a",
		"backup_assets.internal.recovery_root_receipt.v1",
		"backup_assets.internal.recovery_root_receipt.v10." + strings.Repeat("a", 64),
		"backup_assets.internal.recovery_root_receipt.v1x." + strings.Repeat("a", 64),
		"backup_assets.internal.recovery_downgrade_receipt.v1",
		"backup_assets.internal.recovery_downgrade_receipt.v10." + strings.Repeat("b", 64),
		"backup_assets.internal.recovery_downgrade_receipt.v1x." + strings.Repeat("b", 64),
	} {
		if IsInternalSettingKey(lookalike) {
			t.Fatalf("lookalike key was classified internal: %q", lookalike)
		}
	}

	for _, definition := range service.Registry() {
		if IsInternalSettingKey(definition.Key) {
			t.Fatalf("internal key appeared in Registry: %q", definition.Key)
		}
	}
	all, err := service.GetAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, internalKey := range []string{key, malformedKey, receiptKey, downgradeReceiptKey} {
		if _, ok := all[internalKey]; ok {
			t.Fatalf("internal key appeared in GetAll: %q", internalKey)
		}
		if got := service.GetEffective(internalKey); got != "" {
			t.Fatalf("GetEffective returned internal value for %q", internalKey)
		}
		if _, resolveErr := service.resolveValue(internalKey); !errors.Is(resolveErr, ErrInternalSettingUnavailable) {
			t.Fatalf("resolveValue error=%v for %q", resolveErr, internalKey)
		}
		for operation, operationErr := range map[string]error{
			"Validate":     service.Validate(internalKey, "FAKE_REPLACEMENT_FOR_TEST_ONLY"),
			"Update":       service.Update(internalKey, "FAKE_REPLACEMENT_FOR_TEST_ONLY"),
			"UpdateWithTx": service.UpdateWithTx(db, internalKey, "FAKE_REPLACEMENT_FOR_TEST_ONLY"),
			"UpdateMany":   service.UpdateMany(map[string]string{internalKey: "FAKE_REPLACEMENT_FOR_TEST_ONLY"}),
			"Delete":       service.Delete(internalKey),
			"DeleteWithTx": service.DeleteWithTx(db, internalKey),
		} {
			if !errors.Is(operationErr, ErrInternalSettingUnavailable) || operationErr.Error() != ErrInternalSettingUnavailable.Error() {
				t.Fatalf("%s error=%v for internal key %q", operation, operationErr, internalKey)
			}
		}
	}
	if got := rawRecoveryTargetRootValue(t, db, key); got != original {
		t.Fatal("generic settings operation changed the private root row")
	}
	var receipt model.SystemSetting
	if err := db.Where("key = ?", receiptKey).Take(&receipt).Error; err != nil || receipt.Value != receiptValue {
		t.Fatal("generic settings operation changed the private mutation receipt")
	}
	var downgradeReceipt model.SystemSetting
	if err := db.Where("key = ?", downgradeReceiptKey).Take(&downgradeReceipt).Error; err != nil ||
		downgradeReceipt.Value != downgradeReceiptValue {
		t.Fatal("generic settings operation changed the private downgrade receipt")
	}
}

func TestProcessingPipelineRevisionsUseReservedTransactionalState(t *testing.T) {
	db := setupTestDB(t)
	service := NewService(db)
	var first ProcessingPipelineRevisions
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		first, err = service.AdvanceProcessingPipelineRevisionsTx(context.Background(), tx, true, true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if first.Content != 1 || first.OCR != 1 {
		t.Fatalf("initial revisions=%+v", first)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := service.AdvanceProcessingPipelineRevisionsTx(context.Background(), tx, false, true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	current, err := service.ProcessingPipelineRevisions(context.Background())
	if err != nil || current.Content != 1 || current.OCR != 2 {
		t.Fatalf("current revisions=%+v error=%v", current, err)
	}
}

func TestProcessingPipelineRevisionKeysAreNeverPublicSettings(t *testing.T) {
	db := setupTestDB(t)
	service := NewService(db)
	keys := []string{ProcessingContentPipelineRevisionKey, ProcessingOCRPipelineRevisionKey}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := service.AdvanceProcessingPipelineRevisionsTx(context.Background(), tx, true, true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	definitions := service.Registry()
	all, err := service.GetAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		for _, definition := range definitions {
			if definition.Key == key {
				t.Fatalf("reserved key appeared in Registry: %s", key)
			}
		}
		if _, ok := all[key]; ok || service.GetEffective(key) != "" {
			t.Fatalf("reserved key appeared in public resolution: %s all=%v effective=%q", key, ok, service.GetEffective(key))
		}
		if err := service.Validate(key, "9"); err == nil {
			t.Fatalf("reserved key passed Validate: %s", key)
		}
		if err := service.Update(key, "9"); err == nil {
			t.Fatalf("reserved key passed Update: %s", key)
		}
		if err := service.UpdateWithTx(db, key, "9"); err == nil {
			t.Fatalf("reserved key passed UpdateWithTx: %s", key)
		}
		if !IsInternalSettingKey(key) {
			t.Fatalf("reserved key not classified internal: %s", key)
		}
	}
	if err := db.Model(&model.SystemSetting{}).Where("key = ?", ProcessingOCRPipelineRevisionKey).Update("value", "invalid").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProcessingPipelineRevisions(context.Background()); !errors.Is(err, ErrInternalSettingUnavailable) {
		t.Fatalf("malformed internal state error=%v", err)
	}
}

type expectedProcessingSettingDefinition struct {
	env             string
	defaultValue    string
	settingType     SettingType
	min             string
	max             string
	minDuration     string
	maxDuration     string
	requiresRestart bool
	sensitive       bool
}

var expectedBackupAssetProcessingDefinitions = map[string]expectedProcessingSettingDefinition{
	"backup_assets.processing_queue_max":                       {"BACKUP_ASSETS_PROCESSING_QUEUE_MAX", "10000", TypeInt, "1", "100000", "", "", false, false},
	"backup_assets.processing_interactive_slots":               {"BACKUP_ASSETS_PROCESSING_INTERACTIVE_SLOTS", "2", TypeInt, "1", "64", "", "", false, false},
	"backup_assets.processing_background_slots":                {"BACKUP_ASSETS_PROCESSING_BACKGROUND_SLOTS", "2", TypeInt, "1", "64", "", "", false, false},
	"backup_assets.processing_pull_lease":                      {"BACKUP_ASSETS_PROCESSING_PULL_LEASE", "90s", TypeDuration, "", "", "15s", "5m", false, false},
	"backup_assets.processing_pull_heartbeat":                  {"BACKUP_ASSETS_PROCESSING_PULL_HEARTBEAT", "20s", TypeDuration, "", "", "5s", "1m", false, false},
	"backup_assets.processing_attempt_timeout":                 {"BACKUP_ASSETS_PROCESSING_ATTEMPT_TIMEOUT", "2h", TypeDuration, "", "", "1m", "24h", false, false},
	"backup_assets.processing_retry_max":                       {"BACKUP_ASSETS_PROCESSING_RETRY_MAX", "5", TypeInt, "0", "20", "", "", false, false},
	"backup_assets.processing_retry_base":                      {"BACKUP_ASSETS_PROCESSING_RETRY_BASE", "5s", TypeDuration, "", "", "1s", "5m", false, false},
	"backup_assets.processing_retry_max_delay":                 {"BACKUP_ASSETS_PROCESSING_RETRY_MAX_DELAY", "15m", TypeDuration, "", "", "1s", "2h", false, false},
	"backup_assets.processing_input_request_max_bytes":         {"BACKUP_ASSETS_PROCESSING_INPUT_REQUEST_MAX_BYTES", "67108864", TypeInt, "65536", "1073741824", "", "", false, false},
	"backup_assets.processing_input_cumulative_max_bytes":      {"BACKUP_ASSETS_PROCESSING_INPUT_CUMULATIVE_MAX_BYTES", "2147483648", TypeInt, "65536", "17179869184", "", "", false, false},
	"backup_assets.processing_input_max_requests":              {"BACKUP_ASSETS_PROCESSING_INPUT_MAX_REQUESTS", "512", TypeInt, "1", "4096", "", "", false, false},
	"backup_assets.processing_input_max_in_flight":             {"BACKUP_ASSETS_PROCESSING_INPUT_MAX_IN_FLIGHT", "4", TypeInt, "1", "32", "", "", false, false},
	"backup_assets.processing_sink_max_artifacts":              {"BACKUP_ASSETS_PROCESSING_SINK_MAX_ARTIFACTS", "32", TypeInt, "1", "256", "", "", false, false},
	"backup_assets.processing_sink_artifact_max_bytes":         {"BACKUP_ASSETS_PROCESSING_SINK_ARTIFACT_MAX_BYTES", "536870912", TypeInt, "65536", "4294967296", "", "", false, false},
	"backup_assets.processing_sink_total_max_bytes":            {"BACKUP_ASSETS_PROCESSING_SINK_TOTAL_MAX_BYTES", "1073741824", TypeInt, "65536", "17179869184", "", "", false, false},
	"backup_assets.processing_protocol_json_max_bytes":         {"BACKUP_ASSETS_PROCESSING_PROTOCOL_JSON_MAX_BYTES", "65536", TypeInt, "4096", "1048576", "", "", false, false},
	"backup_assets.processing_secret_classify":                 {"BACKUP_ASSETS_PROCESSING_SECRET_CLASSIFY", "false", TypeBool, "", "", "", "", false, false},
	"backup_assets.processing_backfill_paused":                 {"BACKUP_ASSETS_PROCESSING_BACKFILL_PAUSED", "true", TypeBool, "", "", "", "", false, false},
	"backup_assets.processing_backfill_batch_size":             {"BACKUP_ASSETS_PROCESSING_BACKFILL_BATCH_SIZE", "100", TypeInt, "1", "10000", "", "", false, false},
	"backup_assets.processing_backfill_jobs_per_hour":          {"BACKUP_ASSETS_PROCESSING_BACKFILL_JOBS_PER_HOUR", "1000", TypeInt, "1", "100000", "", "", false, false},
	"backup_assets.processing_backfill_bytes_per_hour":         {"BACKUP_ASSETS_PROCESSING_BACKFILL_BYTES_PER_HOUR", "10737418240", TypeInt, "65536", "1099511627776", "", "", false, false},
	"backup_assets.processing_backfill_provider_concurrency":   {"BACKUP_ASSETS_PROCESSING_BACKFILL_PROVIDER_CONCURRENCY", "1", TypeInt, "1", "32", "", "", false, false},
	"backup_assets.processing_backfill_capability_concurrency": {"BACKUP_ASSETS_PROCESSING_BACKFILL_CAPABILITY_CONCURRENCY", "1", TypeInt, "1", "32", "", "", false, false},
	"backup_assets.processing_backfill_recent_window":          {"BACKUP_ASSETS_PROCESSING_BACKFILL_RECENT_WINDOW", "720h", TypeDuration, "", "", "24h", "8760h", false, false},
	"backup_assets.processing_backfill_history_aging_step":     {"BACKUP_ASSETS_PROCESSING_BACKFILL_HISTORY_AGING_STEP", "24h", TypeDuration, "", "", "1h", "720h", false, false},
	"backup_assets.worker_local_enabled":                       {"BACKUP_ASSETS_WORKER_LOCAL_ENABLED", "false", TypeBool, "", "", "", "", true, false},
	"backup_assets.worker_local_socket":                        {"BACKUP_ASSETS_WORKER_LOCAL_SOCKET", "/run/xirang/asset-worker.sock", TypeString, "", "", "", "", true, false},
	"backup_assets.worker_remote_enabled":                      {"BACKUP_ASSETS_WORKER_REMOTE_ENABLED", "false", TypeBool, "", "", "", "", true, false},
	"backup_assets.worker_remote_listen_addr":                  {"BACKUP_ASSETS_WORKER_REMOTE_LISTEN_ADDR", "", TypeString, "", "", "", "", true, false},
	"backup_assets.worker_remote_server_cert_file":             {"BACKUP_ASSETS_WORKER_REMOTE_SERVER_CERT_FILE", "", TypeString, "", "", "", "", true, true},
	"backup_assets.worker_remote_server_key_file":              {"BACKUP_ASSETS_WORKER_REMOTE_SERVER_KEY_FILE", "", TypeString, "", "", "", "", true, true},
	"backup_assets.worker_remote_client_ca_file":               {"BACKUP_ASSETS_WORKER_REMOTE_CLIENT_CA_FILE", "", TypeString, "", "", "", "", true, true},
	"backup_assets.worker_remote_trust_domain":                 {"BACKUP_ASSETS_WORKER_REMOTE_TRUST_DOMAIN", "", TypeString, "", "", "", "", true, true},
	"backup_assets.worker_updater_enabled":                     {"BACKUP_ASSETS_WORKER_UPDATER_ENABLED", "false", TypeBool, "", "", "", "", true, false},
	"backup_assets.worker_updater_online_enabled":              {"BACKUP_ASSETS_WORKER_UPDATER_ONLINE_ENABLED", "false", TypeBool, "", "", "", "", true, false},
	"backup_assets.worker_updater_online_origins":              {"BACKUP_ASSETS_WORKER_UPDATER_ONLINE_ORIGINS", "", TypeString, "", "", "", "", true, false},
	"backup_assets.derived_store_root":                         {"BACKUP_ASSETS_DERIVED_STORE_ROOT", "/var/lib/xirang-asset-runtime/derived", TypeString, "", "", "", "", true, false},
	"backup_assets.derived_store_chunk_bytes":                  {"BACKUP_ASSETS_DERIVED_STORE_CHUNK_BYTES", "1048576", TypeInt, "65536", "8388608", "", "", true, false},
	"backup_assets.derived_store_blob_max_bytes":               {"BACKUP_ASSETS_DERIVED_STORE_BLOB_MAX_BYTES", "4294967296", TypeInt, "65536", "17179869184", "", "", false, false},
	"backup_assets.derived_store_global_max_bytes":             {"BACKUP_ASSETS_DERIVED_STORE_GLOBAL_MAX_BYTES", "107374182400", TypeInt, "65536", "1099511627776", "", "", false, false},
	"backup_assets.derived_store_reconcile_interval":           {"BACKUP_ASSETS_DERIVED_STORE_RECONCILE_INTERVAL", "15m", TypeDuration, "", "", "1m", "24h", false, false},
	"backup_assets.derived_store_reconcile_batch_size":         {"BACKUP_ASSETS_DERIVED_STORE_RECONCILE_BATCH_SIZE", "256", TypeInt, "1", "10000", "", "", false, false},
}

var expectedBackupAssetExportDefinitions = map[string]expectedProcessingSettingDefinition{
	"backup_assets.export.enabled":                     {"BACKUP_ASSETS_EXPORT_ENABLED", "false", TypeBool, "", "", "", "", false, false},
	"backup_assets.export.root":                        {"BACKUP_ASSETS_EXPORT_ROOT", "/var/lib/xirang-asset-runtime/export", TypeString, "", "", "", "", true, false},
	"backup_assets.export.default_profile":             {"BACKUP_ASSETS_EXPORT_DEFAULT_PROFILE", "zip_deflate_v1", TypeString, "", "", "", "", false, false},
	"backup_assets.export.chunk_bytes":                 {"BACKUP_ASSETS_EXPORT_CHUNK_BYTES", "1048576", TypeInt, "65536", "8388608", "", "", false, false},
	"backup_assets.export.max_items":                   {"BACKUP_ASSETS_EXPORT_MAX_ITEMS", "10000", TypeInt, "1", "100000", "", "", false, false},
	"backup_assets.export.max_source_points":           {"BACKUP_ASSETS_EXPORT_MAX_SOURCE_POINTS", "128", TypeInt, "1", "1024", "", "", false, false},
	"backup_assets.export.max_item_bytes":              {"BACKUP_ASSETS_EXPORT_MAX_ITEM_BYTES", "2147483648", TypeInt, "65536", "274877906944", "", "", false, false},
	"backup_assets.export.max_logical_bytes":           {"BACKUP_ASSETS_EXPORT_MAX_LOGICAL_BYTES", "10737418240", TypeInt, "65536", "1099511627776", "", "", false, false},
	"backup_assets.export.max_provider_bytes":          {"BACKUP_ASSETS_EXPORT_MAX_PROVIDER_BYTES", "21474836480", TypeInt, "65536", "2199023255552", "", "", false, false},
	"backup_assets.export.max_ciphertext_bytes":        {"BACKUP_ASSETS_EXPORT_MAX_CIPHERTEXT_BYTES", "12884901888", TypeInt, "65536", "1374389534720", "", "", false, false},
	"backup_assets.export.user_active_jobs":            {"BACKUP_ASSETS_EXPORT_USER_ACTIVE_JOBS", "2", TypeInt, "1", "16", "", "", false, false},
	"backup_assets.export.global_active_jobs":          {"BACKUP_ASSETS_EXPORT_GLOBAL_ACTIVE_JOBS", "8", TypeInt, "1", "64", "", "", false, false},
	"backup_assets.export.worker_concurrency":          {"BACKUP_ASSETS_EXPORT_WORKER_CONCURRENCY", "2", TypeInt, "1", "16", "", "", false, false},
	"backup_assets.export.max_open_readers":            {"BACKUP_ASSETS_EXPORT_MAX_OPEN_READERS", "2", TypeInt, "1", "8", "", "", false, false},
	"backup_assets.export.max_duration":                {"BACKUP_ASSETS_EXPORT_MAX_DURATION", "2h", TypeDuration, "", "", "5m", "24h", false, false},
	"backup_assets.export.max_attempts":                {"BACKUP_ASSETS_EXPORT_MAX_ATTEMPTS", "3", TypeInt, "1", "10", "", "", false, false},
	"backup_assets.export.retry_base":                  {"BACKUP_ASSETS_EXPORT_RETRY_BASE", "5s", TypeDuration, "", "", "1s", "1m", false, false},
	"backup_assets.export.retry_max_delay":             {"BACKUP_ASSETS_EXPORT_RETRY_MAX_DELAY", "5m", TypeDuration, "", "", "5s", "30m", false, false},
	"backup_assets.export.lease_ttl":                   {"BACKUP_ASSETS_EXPORT_LEASE_TTL", "90s", TypeDuration, "", "", "30s", "5m", false, false},
	"backup_assets.export.lease_renew_margin":          {"BACKUP_ASSETS_EXPORT_LEASE_RENEW_MARGIN", "20s", TypeDuration, "", "", "5s", "2m", false, false},
	"backup_assets.export.ready_ttl":                   {"BACKUP_ASSETS_EXPORT_READY_TTL", "24h", TypeDuration, "", "", "15m", "168h", false, false},
	"backup_assets.export.summary_ttl":                 {"BACKUP_ASSETS_EXPORT_SUMMARY_TTL", "2160h", TypeDuration, "", "", "24h", "8760h", false, false},
	"backup_assets.export.ticket_ttl":                  {"BACKUP_ASSETS_EXPORT_TICKET_TTL", "5m", TypeDuration, "", "", "30s", "15m", false, false},
	"backup_assets.export.ticket_max_requests":         {"BACKUP_ASSETS_EXPORT_TICKET_MAX_REQUESTS", "256", TypeInt, "1", "4096", "", "", false, false},
	"backup_assets.export.ticket_max_in_flight":        {"BACKUP_ASSETS_EXPORT_TICKET_MAX_IN_FLIGHT", "2", TypeInt, "1", "8", "", "", false, false},
	"backup_assets.export.ticket_max_cumulative_bytes": {"BACKUP_ASSETS_EXPORT_TICKET_MAX_CUMULATIVE_BYTES", "25769803776", TypeInt, "65536", "2748779069440", "", "", false, false},
	"backup_assets.export.user_store_quota":            {"BACKUP_ASSETS_EXPORT_USER_STORE_QUOTA", "26843545600", TypeInt, "1073741824", "2199023255552", "", "", false, false},
	"backup_assets.export.store_quota":                 {"BACKUP_ASSETS_EXPORT_STORE_QUOTA", "107374182400", TypeInt, "1073741824", "10995116277760", "", "", false, false},
	"backup_assets.export.gc_cadence":                  {"BACKUP_ASSETS_EXPORT_GC_CADENCE", "5m", TypeDuration, "", "", "30s", "1h", false, false},
	"backup_assets.export.reconcile_batch_size":        {"BACKUP_ASSETS_EXPORT_RECONCILE_BATCH_SIZE", "100", TypeInt, "1", "1000", "", "", false, false},
	"backup_assets.archive.member_ttl":                 {"BACKUP_ASSETS_ARCHIVE_MEMBER_TTL", "1h", TypeDuration, "", "", "5m", "24h", false, false},
	"backup_assets.archive.max_expanded_bytes":         {"BACKUP_ASSETS_ARCHIVE_MAX_EXPANDED_BYTES", "8589934592", TypeInt, "1048576", "8589934592", "", "", false, false},
	"backup_assets.archive.member_max_bytes":           {"BACKUP_ASSETS_ARCHIVE_MEMBER_MAX_BYTES", "268435456", TypeInt, "65536", "268435456", "", "", false, false},
	"backup_assets.archive.max_entries":                {"BACKUP_ASSETS_ARCHIVE_MAX_ENTRIES", "100000", TypeInt, "1", "100000", "", "", false, false},
	"backup_assets.archive.max_depth":                  {"BACKUP_ASSETS_ARCHIVE_MAX_DEPTH", "16", TypeInt, "1", "16", "", "", false, false},
	"backup_assets.archive.max_compression_ratio":      {"BACKUP_ASSETS_ARCHIVE_MAX_COMPRESSION_RATIO", "100", TypeInt, "1", "100", "", "", false, false},
	"backup_assets.archive.max_duration":               {"BACKUP_ASSETS_ARCHIVE_MAX_DURATION", "10m", TypeDuration, "", "", "1s", "10m", false, false},
}

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRegistry(t *testing.T) {
	svc := NewService(setupTestDB(t))
	defs := svc.Registry()
	seenKeys := make(map[string]bool, len(defs))
	seenEnv := make(map[string]bool, len(defs))
	for _, def := range defs {
		if seenKeys[def.Key] {
			t.Fatalf("duplicate setting key %q", def.Key)
		}
		if seenEnv[def.EnvVar] {
			t.Fatalf("duplicate setting env var %q", def.EnvVar)
		}
		seenKeys[def.Key] = true
		seenEnv[def.EnvVar] = true
	}
	// 确认返回副本，不影响全局 registry
	defs[0].Key = "mutated"
	if registry[0].Key == "mutated" {
		t.Error("Registry() should return a copy, not a reference")
	}
}

func TestBackupAssetSearchConfigAndOverlayConfigDefinitionsAndSafeDefaults(t *testing.T) {
	type expectedDefinition struct {
		env          string
		defaultValue string
		settingType  SettingType
		min          string
		max          string
		minDuration  string
		maxDuration  string
	}
	want := map[string]expectedDefinition{
		"backup_assets.enabled":                           {"BACKUP_ASSETS_ENABLED", "false", TypeBool, "", "", "", ""},
		"backup_assets.retention_reconcile_interval":      {"BACKUP_ASSETS_RETENTION_RECONCILE_INTERVAL", "5m", TypeDuration, "", "", "30s", "24h"},
		"backup_assets.retention_batch_size":              {"BACKUP_ASSETS_RETENTION_BATCH_SIZE", "100", TypeInt, "1", "1000", "", ""},
		"backup_assets.retention_drain_timeout":           {"BACKUP_ASSETS_RETENTION_DRAIN_TIMEOUT", "30s", TypeDuration, "", "", "5s", "30m"},
		"backup_assets.content_preview_ttl":               {"BACKUP_ASSETS_CONTENT_PREVIEW_TTL", "2m", TypeDuration, "", "", "15s", "10m"},
		"backup_assets.content_media_ttl":                 {"BACKUP_ASSETS_CONTENT_MEDIA_TTL", "15m", TypeDuration, "", "", "1m", "30m"},
		"backup_assets.content_idle_ttl":                  {"BACKUP_ASSETS_CONTENT_IDLE_TTL", "60s", TypeDuration, "", "", "15s", "10m"},
		"backup_assets.content_write_idle_timeout":        {"BACKUP_ASSETS_CONTENT_WRITE_IDLE_TIMEOUT", "30s", TypeDuration, "", "", "5s", "2m"},
		"backup_assets.content_ticket_timeout":            {"BACKUP_ASSETS_CONTENT_TICKET_TIMEOUT", "20s", TypeDuration, "", "", "1s", "25s"},
		"backup_assets.content_request_max_bytes":         {"BACKUP_ASSETS_CONTENT_REQUEST_MAX_BYTES", "67108864", TypeInt, "65536", "1073741824", "", ""},
		"backup_assets.content_cumulative_max_bytes":      {"BACKUP_ASSETS_CONTENT_CUMULATIVE_MAX_BYTES", "536870912", TypeInt, "65536", "8589934592", "", ""},
		"backup_assets.content_max_requests":              {"BACKUP_ASSETS_CONTENT_MAX_REQUESTS", "256", TypeInt, "1", "4096", "", ""},
		"backup_assets.content_grant_max_in_flight":       {"BACKUP_ASSETS_CONTENT_GRANT_MAX_IN_FLIGHT", "2", TypeInt, "1", "8", "", ""},
		"backup_assets.content_user_max_concurrency":      {"BACKUP_ASSETS_CONTENT_USER_MAX_CONCURRENCY", "4", TypeInt, "1", "32", "", ""},
		"backup_assets.content_provider_max_concurrency":  {"BACKUP_ASSETS_CONTENT_PROVIDER_MAX_CONCURRENCY", "4", TypeInt, "1", "32", "", ""},
		"backup_assets.content_global_max_concurrency":    {"BACKUP_ASSETS_CONTENT_GLOBAL_MAX_CONCURRENCY", "16", TypeInt, "1", "128", "", ""},
		"backup_assets.content_rate_window":               {"BACKUP_ASSETS_CONTENT_RATE_WINDOW", "1m", TypeDuration, "", "", "10s", "10m"},
		"backup_assets.content_user_window_bytes":         {"BACKUP_ASSETS_CONTENT_USER_WINDOW_BYTES", "1073741824", TypeInt, "65536", "17179869184", "", ""},
		"backup_assets.content_provider_window_bytes":     {"BACKUP_ASSETS_CONTENT_PROVIDER_WINDOW_BYTES", "4294967296", TypeInt, "65536", "68719476736", "", ""},
		"backup_assets.content_global_window_bytes":       {"BACKUP_ASSETS_CONTENT_GLOBAL_WINDOW_BYTES", "8589934592", TypeInt, "65536", "137438953472", "", ""},
		"backup_assets.content_user_window_requests":      {"BACKUP_ASSETS_CONTENT_USER_WINDOW_REQUESTS", "1024", TypeInt, "1", "65536", "", ""},
		"backup_assets.content_provider_window_requests":  {"BACKUP_ASSETS_CONTENT_PROVIDER_WINDOW_REQUESTS", "4096", TypeInt, "1", "262144", "", ""},
		"backup_assets.content_global_window_requests":    {"BACKUP_ASSETS_CONTENT_GLOBAL_WINDOW_REQUESTS", "8192", TypeInt, "1", "1048576", "", ""},
		"backup_assets.content_classification_scan_bytes": {"BACKUP_ASSETS_CONTENT_CLASSIFICATION_SCAN_BYTES", "262144", TypeInt, "4096", "4194304", "", ""},
		"backup_assets.content_text_preview_bytes":        {"BACKUP_ASSETS_CONTENT_TEXT_PREVIEW_BYTES", "1048576", TypeInt, "4096", "16777216", "", ""},
		"backup_assets.content_hex_preview_bytes":         {"BACKUP_ASSETS_CONTENT_HEX_PREVIEW_BYTES", "65536", TypeInt, "1024", "1048576", "", ""},
		"backup_assets.content_raster_max_pixels":         {"BACKUP_ASSETS_CONTENT_RASTER_MAX_PIXELS", "100000000", TypeInt, "1000000", "250000000", "", ""},
		"backup_assets.content_memory_global_bytes":       {"BACKUP_ASSETS_CONTENT_MEMORY_GLOBAL_BYTES", "67108864", TypeInt, "1048576", "1073741824", "", ""},
		"backup_assets.content_memory_object_bytes":       {"BACKUP_ASSETS_CONTENT_MEMORY_OBJECT_BYTES", "4194304", TypeInt, "65536", "1073741824", "", ""},
		"backup_assets.content_memory_user_bytes":         {"BACKUP_ASSETS_CONTENT_MEMORY_USER_BYTES", "16777216", TypeInt, "65536", "1073741824", "", ""},
		"backup_assets.content_memory_provider_bytes":     {"BACKUP_ASSETS_CONTENT_MEMORY_PROVIDER_BYTES", "33554432", TypeInt, "65536", "1073741824", "", ""},
		"backup_assets.content_cache_enabled":             {"BACKUP_ASSETS_CONTENT_CACHE_ENABLED", "true", TypeBool, "", "", "", ""},
		"backup_assets.content_cache_root":                {"BACKUP_ASSETS_CONTENT_CACHE_ROOT", "/var/cache/xirang/asset-content", TypeString, "", "", "", ""},
		"backup_assets.content_cache_chunk_bytes":         {"BACKUP_ASSETS_CONTENT_CACHE_CHUNK_BYTES", "1048576", TypeInt, "65536", "8388608", "", ""},
		"backup_assets.content_cache_object_bytes":        {"BACKUP_ASSETS_CONTENT_CACHE_OBJECT_BYTES", "536870912", TypeInt, "65536", "8589934592", "", ""},
		"backup_assets.content_cache_user_bytes":          {"BACKUP_ASSETS_CONTENT_CACHE_USER_BYTES", "2147483648", TypeInt, "65536", "34359738368", "", ""},
		"backup_assets.content_cache_provider_bytes":      {"BACKUP_ASSETS_CONTENT_CACHE_PROVIDER_BYTES", "4294967296", TypeInt, "65536", "68719476736", "", ""},
		"backup_assets.content_cache_global_bytes":        {"BACKUP_ASSETS_CONTENT_CACHE_GLOBAL_BYTES", "8589934592", TypeInt, "65536", "137438953472", "", ""},
		"backup_assets.content_cache_object_files":        {"BACKUP_ASSETS_CONTENT_CACHE_OBJECT_FILES", "1024", TypeInt, "2", "131072", "", ""},
		"backup_assets.content_cache_user_files":          {"BACKUP_ASSETS_CONTENT_CACHE_USER_FILES", "4096", TypeInt, "2", "262144", "", ""},
		"backup_assets.content_cache_provider_files":      {"BACKUP_ASSETS_CONTENT_CACHE_PROVIDER_FILES", "8192", TypeInt, "2", "262144", "", ""},
		"backup_assets.content_cache_global_files":        {"BACKUP_ASSETS_CONTENT_CACHE_GLOBAL_FILES", "16384", TypeInt, "16", "262144", "", ""},
		"backup_assets.content_cache_idle_ttl":            {"BACKUP_ASSETS_CONTENT_CACHE_IDLE_TTL", "15m", TypeDuration, "", "", "1m", "24h"},
		"backup_assets.content_cache_absolute_ttl":        {"BACKUP_ASSETS_CONTENT_CACHE_ABSOLUTE_TTL", "2h", TypeDuration, "", "", "1m", "24h"},
		"backup_assets.content_reconcile_interval":        {"BACKUP_ASSETS_CONTENT_RECONCILE_INTERVAL", "1m", TypeDuration, "", "", "10s", "1h"},
		"backup_assets.content_reconcile_batch_size":      {"BACKUP_ASSETS_CONTENT_RECONCILE_BATCH_SIZE", "100", TypeInt, "1", "1000", "", ""},
		"backup_assets.content_audit_backlog_max":         {"BACKUP_ASSETS_CONTENT_AUDIT_BACKLOG_MAX", "10000", TypeInt, "100", "100000", "", ""},
		"backup_assets.content_allow_insecure_loopback":   {"BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_LOOPBACK", "false", TypeBool, "", "", "", ""},
		"backup_assets.catalog_batch_size":                {"BACKUP_ASSETS_CATALOG_BATCH_SIZE", "2000", TypeInt, "1", "100000", "", ""},
		"backup_assets.catalog_build_timeout":             {"BACKUP_ASSETS_CATALOG_BUILD_TIMEOUT", "30m", TypeDuration, "", "", "1m", "24h"},
		"backup_assets.repository_reconcile_interval":     {"BACKUP_ASSETS_REPOSITORY_RECONCILE_INTERVAL", "15m", TypeDuration, "", "", "1m", "24h"},
		"backup_assets.audit_segment_max_events":          {"BACKUP_ASSETS_AUDIT_SEGMENT_MAX_EVENTS", "10000", TypeInt, "100", "1000000", "", ""},
		"backup_assets.audit_segment_max_age":             {"BACKUP_ASSETS_AUDIT_SEGMENT_MAX_AGE", "24h", TypeDuration, "", "", "1h", "168h"},
		"backup_assets.audit_detail_retention_days":       {"BACKUP_ASSETS_AUDIT_DETAIL_RETENTION_DAYS", "180", TypeInt, "1", "3650", "", ""},
		"backup_assets.audit_checkpoint_retention_days":   {"BACKUP_ASSETS_AUDIT_CHECKPOINT_RETENTION_DAYS", "2555", TypeInt, "180", "36500", "", ""},
		"backup_assets.lease_duration":                    {"BACKUP_ASSETS_LEASE_DURATION", "5m", TypeDuration, "", "", "30s", "30m"},
		"backup_assets.lease_heartbeat":                   {"BACKUP_ASSETS_LEASE_HEARTBEAT", "60s", TypeDuration, "", "", "10s", "5m"},
		"backup_assets.lease_absolute_deadline":           {"BACKUP_ASSETS_LEASE_ABSOLUTE_DEADLINE", "168h", TypeDuration, "", "", "5m", "168h"},
		"backup_assets.provider_operation_timeout":        {"BACKUP_ASSETS_PROVIDER_OPERATION_TIMEOUT", "2m", TypeDuration, "", "", "5s", "30m"},
		"backup_assets.provider_max_concurrency":          {"BACKUP_ASSETS_PROVIDER_MAX_CONCURRENCY", "4", TypeInt, "1", "32", "", ""},
		"backup_assets.provider_metadata_limit_bytes":     {"BACKUP_ASSETS_PROVIDER_METADATA_LIMIT_BYTES", "16777216", TypeInt, "65536", "67108864", "", ""},
		"backup_assets.publication_reconcile_interval":    {"BACKUP_ASSETS_PUBLICATION_RECONCILE_INTERVAL", "5m", TypeDuration, "", "", "30s", "24h"},
		"backup_assets.publication_reconcile_batch_size":  {"BACKUP_ASSETS_PUBLICATION_RECONCILE_BATCH_SIZE", "100", TypeInt, "1", "1000", "", ""},
		"backup_assets.publication_worker_concurrency":    {"BACKUP_ASSETS_PUBLICATION_WORKER_CONCURRENCY", "2", TypeInt, "1", "32", "", ""},
		"backup_assets.publication_missing_grace":         {"BACKUP_ASSETS_PUBLICATION_MISSING_GRACE", "30m", TypeDuration, "", "", "1m", "24h"},
		"backup_assets.publication_stream_max_bytes":      {"BACKUP_ASSETS_PUBLICATION_STREAM_MAX_BYTES", "268435456", TypeInt, "1048576", "1073741824", "", ""},
		"backup_assets.manifest_timeout":                  {"BACKUP_ASSETS_MANIFEST_TIMEOUT", "2h", TypeDuration, "", "", "1m", "24h"},
		"backup_assets.manifest_max_bytes":                {"BACKUP_ASSETS_MANIFEST_MAX_BYTES", "4294967296", TypeInt, "1048576", "17179869184", "", ""},
		"backup_assets.manifest_max_entries":              {"BACKUP_ASSETS_MANIFEST_MAX_ENTRIES", "10000000", TypeInt, "1", "100000000", "", ""},
		"backup_assets.manifest_max_record_bytes":         {"BACKUP_ASSETS_MANIFEST_MAX_RECORD_BYTES", "1048576", TypeInt, "4096", "4194304", "", ""},
		"backup_assets.manifest_max_depth":                {"BACKUP_ASSETS_MANIFEST_MAX_DEPTH", "4096", TypeInt, "1", "65536", "", ""},
		"backup_assets.rclone_preflight_ttl":              {"BACKUP_ASSETS_RCLONE_PREFLIGHT_TTL", "30m", TypeDuration, "", "", "16m", "24h"},
		"backup_assets.rclone_portable_deadline":          {"BACKUP_ASSETS_RCLONE_PORTABLE_DEADLINE", "24h", TypeDuration, "", "", "5m", "168h"},
		"backup_assets.rclone_native_deadline":            {"BACKUP_ASSETS_RCLONE_NATIVE_DEADLINE", "45m", TypeDuration, "", "", "5m", "55m"},
		"backup_assets.rclone_bound_config_max_bytes":     {"BACKUP_ASSETS_RCLONE_BOUND_CONFIG_MAX_BYTES", "65536", TypeInt, "1024", "65536", "", ""},
		"backup_assets.rclone_control_payload_max_bytes":  {"BACKUP_ASSETS_RCLONE_CONTROL_PAYLOAD_MAX_BYTES", "8388608", TypeInt, "65536", "67108864", "", ""},
		"backup_assets.rclone_full_verify_max_bytes":      {"BACKUP_ASSETS_RCLONE_FULL_VERIFY_MAX_BYTES", "1099511627776", TypeInt, "1048576", "17592186044416", "", ""},
		"backup_assets.rclone_manifest_chunk_max_bytes":   {"BACKUP_ASSETS_RCLONE_MANIFEST_CHUNK_MAX_BYTES", "8388608", TypeInt, "65536", "67108864", "", ""},
		"backup_assets.rclone_low_level_retries":          {"BACKUP_ASSETS_RCLONE_LOW_LEVEL_RETRIES", "3", TypeInt, "1", "10", "", ""},
		"backup_assets.rclone_staging_orphan_age":         {"BACKUP_ASSETS_RCLONE_STAGING_ORPHAN_AGE", "24h", TypeDuration, "", "", "1h", "168h"},
		"backup_assets.rclone_staging_scan_limit":         {"BACKUP_ASSETS_RCLONE_STAGING_SCAN_LIMIT", "256", TypeInt, "1", "4096", "", ""},
		"backup_assets.rclone_kms_read_key_max_count":     {"BACKUP_ASSETS_RCLONE_KMS_READ_KEY_MAX_COUNT", "8", TypeInt, "1", "32", "", ""},
		"backup_assets.rclone_health_interval":            {"BACKUP_ASSETS_RCLONE_HEALTH_INTERVAL", "15m", TypeDuration, "", "", "1m", "24h"},
		"backup_assets.rclone_health_batch_size":          {"BACKUP_ASSETS_RCLONE_HEALTH_BATCH_SIZE", "100", TypeInt, "1", "1000", "", ""},
		"backup_assets.rclone_aws_sdk_max_attempts":       {"BACKUP_ASSETS_RCLONE_AWS_SDK_MAX_ATTEMPTS", "3", TypeInt, "1", "10", "", ""},
		"backup_assets.search_reconcile_interval":         {"BACKUP_ASSETS_SEARCH_RECONCILE_INTERVAL", "1m", TypeDuration, "", "", "10s", "1h"},
		"backup_assets.search_build_timeout":              {"BACKUP_ASSETS_SEARCH_BUILD_TIMEOUT", "30m", TypeDuration, "", "", "1m", "24h"},
		"backup_assets.search_batch_size":                 {"BACKUP_ASSETS_SEARCH_BATCH_SIZE", "500", TypeInt, "50", "5000", "", ""},
		"backup_assets.search_max_concurrency":            {"BACKUP_ASSETS_SEARCH_MAX_CONCURRENCY", "2", TypeInt, "1", "16", "", ""},
		"backup_assets.search_ast_max_depth":              {"BACKUP_ASSETS_SEARCH_AST_MAX_DEPTH", "8", TypeInt, "1", "16", "", ""},
		"backup_assets.search_ast_max_nodes":              {"BACKUP_ASSETS_SEARCH_AST_MAX_NODES", "64", TypeInt, "2", "256", "", ""},
		"backup_assets.search_values_per_node":            {"BACKUP_ASSETS_SEARCH_VALUES_PER_NODE", "32", TypeInt, "1", "64", "", ""},
		"backup_assets.search_body_max_bytes":             {"BACKUP_ASSETS_SEARCH_BODY_MAX_BYTES", "65536", TypeInt, "1024", "65536", "", ""},
		"backup_assets.search_value_max_bytes":            {"BACKUP_ASSETS_SEARCH_VALUE_MAX_BYTES", "1024", TypeInt, "1", "4096", "", ""},
		"backup_assets.search_candidate_limit":            {"BACKUP_ASSETS_SEARCH_CANDIDATE_LIMIT", "10000", TypeInt, "100", "100000", "", ""},
		"backup_assets.search_query_timeout":              {"BACKUP_ASSETS_SEARCH_QUERY_TIMEOUT", "5s", TypeDuration, "", "", "100ms", "30s"},
		"backup_assets.search_page_size_max":              {"BACKUP_ASSETS_SEARCH_PAGE_SIZE_MAX", "200", TypeInt, "1", "500", "", ""},
		"backup_assets.search_suggestion_limit":           {"BACKUP_ASSETS_SEARCH_SUGGESTION_LIMIT", "20", TypeInt, "0", "50", "", ""},
		"backup_assets.saved_search_quota":                {"BACKUP_ASSETS_SAVED_SEARCH_QUOTA", "100", TypeInt, "1", "1000", "", ""},
		"backup_assets.favorite_quota":                    {"BACKUP_ASSETS_FAVORITE_QUOTA", "5000", TypeInt, "1", "100000", "", ""},
		"backup_assets.tag_definition_quota":              {"BACKUP_ASSETS_TAG_DEFINITION_QUOTA", "100", TypeInt, "1", "1000", "", ""},
		"backup_assets.tag_assignment_quota":              {"BACKUP_ASSETS_TAG_ASSIGNMENT_QUOTA", "10000", TypeInt, "1", "200000", "", ""},
		"backup_assets.overlay_bulk_max_items":            {"BACKUP_ASSETS_OVERLAY_BULK_MAX_ITEMS", "200", TypeInt, "1", "1000", "", ""},
		"backup_assets.overlay_label_max_bytes":           {"BACKUP_ASSETS_OVERLAY_LABEL_MAX_BYTES", "256", TypeInt, "1", "4096", "", ""},
		"backup_assets.recent_quota":                      {"BACKUP_ASSETS_RECENT_QUOTA", "10000", TypeInt, "1", "100000", "", ""},
		"backup_assets.recent_retention":                  {"BACKUP_ASSETS_RECENT_RETENTION", "720h", TypeDuration, "", "", "24h", "8760h"},
		"backup_assets.recent_writes_per_minute":          {"BACKUP_ASSETS_RECENT_WRITES_PER_MINUTE", "120", TypeInt, "1", "10000", "", ""},
		"backup_assets.idempotency_ttl":                   {"BACKUP_ASSETS_IDEMPOTENCY_TTL", "24h", TypeDuration, "", "", "1h", "168h"},
		"backup_assets.idempotency_key_max_bytes":         {"BACKUP_ASSETS_IDEMPOTENCY_KEY_MAX_BYTES", "128", TypeInt, "32", "256", "", ""},
	}
	defs := NewService(setupTestDB(t)).Registry()
	got := make(map[string]SettingDef, len(want))
	for _, def := range defs {
		if strings.HasPrefix(def.Key, "backup_assets.") {
			got[def.Key] = def
		}
	}
	wantCount := len(want) + len(expectedBackupAssetProcessingDefinitions) +
		len(expectedBackupAssetExportDefinitions) + len(backupAssetRecoverySettingKeys)
	if len(got) != wantCount {
		t.Fatalf("backup asset setting count=%d, want %d", len(got), wantCount)
	}
	for key, expected := range want {
		def, ok := got[key]
		if !ok {
			t.Fatalf("missing setting %s", key)
		}
		if def.EnvVar != expected.env || def.CodeDefault != expected.defaultValue || def.Type != expected.settingType ||
			def.Min != expected.min || def.Max != expected.max || def.MinDuration != expected.minDuration || def.MaxDuration != expected.maxDuration {
			t.Errorf("setting %s mismatch: %+v", key, def)
		}
		if def.Sensitive || def.RequiresRestart {
			t.Errorf("foundation setting %s lifecycle mismatch: %+v", key, def)
		}
	}

	t.Setenv("BACKUP_ASSETS_ENABLED", "")
	service := NewService(setupTestDB(t))
	if got := service.GetEffective("backup_assets.enabled"); got != "false" {
		t.Fatalf("backup assets default=%q, want false", got)
	}
}

func TestRetentionSettingsDefinitionsAndSafeDefaults(t *testing.T) {
	type expectedDefinition struct {
		env          string
		defaultValue string
		settingType  SettingType
		min          string
		max          string
		minDuration  string
		maxDuration  string
	}
	want := map[string]expectedDefinition{
		"backup_assets.retention_reconcile_interval": {"BACKUP_ASSETS_RETENTION_RECONCILE_INTERVAL", "5m", TypeDuration, "", "", "30s", "24h"},
		"backup_assets.retention_batch_size":         {"BACKUP_ASSETS_RETENTION_BATCH_SIZE", "100", TypeInt, "1", "1000", "", ""},
		"backup_assets.retention_drain_timeout":      {"BACKUP_ASSETS_RETENTION_DRAIN_TIMEOUT", "30s", TypeDuration, "", "", "5s", "30m"},
	}

	definitions := NewService(setupTestDB(t)).Registry()
	got := make(map[string]SettingDef, len(want))
	for _, definition := range definitions {
		if _, ok := want[definition.Key]; ok {
			got[definition.Key] = definition
		}
	}
	for key, expected := range want {
		definition, ok := got[key]
		if !ok {
			t.Fatalf("missing retention setting %s", key)
		}
		if definition.EnvVar != expected.env || definition.CodeDefault != expected.defaultValue ||
			definition.Type != expected.settingType || definition.Min != expected.min || definition.Max != expected.max ||
			definition.MinDuration != expected.minDuration || definition.MaxDuration != expected.maxDuration ||
			definition.RequiresRestart || definition.Sensitive {
			t.Errorf("retention setting %s mismatch: %+v", key, definition)
		}
	}

	t.Setenv("BACKUP_ASSETS_ENABLED", "")
	if got := NewService(setupTestDB(t)).GetEffective("backup_assets.enabled"); got != "false" {
		t.Fatalf("backup assets default=%q, want false", got)
	}
}

func TestBackupAssetProcessingDefinitionsAndSafeDefaults(t *testing.T) {
	defs := NewService(setupTestDB(t)).Registry()
	got := make(map[string]SettingDef, len(expectedBackupAssetProcessingDefinitions))
	for _, def := range defs {
		if _, expected := expectedBackupAssetProcessingDefinitions[def.Key]; expected {
			got[def.Key] = def
		}
	}
	if len(got) != len(expectedBackupAssetProcessingDefinitions) {
		t.Fatalf("processing setting count=%d, want %d", len(got), len(expectedBackupAssetProcessingDefinitions))
	}
	for key, expected := range expectedBackupAssetProcessingDefinitions {
		def, ok := got[key]
		if !ok {
			t.Fatalf("missing processing setting %s", key)
		}
		if def.EnvVar != expected.env || def.CodeDefault != expected.defaultValue || def.Type != expected.settingType ||
			def.Min != expected.min || def.Max != expected.max || def.MinDuration != expected.minDuration ||
			def.MaxDuration != expected.maxDuration || def.RequiresRestart != expected.requiresRestart ||
			def.Sensitive != expected.sensitive {
			t.Errorf("processing setting %s mismatch: %+v", key, def)
		}
	}
	service := NewService(setupTestDB(t))
	for _, key := range []string{
		"backup_assets.enabled", "backup_assets.worker_local_enabled", "backup_assets.worker_remote_enabled",
		"backup_assets.worker_updater_enabled", "backup_assets.worker_updater_online_enabled", "backup_assets.processing_secret_classify",
	} {
		if value := service.GetEffective(key); value != "false" {
			t.Errorf("%s default=%q, want false", key, value)
		}
	}
}

func TestBackupAssetExportSettingDefinitions(t *testing.T) {
	definitions := NewService(setupTestDB(t)).Registry()
	got := make(map[string]SettingDef, len(expectedBackupAssetExportDefinitions))
	for _, definition := range definitions {
		if _, expected := expectedBackupAssetExportDefinitions[definition.Key]; expected {
			got[definition.Key] = definition
		}
	}
	if len(got) != len(expectedBackupAssetExportDefinitions) {
		t.Fatalf("export/archive setting count=%d, want %d", len(got), len(expectedBackupAssetExportDefinitions))
	}
	for key, expected := range expectedBackupAssetExportDefinitions {
		definition, ok := got[key]
		if !ok {
			t.Fatalf("missing export/archive setting %s", key)
		}
		if definition.EnvVar != expected.env || definition.CodeDefault != expected.defaultValue ||
			definition.Type != expected.settingType || definition.Min != expected.min || definition.Max != expected.max ||
			definition.MinDuration != expected.minDuration || definition.MaxDuration != expected.maxDuration ||
			definition.RequiresRestart != expected.requiresRestart || definition.Sensitive != expected.sensitive {
			t.Errorf("export/archive setting %s mismatch: %+v", key, definition)
		}
	}
	for _, definition := range definitions {
		key := strings.ToLower(definition.Key)
		if strings.Contains(key, "export.kek") || strings.Contains(key, "export.key_file") || strings.Contains(key, "export.keyfile") {
			t.Fatalf("raw Export key material must not be a public setting: %s", definition.Key)
		}
	}
}

func TestBackupAssetExportFoundationKeysAreComplete(t *testing.T) {
	keys := BackupAssetFoundationSettingKeys()
	keySet := make(map[string]bool, len(keys))
	for _, key := range keys {
		keySet[key] = true
	}
	for key := range expectedBackupAssetExportDefinitions {
		if !keySet[key] || !IsBackupAssetFoundationSetting(key) {
			t.Errorf("Export/Archive setting omitted from atomic foundation set: %s", key)
		}
	}
	if len(keys) != len(backupAssetCoreSettingKeys)+len(backupAssetSearchOverlaySettingKeys)+
		len(backupAssetContentSettingKeys)+len(backupAssetProcessingSettingKeys)+len(expectedBackupAssetExportDefinitions)+
		len(backupAssetRecoverySettingKeys) {
		t.Fatalf("foundation key count=%d does not include exact Export/Archive set", len(keys))
	}
}

func TestBackupAssetExportCrossSettingBoundaries(t *testing.T) {
	valid := backupAssetFoundationValuesForTest()
	if err := ValidateBackupAssetFoundationConfig(valid); err != nil {
		t.Fatalf("valid Export/Archive defaults rejected: %v", err)
	}

	tests := []struct {
		name   string
		values map[string]string
	}{
		{"source points must not exceed items", map[string]string{"backup_assets.export.max_source_points": "10001"}},
		{"item bytes must not exceed logical bytes", map[string]string{"backup_assets.export.max_logical_bytes": "1073741824"}},
		{"provider bytes must cover logical bytes", map[string]string{"backup_assets.export.max_provider_bytes": "8589934592"}},
		{"ciphertext must cover archive overhead", map[string]string{"backup_assets.export.max_ciphertext_bytes": "10737418240"}},
		{"ticket cumulative bytes must cover ciphertext", map[string]string{"backup_assets.export.ticket_max_cumulative_bytes": "8589934592"}},
		{"user store quota must not exceed global", map[string]string{"backup_assets.export.user_store_quota": "2199023255552"}},
		{"global store must cover artifact and spool", map[string]string{"backup_assets.export.store_quota": "12884901888"}},
		{"user jobs must not exceed global", map[string]string{"backup_assets.export.global_active_jobs": "1"}},
		{"retry maximum must cover retry base", map[string]string{"backup_assets.export.retry_base": "1m", "backup_assets.export.retry_max_delay": "5s"}},
		{"renew margin must be below half lease ttl", map[string]string{"backup_assets.export.lease_renew_margin": "45s"}},
		{"member bytes must not exceed expanded bytes", map[string]string{"backup_assets.archive.max_expanded_bytes": "1048576"}},
		{"profile must be closed", map[string]string{"backup_assets.export.default_profile": "zip_custom"}},
		{"root must be private absolute", map[string]string{"backup_assets.export.root": "/data/export"}},
		{"root must not overlap content", map[string]string{"backup_assets.export.root": "/var/cache/xirang/asset-content/export"}},
		{"root must not overlap derived", map[string]string{"backup_assets.export.root": "/var/lib/xirang-asset-runtime/derived/export"}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			values := cloneSettingsValues(valid)
			for key, value := range testCase.values {
				values[key] = value
			}
			if err := ValidateBackupAssetFoundationConfig(values); err == nil {
				t.Fatal("expected coupled Export/Archive settings rejection")
			}
		})
	}
}

func TestBackupAssetExportCiphertextAdmissionRejectsV1AndArchivePathUnderestimates(t *testing.T) {
	const (
		minimumCipherChunkBytes int64 = 64 << 10
		cipherRecordBytes       int64 = 20
		cipherFixedBytes        int64 = 20 + 68
		legacyArchiveFixedBytes int64 = 64 << 20
		archivePathBytes        int64 = 4096
	)
	legacyMinimum := func(maxLogicalBytes, maxItems int64) int64 {
		return maxLogicalBytes + maxItems*1024 + legacyArchiveFixedBytes
	}
	cases := []struct {
		name       string
		values     map[string]string
		assertSafe func(t *testing.T, maxCiphertextBytes int64)
	}{
		{
			name: "large final stream needs V1 chunk records",
			values: map[string]string{
				"backup_assets.export.chunk_bytes":                 strconv.FormatInt(minimumCipherChunkBytes, 10),
				"backup_assets.export.max_items":                   "4",
				"backup_assets.export.max_source_points":           "4",
				"backup_assets.export.max_logical_bytes":           strconv.FormatInt(1<<40, 10),
				"backup_assets.export.max_provider_bytes":          strconv.FormatInt(2<<40, 10),
				"backup_assets.export.max_ciphertext_bytes":        strconv.FormatInt(legacyMinimum(1<<40, 4), 10),
				"backup_assets.export.ticket_max_cumulative_bytes": strconv.FormatInt(2<<40, 10),
				"backup_assets.export.user_store_quota":            strconv.FormatInt(2<<40, 10),
				"backup_assets.export.store_quota":                 strconv.FormatInt(2<<40, 10),
			},
			assertSafe: func(t *testing.T, maxCiphertextBytes int64) {
				t.Helper()
				plaintextBytes := int64(1 << 40)
				chunkCount := 1 + (plaintextBytes-1)/minimumCipherChunkBytes
				minimumCiphertextBytes := plaintextBytes + chunkCount*cipherRecordBytes + cipherFixedBytes
				if maxCiphertextBytes >= minimumCiphertextBytes {
					t.Fatalf("fixture ciphertext=%d must be below V1 minimum=%d", maxCiphertextBytes, minimumCiphertextBytes)
				}
			},
		},
		{
			name: "directory member paths exceed legacy archive allowance",
			values: map[string]string{
				"backup_assets.export.chunk_bytes":          strconv.FormatInt(minimumCipherChunkBytes, 10),
				"backup_assets.export.max_items":            "100000",
				"backup_assets.export.max_item_bytes":       strconv.FormatInt(minimumCipherChunkBytes, 10),
				"backup_assets.export.max_logical_bytes":    strconv.FormatInt(minimumCipherChunkBytes, 10),
				"backup_assets.export.max_provider_bytes":   strconv.FormatInt(minimumCipherChunkBytes, 10),
				"backup_assets.export.max_ciphertext_bytes": strconv.FormatInt(legacyMinimum(minimumCipherChunkBytes, 100000), 10),
			},
			assertSafe: func(t *testing.T, maxCiphertextBytes int64) {
				t.Helper()
				pathBytes := int64(100000) * archivePathBytes
				if maxCiphertextBytes >= pathBytes {
					t.Fatalf("fixture ciphertext=%d must be below directory path bytes=%d", maxCiphertextBytes, pathBytes)
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			values := backupAssetFoundationValuesForTest()
			for key, value := range testCase.values {
				values[key] = value
			}
			maxCiphertextBytes, err := strconv.ParseInt(values["backup_assets.export.max_ciphertext_bytes"], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			testCase.assertSafe(t, maxCiphertextBytes)
			if err := ValidateBackupAssetFoundationConfig(values); err == nil {
				t.Fatalf("accepted max_ciphertext_bytes=%d below a bounded archive/ciphertext requirement", maxCiphertextBytes)
			}
		})
	}
}

func TestBackupAssetExportStoreQuotaUsesExactV1SpoolBoundary(t *testing.T) {
	// These independent constants freeze the V1 physical layout at the settings
	// boundary: 20-byte header, 20 bytes per chunk, and a 68-byte trailer.
	const (
		maxItemBytes       int64 = 2 << 30
		maxCiphertextBytes int64 = 12 << 30
		fixedBytes         int64 = 20 + 68
		chunkRecordBytes   int64 = 20
	)
	ciphertextBytes := func(plaintextBytes, chunkBytes int64) int64 {
		if plaintextBytes == 0 {
			return fixedBytes
		}
		chunkCount := 1 + (plaintextBytes-1)/chunkBytes
		return plaintextBytes + chunkCount*chunkRecordBytes + fixedBytes
	}

	for _, chunkBytes := range []int64{64 << 10, 8 << 20} {
		t.Run(strconv.FormatInt(chunkBytes, 10), func(t *testing.T) {
			values := backupAssetFoundationValuesForTest()
			values["backup_assets.export.chunk_bytes"] = strconv.FormatInt(chunkBytes, 10)
			values["backup_assets.export.max_items"] = "1"
			values["backup_assets.export.max_source_points"] = "1"

			plaintextOnlyBoundary := maxCiphertextBytes + maxItemBytes
			values["backup_assets.export.user_store_quota"] = strconv.FormatInt(plaintextOnlyBoundary, 10)
			values["backup_assets.export.store_quota"] = strconv.FormatInt(plaintextOnlyBoundary, 10)
			if err := ValidateBackupAssetFoundationConfig(values); err == nil {
				t.Fatal("plaintext-only spool boundary must not satisfy the V1 ciphertext reservation")
			}

			exactBoundary := maxCiphertextBytes + ciphertextBytes(maxItemBytes, chunkBytes)
			values["backup_assets.export.user_store_quota"] = strconv.FormatInt(exactBoundary, 10)
			values["backup_assets.export.store_quota"] = strconv.FormatInt(exactBoundary, 10)
			if err := ValidateBackupAssetFoundationConfig(values); err != nil {
				t.Fatalf("exact V1 spool boundary rejected: %v", err)
			}
		})
	}
}

func TestBackupAssetExportStoreQuotaCoversMaximumMultiItemSpoolPeak(t *testing.T) {
	const (
		chunkBytes           int64 = 1 << 20
		maxItemBytes         int64 = 1 << 20
		maxLogicalBytes      int64 = 2 << 20
		maxCiphertextBytes   int64 = 12 << 30
		cipherFixedBytes     int64 = 20 + 68
		cipherRecordOverhead int64 = 20
	)
	ciphertextBytes := func(plaintextBytes int64) int64 {
		chunkCount := int64(0)
		if plaintextBytes > 0 {
			chunkCount = 1 + (plaintextBytes-1)/chunkBytes
		}
		return plaintextBytes + chunkCount*cipherRecordOverhead + cipherFixedBytes
	}

	spoolBytes := ciphertextBytes(maxItemBytes)
	exactPeak := maxCiphertextBytes + 2*spoolBytes
	base := backupAssetFoundationValuesForTest()
	base["backup_assets.export.chunk_bytes"] = strconv.FormatInt(chunkBytes, 10)
	base["backup_assets.export.max_items"] = "2"
	base["backup_assets.export.max_source_points"] = "2"
	base["backup_assets.export.max_item_bytes"] = strconv.FormatInt(maxItemBytes, 10)
	base["backup_assets.export.max_logical_bytes"] = strconv.FormatInt(maxLogicalBytes, 10)
	base["backup_assets.export.max_provider_bytes"] = strconv.FormatInt(maxLogicalBytes, 10)
	base["backup_assets.export.max_ciphertext_bytes"] = strconv.FormatInt(maxCiphertextBytes, 10)
	base["backup_assets.export.user_store_quota"] = strconv.FormatInt(exactPeak, 10)
	base["backup_assets.export.store_quota"] = strconv.FormatInt(exactPeak, 10)
	if err := ValidateBackupAssetFoundationConfig(base); err != nil {
		t.Fatalf("exact two-item maximum peak=%d rejected: %v", exactPeak, err)
	}

	for _, quotaKey := range []string{
		"backup_assets.export.user_store_quota",
		"backup_assets.export.store_quota",
	} {
		t.Run(quotaKey, func(t *testing.T) {
			values := cloneSettingsValues(base)
			values[quotaKey] = strconv.FormatInt(exactPeak-1, 10)
			if quotaKey == "backup_assets.export.store_quota" {
				values["backup_assets.export.user_store_quota"] = strconv.FormatInt(exactPeak-1, 10)
			}
			if err := ValidateBackupAssetFoundationConfig(values); err == nil {
				t.Fatalf("accepted %s=%d below exact two-item maximum peak=%d", quotaKey, exactPeak-1, exactPeak)
			}
		})
	}
}

func TestBackupAssetExportStoreQuotaCoversLogicalCapChunkBoundary(t *testing.T) {
	const (
		chunkBytes           int64 = 64 << 10
		maxItemBytes         int64 = chunkBytes + 2
		maxLogicalBytes      int64 = maxItemBytes + 2
		maxCiphertextBytes   int64 = 12 << 30
		cipherFixedBytes     int64 = 20 + 68
		cipherRecordOverhead int64 = 20
	)
	ciphertextBytes := func(plaintextBytes int64) int64 {
		chunkCount := int64(0)
		if plaintextBytes > 0 {
			chunkCount = 1 + (plaintextBytes-1)/chunkBytes
		}
		return plaintextBytes + chunkCount*cipherRecordOverhead + cipherFixedBytes
	}

	// The exact maximum is [maxItemBytes, 1, 1]: the capped first file
	// crosses the chunk boundary while the logical remainder opens two more
	// regular spools.
	exactPeak := maxCiphertextBytes + ciphertextBytes(maxItemBytes) + 2*ciphertextBytes(1)
	base := backupAssetFoundationValuesForTest()
	base["backup_assets.export.chunk_bytes"] = strconv.FormatInt(chunkBytes, 10)
	base["backup_assets.export.max_items"] = "3"
	base["backup_assets.export.max_source_points"] = "3"
	base["backup_assets.export.max_item_bytes"] = strconv.FormatInt(maxItemBytes, 10)
	base["backup_assets.export.max_logical_bytes"] = strconv.FormatInt(maxLogicalBytes, 10)
	base["backup_assets.export.max_provider_bytes"] = strconv.FormatInt(maxLogicalBytes, 10)
	base["backup_assets.export.max_ciphertext_bytes"] = strconv.FormatInt(maxCiphertextBytes, 10)
	base["backup_assets.export.user_store_quota"] = strconv.FormatInt(exactPeak, 10)
	base["backup_assets.export.store_quota"] = strconv.FormatInt(exactPeak, 10)
	if err := ValidateBackupAssetFoundationConfig(base); err != nil {
		t.Fatalf("exact logical-cap/chunk-boundary peak=%d rejected: %v", exactPeak, err)
	}

	for _, quotaKey := range []string{
		"backup_assets.export.user_store_quota",
		"backup_assets.export.store_quota",
	} {
		t.Run(quotaKey, func(t *testing.T) {
			values := cloneSettingsValues(base)
			values[quotaKey] = strconv.FormatInt(exactPeak-1, 10)
			if quotaKey == "backup_assets.export.store_quota" {
				values["backup_assets.export.user_store_quota"] = strconv.FormatInt(exactPeak-1, 10)
			}
			if err := ValidateBackupAssetFoundationConfig(values); err == nil {
				t.Fatalf("accepted %s=%d below exact logical-cap/chunk-boundary peak=%d", quotaKey, exactPeak-1, exactPeak)
			}
		})
	}
}

func TestBackupAssetExportMaximumStorePeakV1(t *testing.T) {
	const (
		cipherFixedBytes     int64 = 20 + 68
		cipherRecordOverhead int64 = 20
	)
	ciphertextBytes := func(plaintextBytes, chunkBytes int64) int64 {
		chunkCount := int64(0)
		if plaintextBytes > 0 {
			chunkCount = 1 + (plaintextBytes-1)/chunkBytes
		}
		return plaintextBytes + chunkCount*cipherRecordOverhead + cipherFixedBytes
	}

	tests := []struct {
		name                                           string
		archiveCiphertextBytes, maxItems, maxItemBytes int64
		maxLogicalBytes, chunkBytes, want              int64
	}{
		{
			name:                   "logical cap distributes chunk boundaries",
			archiveCiphertextBytes: 1, maxItems: 3, maxItemBytes: 10,
			maxLogicalBytes: 15, chunkBytes: 8,
			want: 1 + ciphertextBytes(10, 8) + ciphertextBytes(4, 8) + ciphertextBytes(1, 8),
		},
		{
			name:                   "zero byte regular spools retain fixed overhead",
			archiveCiphertextBytes: 1, maxItems: 3, maxItemBytes: 10,
			maxLogicalBytes: 1, chunkBytes: 8,
			want: 1 + ciphertextBytes(1, 8) + 2*ciphertextBytes(0, 8),
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := BackupAssetExportMaximumStorePeakV1(
				testCase.archiveCiphertextBytes, testCase.maxItems, testCase.maxItemBytes,
				testCase.maxLogicalBytes, testCase.chunkBytes,
			)
			if !ok || got != testCase.want {
				t.Fatalf("maximum peak=(%d, %t), want (%d, true)", got, ok, testCase.want)
			}
		})
	}
}

func TestBackupAssetExportMaximumStorePeakV1FailsClosedOnOverflow(t *testing.T) {
	if got, ok := BackupAssetExportMaximumStorePeakV1(math.MaxInt64, 1, 1, 1, 1); ok || got != 0 {
		t.Fatalf("overflow peak=(%d, %t), want (0, false)", got, ok)
	}
}

func TestBackupAssetExportCiphertextSizeV1SettingsContract(t *testing.T) {
	const (
		minChunkBytes     int64 = 64 << 10
		maxChunkBytes     int64 = 8 << 20
		fixedBytes        int64 = 20 + 68
		chunkRecordBytes  int64 = 20
		maximumChunkCount int64 = math.MaxUint32
	)
	tests := []struct {
		name           string
		plaintextBytes int64
		chunkBytes     int64
		want           int64
	}{
		{name: "empty", plaintextBytes: 0, chunkBytes: minChunkBytes, want: fixedBytes},
		{name: "one byte", plaintextBytes: 1, chunkBytes: minChunkBytes, want: 1 + chunkRecordBytes + fixedBytes},
		{name: "one full chunk", plaintextBytes: minChunkBytes, chunkBytes: minChunkBytes, want: minChunkBytes + chunkRecordBytes + fixedBytes},
		{name: "full plus partial", plaintextBytes: minChunkBytes + 1, chunkBytes: minChunkBytes, want: minChunkBytes + 1 + 2*chunkRecordBytes + fixedBytes},
		{name: "maximum registry chunk", plaintextBytes: maxChunkBytes + 1, chunkBytes: maxChunkBytes, want: maxChunkBytes + 1 + 2*chunkRecordBytes + fixedBytes},
		{
			name:           "uint32 counter limit",
			plaintextBytes: maximumChunkCount * minChunkBytes,
			chunkBytes:     minChunkBytes,
			want:           maximumChunkCount*minChunkBytes + maximumChunkCount*chunkRecordBytes + fixedBytes,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := backupAssetExportCiphertextSizeV1(testCase.plaintextBytes, testCase.chunkBytes)
			if !ok || got != testCase.want {
				t.Fatalf("ciphertext size (%d, %d)=(%d, %t), want (%d, true)", testCase.plaintextBytes, testCase.chunkBytes, got, ok, testCase.want)
			}
		})
	}

	invalid := []struct {
		name           string
		plaintextBytes int64
		chunkBytes     int64
	}{
		{name: "negative plaintext", plaintextBytes: -1, chunkBytes: minChunkBytes},
		{name: "chunk below registry minimum", plaintextBytes: 1, chunkBytes: minChunkBytes - 1},
		{name: "chunk above registry maximum", plaintextBytes: 1, chunkBytes: maxChunkBytes + 1},
		{name: "uint32 counter overflow", plaintextBytes: maximumChunkCount*minChunkBytes + 1, chunkBytes: minChunkBytes},
		{name: "int64 size overflow", plaintextBytes: math.MaxInt64, chunkBytes: minChunkBytes},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			if got, ok := backupAssetExportCiphertextSizeV1(testCase.plaintextBytes, testCase.chunkBytes); ok || got != 0 {
				t.Fatalf("invalid ciphertext size (%d, %d)=(%d, %t), want (0, false)", testCase.plaintextBytes, testCase.chunkBytes, got, ok)
			}
		})
	}
}

func TestBackupAssetProcessingCrossSettingBoundaries(t *testing.T) {
	valid := backupAssetFoundationValuesForTest()
	if err := ValidateBackupAssetFoundationConfig(valid); err != nil {
		t.Fatalf("valid processing defaults rejected: %v", err)
	}

	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"heartbeat must be below half lease", "backup_assets.processing_pull_heartbeat", "45s"},
		{"retry max delay must cover base", "backup_assets.processing_retry_max_delay", "4s"},
		{"input cumulative must cover request", "backup_assets.processing_input_cumulative_max_bytes", "65536"},
		{"sink total must cover artifact", "backup_assets.processing_sink_total_max_bytes", "65536"},
		{"derived blob must cover chunk", "backup_assets.derived_store_blob_max_bytes", "65536"},
		{"derived global must cover blob", "backup_assets.derived_store_global_max_bytes", "65536"},
		{"local socket must be clean absolute", "backup_assets.worker_local_socket", "run/worker.sock"},
		{"derived root must be private", "backup_assets.derived_store_root", "/data/derived"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := cloneSettingsValues(valid)
			values[test.key] = test.value
			if err := ValidateBackupAssetFoundationConfig(values); err == nil {
				t.Fatalf("%s=%q unexpectedly accepted", test.key, test.value)
			}
		})
	}

	remote := cloneSettingsValues(valid)
	remote["backup_assets.worker_remote_enabled"] = "true"
	if err := ValidateBackupAssetFoundationConfig(remote); err == nil {
		t.Fatal("partial remote trust unexpectedly accepted")
	}
	for key, value := range map[string]string{
		"backup_assets.worker_remote_listen_addr":      "127.0.0.1:10762",
		"backup_assets.worker_remote_server_cert_file": "/run/secrets/worker-server.crt",
		"backup_assets.worker_remote_server_key_file":  "/run/secrets/worker-server.key",
		"backup_assets.worker_remote_client_ca_file":   "/run/secrets/worker-client-ca.crt",
		"backup_assets.worker_remote_trust_domain":     "workers.example.internal",
	} {
		remote[key] = value
	}
	if err := ValidateBackupAssetFoundationConfig(remote); err != nil {
		t.Fatalf("complete remote trust rejected: %v", err)
	}
	remote["backup_assets.worker_remote_listen_addr"] = "0.0.0.0:10762"
	if err := ValidateBackupAssetFoundationConfig(remote); err == nil {
		t.Fatal("wildcard remote listen address unexpectedly accepted")
	}

	updaterOnline := cloneSettingsValues(valid)
	updaterOnline["backup_assets.worker_updater_online_enabled"] = "true"
	updaterOnline["backup_assets.worker_updater_online_origins"] = "https://bundles.example.internal:443"
	if err := ValidateBackupAssetFoundationConfig(updaterOnline); err == nil {
		t.Fatal("online updater without updater identity unexpectedly accepted")
	}
	updaterOnline["backup_assets.worker_updater_enabled"] = "true"
	if err := ValidateBackupAssetFoundationConfig(updaterOnline); err != nil {
		t.Fatalf("closed updater origin rejected: %v", err)
	}
	for _, origins := range []string{
		"", "http://bundles.example.internal:80", "https://bundles.example.internal",
		"https://z.example.internal:443,https://a.example.internal:443",
		"https://a.example.internal:443,https://a.example.internal:443",
		"https://A.example.internal:443", "https://a.example.internal:443/path",
	} {
		candidate := cloneSettingsValues(updaterOnline)
		candidate["backup_assets.worker_updater_online_origins"] = origins
		if err := ValidateBackupAssetFoundationConfig(candidate); err == nil {
			t.Fatalf("unsafe updater origins %q unexpectedly accepted", origins)
		}
	}
}

func TestMaxDurationValidation(t *testing.T) {
	service := NewService(setupTestDB(t))
	if err := service.Validate("backup_assets.catalog_build_timeout", "24h"); err != nil {
		t.Fatalf("24h maximum rejected: %v", err)
	}
	if err := service.Validate("backup_assets.catalog_build_timeout", "24h1s"); err == nil {
		t.Fatal("duration above MaxDuration unexpectedly accepted")
	}
	if err := validateRegistryDefinitions([]SettingDef{{
		Key: "test.duration", EnvVar: "TEST_DURATION", CodeDefault: "1m", Type: TypeDuration, MaxDuration: "not-a-duration",
	}}); err == nil {
		t.Fatal("malformed MaxDuration definition unexpectedly accepted")
	}
}

func TestBackupAssetSettingsLeaseHeartbeatMustBeLowerThanDuration(t *testing.T) {
	valid := map[string]string{
		"backup_assets.lease_duration":          "5m",
		"backup_assets.lease_heartbeat":         "60s",
		"backup_assets.lease_absolute_deadline": "168h",
	}
	if err := ValidateBackupAssetFoundationConfig(valid); err != nil {
		t.Fatalf("valid foundation lease config rejected: %v", err)
	}
	invalid := map[string]string{
		"backup_assets.lease_duration":          "5m",
		"backup_assets.lease_heartbeat":         "5m",
		"backup_assets.lease_absolute_deadline": "168h",
	}
	if err := ValidateBackupAssetFoundationConfig(invalid); err == nil {
		t.Fatal("heartbeat equal to lease duration unexpectedly accepted")
	}
}

func TestBackupAssetFoundationCrossSettingPublicationBoundaries(t *testing.T) {
	values := backupAssetFoundationValuesForTest()
	values["backup_assets.lease_duration"] = "71s"
	values["backup_assets.lease_heartbeat"] = "60s"
	values["backup_assets.lease_absolute_deadline"] = "2h"
	values["backup_assets.publication_missing_grace"] = "71s"
	values["backup_assets.manifest_timeout"] = "1h"
	values["backup_assets.manifest_max_bytes"] = "1048576"
	values["backup_assets.manifest_max_record_bytes"] = "1048576"
	if err := ValidateBackupAssetFoundationConfig(values); err != nil {
		t.Fatalf("valid publication foundation config rejected: %v", err)
	}
	if sshutil.CommandExecutionJoinTimeout != 10*time.Second {
		t.Fatalf("command execution join timeout=%s, want 10s", sshutil.CommandExecutionJoinTimeout)
	}

	invalidJoinMargin := cloneSettingsValues(values)
	invalidJoinMargin["backup_assets.lease_duration"] = "70s"
	if err := ValidateBackupAssetFoundationConfig(invalidJoinMargin); err == nil {
		t.Fatal("lease duration with no command-join margin unexpectedly accepted")
	}
	invalidMissingGrace := cloneSettingsValues(values)
	invalidMissingGrace["backup_assets.publication_missing_grace"] = "60s"
	if err := ValidateBackupAssetFoundationConfig(invalidMissingGrace); err == nil {
		t.Fatal("publication missing grace below lease duration unexpectedly accepted")
	}
	invalidManifestTimeout := cloneSettingsValues(values)
	invalidManifestTimeout["backup_assets.manifest_timeout"] = "2h"
	if err := ValidateBackupAssetFoundationConfig(invalidManifestTimeout); err == nil {
		t.Fatal("manifest timeout equal to absolute deadline unexpectedly accepted")
	}
	invalidRecordLimit := cloneSettingsValues(values)
	invalidRecordLimit["backup_assets.manifest_max_record_bytes"] = "1048577"
	if err := ValidateBackupAssetFoundationConfig(invalidRecordLimit); err == nil {
		t.Fatal("manifest record limit above total bytes unexpectedly accepted")
	}
}

func TestBackupAssetRcloneSettingCrossFieldBoundaries(t *testing.T) {
	service := NewService(setupTestDB(t))
	values := backupAssetFoundationValuesForTest()
	values["backup_assets.rclone_preflight_ttl"] = "16m"
	values["backup_assets.rclone_native_deadline"] = "55m"
	values["backup_assets.rclone_bound_config_max_bytes"] = "65536"
	values["backup_assets.rclone_control_payload_max_bytes"] = "65536"
	values["backup_assets.rclone_manifest_chunk_max_bytes"] = "65536"
	if err := service.ValidateBackupAssetEffectiveUpdate(values, map[string]string{}); err != nil {
		t.Fatalf("valid Rclone boundary settings rejected: %v", err)
	}

	for name, overrides := range map[string]map[string]string{
		"settle window": {"backup_assets.rclone_preflight_ttl": "15m"},
		"STS margin":    {"backup_assets.rclone_native_deadline": "55m1s"},
		"SecretStdin":   {"backup_assets.rclone_bound_config_max_bytes": "65537"},
		"manifest payload": {
			"backup_assets.rclone_control_payload_max_bytes": "65536",
			"backup_assets.rclone_manifest_chunk_max_bytes":  "65537",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := service.ValidateBackupAssetEffectiveUpdate(values, overrides); err == nil {
				t.Fatalf("unsafe Rclone settings accepted: %#v", overrides)
			}
		})
	}
}

func TestValidateBackupAssetEffectiveUpdateCombinesExplicitCurrentAndRequestOverrides(t *testing.T) {
	service := NewService(setupTestDB(t))
	current := backupAssetFoundationValuesForTest()
	current["backup_assets.lease_duration"] = "71s"
	current["backup_assets.lease_heartbeat"] = "60s"
	if err := service.ValidateBackupAssetEffectiveUpdate(current, map[string]string{"backup_assets.publication_missing_grace": "71s"}); err != nil {
		t.Fatalf("valid explicit current/override combination rejected: %v", err)
	}
	if err := service.ValidateBackupAssetEffectiveUpdate(current, map[string]string{"backup_assets.lease_duration": "70s"}); err == nil {
		t.Fatal("invalid explicit current/override combination unexpectedly accepted")
	}
	if err := service.ValidateBackupAssetEffectiveUpdate(current, map[string]string{"login.rate_limit": "100"}); err == nil {
		t.Fatal("non-foundation override unexpectedly accepted")
	}
}

func TestValidateBackupAssetEffectiveUpdateDoesNotMutateInputsOrReadAgain(t *testing.T) {
	db := setupTestDB(t)
	service := NewService(db)
	current := backupAssetFoundationValuesForTest()
	current["backup_assets.lease_duration"] = "71s"
	current["backup_assets.lease_heartbeat"] = "60s"
	overrides := map[string]string{"backup_assets.publication_missing_grace": "71s"}
	wantCurrent := cloneSettingsValues(current)
	wantOverrides := cloneSettingsValues(overrides)
	if err := service.Update("backup_assets.lease_duration", "70s"); err != nil {
		t.Fatalf("seed divergent DB value: %v", err)
	}
	t.Setenv("BACKUP_ASSETS_PUBLICATION_MISSING_GRACE", "60s")
	if err := service.ValidateBackupAssetEffectiveUpdate(current, overrides); err != nil {
		t.Fatalf("pure explicit validation read DB/env instead of supplied maps: %v", err)
	}
	if !reflect.DeepEqual(current, wantCurrent) || !reflect.DeepEqual(overrides, wantOverrides) {
		t.Fatalf("explicit validation mutated inputs: current=%#v overrides=%#v", current, overrides)
	}
}

func TestWithBackupAssetMutationSerializesCallbacksOverFreshSnapshots(t *testing.T) {
	service := NewService(setupTestDB(t))
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	secondObserved := make(chan string, 1)
	secondDone := make(chan error, 1)

	go func() {
		firstDone <- service.WithBackupAssetMutation(context.Background(), func(current map[string]string) error {
			close(firstEntered)
			<-releaseFirst
			return service.Update("backup_assets.lease_duration", "10m")
		})
	}()
	<-firstEntered
	go func() {
		secondDone <- service.WithBackupAssetMutation(context.Background(), func(current map[string]string) error {
			secondObserved <- current["backup_assets.lease_duration"]
			current["backup_assets.lease_duration"] = "mutated-callback-copy"
			return nil
		})
	}()
	select {
	case observed := <-secondObserved:
		t.Fatalf("second callback entered before first persistence finished: %q", observed)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first mutation: %v", err)
	}
	if observed := <-secondObserved; observed != "10m" {
		t.Fatalf("second mutation did not receive a fresh snapshot: %q", observed)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second mutation: %v", err)
	}
	if err := service.WithBackupAssetMutation(context.Background(), func(current map[string]string) error {
		if current["backup_assets.lease_duration"] != "10m" {
			t.Fatalf("callback mutation corrupted service-owned snapshot: %q", current["backup_assets.lease_duration"])
		}
		return nil
	}); err != nil {
		t.Fatalf("third mutation: %v", err)
	}
}

func TestBackupAssetSearchConfigAndOverlayConfigCrossSettingBoundaries(t *testing.T) {
	values := backupAssetFoundationValuesForTest()
	if err := ValidateBackupAssetFoundationConfig(values); err != nil {
		t.Fatalf("valid Search/Overlay defaults rejected: %v", err)
	}

	tests := map[string]map[string]string{
		"nodes below depth": {
			"backup_assets.search_ast_max_depth": "9",
			"backup_assets.search_ast_max_nodes": "8",
		},
		"body below value": {
			"backup_assets.search_body_max_bytes":  "1024",
			"backup_assets.search_value_max_bytes": "1025",
		},
		"candidates below page": {
			"backup_assets.search_candidate_limit": "100",
			"backup_assets.search_page_size_max":   "101",
		},
		"page below suggestions": {
			"backup_assets.search_page_size_max":    "20",
			"backup_assets.search_suggestion_limit": "21",
		},
		"assignments below bulk": {
			"backup_assets.tag_assignment_quota":   "199",
			"backup_assets.overlay_bulk_max_items": "200",
		},
		"build beyond lease deadline": {
			"backup_assets.search_build_timeout":    "2h1s",
			"backup_assets.lease_absolute_deadline": "2h",
		},
	}
	for name, overrides := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := cloneSettingsValues(values)
			for key, value := range overrides {
				candidate[key] = value
			}
			if err := ValidateBackupAssetFoundationConfig(candidate); err == nil {
				t.Fatalf("unsafe Search/Overlay settings accepted: %#v", overrides)
			}
		})
	}
}

func TestBackupAssetContentConfigCrossSettingBoundaries(t *testing.T) {
	values := backupAssetFoundationValuesForTest()
	if err := ValidateBackupAssetFoundationConfig(values); err != nil {
		t.Fatalf("valid Content defaults rejected: %v", err)
	}

	tests := map[string]map[string]string{
		"idle beyond preview": {
			"backup_assets.content_idle_ttl":    "3m",
			"backup_assets.content_preview_ttl": "2m",
		},
		"request beyond cumulative": {
			"backup_assets.content_request_max_bytes":    "1073741824",
			"backup_assets.content_cumulative_max_bytes": "536870912",
		},
		"grant beyond user concurrency": {
			"backup_assets.content_grant_max_in_flight":  "5",
			"backup_assets.content_user_max_concurrency": "4",
		},
		"content provider beyond provider admission": {
			"backup_assets.content_provider_max_concurrency": "5",
			"backup_assets.provider_max_concurrency":         "4",
		},
		"global below provider concurrency": {
			"backup_assets.content_provider_max_concurrency": "4",
			"backup_assets.content_global_max_concurrency":   "3",
		},
		"user bytes below request": {
			"backup_assets.content_request_max_bytes": "67108864",
			"backup_assets.content_user_window_bytes": "65536",
		},
		"global bytes below provider": {
			"backup_assets.content_provider_window_bytes": "4294967296",
			"backup_assets.content_global_window_bytes":   "1073741824",
		},
		"provider requests below user": {
			"backup_assets.content_user_window_requests":     "4096",
			"backup_assets.content_provider_window_requests": "1024",
		},
		"global requests below provider": {
			"backup_assets.content_provider_window_requests": "8192",
			"backup_assets.content_global_window_requests":   "4096",
		},
		"scan beyond text": {
			"backup_assets.content_classification_scan_bytes": "2097152",
			"backup_assets.content_text_preview_bytes":        "1048576",
		},
		"memory object beyond user": {
			"backup_assets.content_memory_object_bytes": "33554432",
			"backup_assets.content_memory_user_bytes":   "16777216",
		},
		"memory provider beyond global": {
			"backup_assets.content_memory_provider_bytes": "134217728",
			"backup_assets.content_memory_global_bytes":   "67108864",
		},
		"cache chunk beyond object": {
			"backup_assets.content_cache_chunk_bytes":  "8388608",
			"backup_assets.content_cache_object_bytes": "4194304",
		},
		"cache object beyond user": {
			"backup_assets.content_cache_object_bytes": "4294967296",
			"backup_assets.content_cache_user_bytes":   "2147483648",
		},
		"cache provider beyond global": {
			"backup_assets.content_cache_provider_bytes": "17179869184",
			"backup_assets.content_cache_global_bytes":   "8589934592",
		},
		"cache object files cannot hold chunks": {
			"backup_assets.content_cache_chunk_bytes":  "65536",
			"backup_assets.content_cache_object_bytes": "536870912",
			"backup_assets.content_cache_object_files": "1024",
		},
		"cache object files beyond user": {
			"backup_assets.content_cache_object_files": "8192",
			"backup_assets.content_cache_user_files":   "4096",
		},
		"cache provider files beyond global": {
			"backup_assets.content_cache_provider_files": "32768",
			"backup_assets.content_cache_global_files":   "16384",
		},
		"cache idle beyond absolute": {
			"backup_assets.content_cache_idle_ttl":     "3h",
			"backup_assets.content_cache_absolute_ttl": "2h",
		},
		"unsafe cache root": {
			"backup_assets.content_cache_root": "/data/content-cache",
		},
	}
	for name, overrides := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := cloneSettingsValues(values)
			for key, value := range overrides {
				candidate[key] = value
			}
			if err := ValidateBackupAssetFoundationConfig(candidate); err == nil {
				t.Fatalf("unsafe Content settings accepted: %#v", overrides)
			}
		})
	}
}

func TestBackupAssetContentValidationDoesNotInventLegacyCoreOverrides(t *testing.T) {
	if err := ValidateBackupAssetFoundationConfig(map[string]string{
		"backup_assets.provider_max_concurrency": "1",
	}); err != nil {
		t.Fatalf("legacy core-only snapshot rejected by absent Content settings: %v", err)
	}
}

func TestBackupAssetContentSettingsUseDBEnvDefaultPrecedenceInAtomicSnapshot(t *testing.T) {
	t.Setenv("BACKUP_ASSETS_CONTENT_PREVIEW_TTL", "3m")
	service := NewService(setupTestDB(t))
	if err := service.Update("backup_assets.content_preview_ttl", "4m"); err != nil {
		t.Fatalf("persist content override: %v", err)
	}
	values, err := service.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatalf("BackupAssetSettingsSnapshot: %v", err)
	}
	if values["backup_assets.enabled"] != "false" {
		t.Fatalf("content settings changed feature default: %q", values["backup_assets.enabled"])
	}
	if values["backup_assets.content_preview_ttl"] != "4m" {
		t.Fatalf("DB did not override content env: %q", values["backup_assets.content_preview_ttl"])
	}
	if values["backup_assets.content_media_ttl"] != "15m" {
		t.Fatalf("content code default missing from snapshot: %q", values["backup_assets.content_media_ttl"])
	}
	for _, key := range []string{
		"backup_assets.content_ticket_timeout",
		"backup_assets.content_user_window_requests",
		"backup_assets.content_memory_provider_bytes",
		"backup_assets.content_cache_object_files",
		"backup_assets.content_allow_insecure_loopback",
	} {
		if _, exists := values[key]; !exists {
			t.Fatalf("atomic snapshot omitted %s", key)
		}
	}
}

func TestRecoveryAuthorizationReceiptSettingsAtomicSnapshot(t *testing.T) {
	t.Setenv("BACKUP_ASSETS_RECOVERY_RECEIPT_REPLAY_TTL", "25m")
	t.Setenv("BACKUP_ASSETS_RECOVERY_WRITE_GRANT_TTL", "12m")
	t.Setenv("BACKUP_ASSETS_RECOVERY_RECEIPT_REAPER_CADENCE", "2m")
	service := NewService(setupTestDB(t))
	if err := service.UpdateMany(map[string]string{
		"backup_assets.recovery.receipt_replay_ttl":        "30m",
		"backup_assets.recovery.receipt_reaper_batch_size": "64",
	}); err != nil {
		t.Fatalf("persist Recovery authorization settings: %v", err)
	}

	values, err := service.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatalf("BackupAssetSettingsSnapshot: %v", err)
	}
	want := map[string]string{
		"backup_assets.recovery.receipt_replay_ttl":        "30m",
		"backup_assets.recovery.write_grant_ttl":           "12m",
		"backup_assets.recovery.delete_grant_ttl":          "10m",
		"backup_assets.recovery.receipt_reaper_cadence":    "2m",
		"backup_assets.recovery.receipt_reaper_batch_size": "64",
	}
	for key, expected := range want {
		if got, exists := values[key]; !exists || got != expected {
			t.Errorf("atomic Recovery setting %s=%q exists=%v, want %q", key, got, exists, expected)
		}
	}
	if len(values) != len(BackupAssetFoundationSettingKeys()) {
		t.Fatalf("snapshot key count=%d, want %d", len(values), len(BackupAssetFoundationSettingKeys()))
	}
}

func TestBackupAssetRecoveryManagedRuntimeCrossSettingBoundaries(t *testing.T) {
	base := backupAssetFoundationValuesForTest()
	if err := ValidateBackupAssetFoundationConfig(base); err != nil {
		t.Fatalf("valid Recovery defaults rejected: %v", err)
	}
	tests := []map[string]string{
		{"backup_assets.recovery.lease_ttl": "30s", "backup_assets.recovery.lease_renew_margin": "30s"},
		{"backup_assets.recovery.retry_base": "2m", "backup_assets.recovery.retry_max_delay": "1m"},
		{"backup_assets.recovery.result_default_ttl": "24h", "backup_assets.recovery.result_retain_hard_cap": "12h"},
		{"backup_assets.recovery.result_drain_timeout": "5m", "backup_assets.recovery.cleanup_lease_ttl": "5m"},
		{"backup_assets.recovery.cleanup_retry_base": "10m", "backup_assets.recovery.cleanup_retry_max_delay": "5m"},
	}
	for _, changes := range tests {
		candidate := cloneSettingsValues(base)
		for key, value := range changes {
			candidate[key] = value
		}
		if err := ValidateBackupAssetFoundationConfig(candidate); err == nil {
			t.Fatalf("accepted unsafe Recovery settings %v", changes)
		}
	}
}

func TestRecoveryRemovedSettingNamesHaveNoRegistryOrUpdateAlias(t *testing.T) {
	db := setupTestDB(t)
	service := NewService(db)
	removedSuffixes := []string{
		"default" + "_root_id",
		"verification" + "_timeout",
		"orphan" + "_quarantine_limit",
	}
	for _, suffix := range removedSuffixes {
		key := "backup_assets.recovery." + suffix
		if definition := findDef(key); definition != nil {
			t.Fatalf("removed Recovery setting %q retained a registry alias", key)
		}
		if err := service.Update(key, "1"); err == nil {
			t.Fatalf("removed Recovery setting %q was accepted", key)
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			return service.UpdateWithTx(tx, key, "1")
		}); err == nil {
			t.Fatalf("config import accepted removed Recovery setting %q", key)
		}
		if err := service.UpdateMany(map[string]string{key: "1"}); err == nil {
			t.Fatalf("atomic config import accepted removed Recovery setting %q", key)
		}
		var count int64
		if err := db.Model(&model.SystemSetting{}).Where("key = ?", key).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("removed Recovery config import persisted %d rows for %q", count, key)
		}
	}
}

func TestRecoveryReconciliationFindingLimitIsRegistered(t *testing.T) {
	definition := findDef("backup_assets.recovery.reconciliation_finding_limit")
	if definition == nil {
		t.Fatal("Recovery reconciliation finding limit is not registered")
	}
	if definition.CodeDefault != "100" || definition.Min != "1" || definition.Max != "256" ||
		definition.Type != TypeInt || definition.Category != "backup_assets" {
		t.Fatalf("Recovery reconciliation finding limit definition=%+v", definition)
	}
}

func TestBackupAssetSearchConfigAndOverlayConfigSnapshotIsCompleteCopiedAndMutationAtomic(t *testing.T) {
	service := NewService(setupTestDB(t))
	before, err := service.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatalf("BackupAssetSettingsSnapshot: %v", err)
	}
	if len(before) != len(BackupAssetFoundationSettingKeys()) {
		t.Fatalf("snapshot key count=%d, want %d", len(before), len(BackupAssetFoundationSettingKeys()))
	}
	before["backup_assets.search_candidate_limit"] = "mutated-caller-copy"
	again, err := service.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatalf("second BackupAssetSettingsSnapshot: %v", err)
	}
	if again["backup_assets.search_candidate_limit"] != "10000" {
		t.Fatalf("caller mutation corrupted service snapshot: %q", again["backup_assets.search_candidate_limit"])
	}

	firstUpdated := make(chan struct{})
	releaseMutation := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- service.WithBackupAssetMutation(context.Background(), func(map[string]string) error {
			if err := service.Update("backup_assets.search_candidate_limit", "20000"); err != nil {
				return err
			}
			close(firstUpdated)
			<-releaseMutation
			return service.Update("backup_assets.search_page_size_max", "300")
		})
	}()
	<-firstUpdated
	snapshotDone := make(chan map[string]string, 1)
	snapshotErr := make(chan error, 1)
	go func() {
		values, snapshotError := service.BackupAssetSettingsSnapshot()
		if snapshotError != nil {
			snapshotErr <- snapshotError
			return
		}
		snapshotDone <- values
	}()
	select {
	case values := <-snapshotDone:
		t.Fatalf("snapshot observed a mutation mid-transition: %#v", values)
	case err := <-snapshotErr:
		t.Fatalf("snapshot failed during transition: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseMutation)
	if err := <-mutationDone; err != nil {
		t.Fatalf("atomic settings mutation: %v", err)
	}
	select {
	case err := <-snapshotErr:
		t.Fatalf("snapshot after transition: %v", err)
	case values := <-snapshotDone:
		if values["backup_assets.search_candidate_limit"] != "20000" || values["backup_assets.search_page_size_max"] != "300" {
			t.Fatalf("snapshot observed half transition: %#v", values)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot remained blocked after settings transition")
	}
}

func backupAssetFoundationValuesForTest() map[string]string {
	values := make(map[string]string)
	for _, def := range registry {
		if strings.HasPrefix(def.Key, "backup_assets.") {
			values[def.Key] = def.CodeDefault
		}
	}
	return values
}

func cloneSettingsValues(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func TestAnomalyDefaults_AreConservativeAndAlertsOff(t *testing.T) {
	svc := NewService(setupTestDB(t))
	cases := map[string]string{
		"anomaly.enabled":           "true",
		"anomaly.alerts_enabled":    "false",
		"anomaly.ewma_sigma":        "5.0",
		"anomaly.ewma_window_hours": "6",
		"anomaly.ewma_min_samples":  "24",
	}
	for key, want := range cases {
		if got := svc.GetEffective(key); got != want {
			t.Errorf("%s default = %q, want %q", key, got, want)
		}
	}
}

func TestSensitiveSettingPersistsEncryptedAndReadsPlaintext(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	db := setupTestDB(t)
	svc := NewService(db)

	if err := svc.Update("smtp.password", "FAKE_SMTP_PASSWORD_FOR_TEST_ONLY"); err != nil {
		t.Fatalf("update sensitive setting: %v", err)
	}
	var row model.SystemSetting
	if err := db.First(&row, "key = ?", "smtp.password").Error; err != nil {
		t.Fatalf("load stored setting: %v", err)
	}
	if !strings.HasPrefix(row.Value, "enc:v2:") || strings.Contains(row.Value, "FAKE_SMTP_PASSWORD_FOR_TEST_ONLY") {
		t.Fatalf("expected encrypted stored value, got %q", row.Value)
	}
	if got := svc.GetEffective("smtp.password"); got != "FAKE_SMTP_PASSWORD_FOR_TEST_ONLY" {
		t.Fatalf("expected decrypted effective value, got %q", got)
	}
	all, err := svc.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if got := all["smtp.password"].Value; got != "FAKE_SMTP_PASSWORD_FOR_TEST_ONLY" {
		t.Fatalf("expected decrypted GetAll value, got %q", got)
	}
}

func TestSensitiveSettingEmptyValuePersistsWithoutEncryption(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	db := setupTestDB(t)
	svc := NewService(db)

	if err := svc.Update("smtp.password", ""); err != nil {
		t.Fatalf("update empty sensitive setting: %v", err)
	}
	var row model.SystemSetting
	if err := db.First(&row, "key = ?", "smtp.password").Error; err != nil {
		t.Fatalf("load stored setting: %v", err)
	}
	if row.Value != "" {
		t.Fatalf("empty sensitive setting should stay empty, got %q", row.Value)
	}
}

func TestGetEffectiveDBErrorKeepsExpiredCache(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	svc.cache["login.rate_limit"] = cachedValue{value: "77", expiresAt: time.Now().Add(-time.Minute)}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if got := svc.GetEffective("login.rate_limit"); got != "77" {
		t.Fatalf("DB error should return stale cached value, got %q", got)
	}
}

func TestGetEffective_Default(t *testing.T) {
	svc := NewService(setupTestDB(t))
	val := svc.GetEffective("login.rate_limit")
	if val != "10" {
		t.Errorf("expected '10', got '%s'", val)
	}
}

func TestGetEffective_EnvOverride(t *testing.T) {
	t.Setenv("LOGIN_RATE_LIMIT", "20")
	svc := NewService(setupTestDB(t))
	val := svc.GetEffective("login.rate_limit")
	if val != "20" {
		t.Errorf("expected '20', got '%s'", val)
	}
}

func TestGetEffective_DBOverride(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	if err := svc.Update("login.rate_limit", "30"); err != nil {
		t.Fatal(err)
	}
	val := svc.GetEffective("login.rate_limit")
	if val != "30" {
		t.Errorf("expected '30', got '%s'", val)
	}
}

func TestGetEffective_DBOverridesEnv(t *testing.T) {
	t.Setenv("LOGIN_RATE_LIMIT", "20")
	db := setupTestDB(t)
	svc := NewService(db)
	_ = svc.Update("login.rate_limit", "30")
	val := svc.GetEffective("login.rate_limit")
	if val != "30" {
		t.Errorf("expected DB value '30' to override env '20', got '%s'", val)
	}
}

func TestUpdate_Invalid(t *testing.T) {
	svc := NewService(setupTestDB(t))
	if err := svc.Update("login.rate_limit", "abc"); err == nil {
		t.Error("expected error for non-integer value")
	}
	if err := svc.Update("unknown.key", "1"); err == nil {
		t.Error("expected error for unknown key")
	}
}

func TestUpdate_SecurityFloor(t *testing.T) {
	svc := NewService(setupTestDB(t))
	// login.rate_limit Min=5
	if err := svc.Update("login.rate_limit", "2"); err == nil {
		t.Error("expected error: rate_limit below security floor of 5")
	}
	// login.fail_lock_threshold Min=3
	if err := svc.Update("login.fail_lock_threshold", "1"); err == nil {
		t.Error("expected error: lock threshold below security floor of 3")
	}
	// login.rate_window MinDuration=10s
	if err := svc.Update("login.rate_window", "5s"); err == nil {
		t.Error("expected error: rate_window below 10s floor")
	}
	// login.fail_lock_duration MinDuration=1m
	if err := svc.Update("login.fail_lock_duration", "30s"); err == nil {
		t.Error("expected error: lock_duration below 1m floor")
	}
}

func TestUpdate_ValueTooLong(t *testing.T) {
	svc := NewService(setupTestDB(t))
	longVal := make([]byte, maxValueLength+1)
	for i := range longVal {
		longVal[i] = '1'
	}
	if err := svc.Update("login.rate_limit", string(longVal)); err == nil {
		t.Error("expected error for value exceeding max length")
	}
}

func TestValidate_Bool(t *testing.T) {
	svc := NewService(setupTestDB(t))
	if err := svc.Validate("login.captcha_enabled", "true"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := svc.Validate("login.captcha_enabled", "false"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := svc.Validate("login.captcha_enabled", "yes"); err == nil {
		t.Error("expected error for non-bool value")
	}
}

func TestValidate_Duration(t *testing.T) {
	svc := NewService(setupTestDB(t))
	if err := svc.Validate("alert.dedup_window", "5m"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := svc.Validate("alert.dedup_window", "-1m"); err == nil {
		t.Error("expected error for negative duration")
	}
	if err := svc.Validate("alert.dedup_window", "invalid"); err == nil {
		t.Error("expected error for invalid duration")
	}
}

func TestDelete(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	_ = svc.Update("login.rate_limit", "30")
	if err := svc.Delete("login.rate_limit"); err != nil {
		t.Fatal(err)
	}
	val := svc.GetEffective("login.rate_limit")
	if val != "10" {
		t.Errorf("expected default '10' after delete, got '%s'", val)
	}
}

func TestGetAll(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	_ = svc.Update("login.rate_limit", "25")
	all, err := svc.GetAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(registry) {
		t.Errorf("expected %d settings, got %d", len(registry), len(all))
	}
	if all["login.rate_limit"].Source != "db" {
		t.Errorf("expected source 'db', got '%s'", all["login.rate_limit"].Source)
	}
	if all["login.rate_limit"].Value != "25" {
		t.Errorf("expected '25', got '%s'", all["login.rate_limit"].Value)
	}
}

func TestCache_InvalidatedOnUpdate(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	// Prime cache
	val := svc.GetEffective("login.rate_limit")
	if val != "10" {
		t.Fatalf("expected '10', got '%s'", val)
	}
	// Update should invalidate cache
	_ = svc.Update("login.rate_limit", "50")
	val = svc.GetEffective("login.rate_limit")
	if val != "50" {
		t.Errorf("expected '50' after update, got '%s' (cache not invalidated?)", val)
	}
}

func TestCache_InvalidatedOnDelete(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	_ = svc.Update("login.rate_limit", "50")
	// Prime cache with DB value
	_ = svc.GetEffective("login.rate_limit")
	// Delete should invalidate cache
	_ = svc.Delete("login.rate_limit")
	val := svc.GetEffective("login.rate_limit")
	if val != "10" {
		t.Errorf("expected default '10' after delete, got '%s' (cache not invalidated?)", val)
	}
}
