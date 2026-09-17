package task

import (
	"encoding/json"
	"strings"
	"testing"

	"xirang/backend/internal/secure"
)

func TestMigrateLegacyResticConfigCanonicalizesAppendOnly(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		want       string
		wantChange bool
	}{
		{
			name:       "true selects version two",
			input:      `{"repository_password":"FAKE_RESTIC_CONVERTER_PASSWORD_FOR_TEST_ONLY","append_only":true,"exclude_patterns":["cache"],"unknown":{"keep":true}}`,
			want:       `{"exclude_patterns":["cache"],"repository_password":"FAKE_RESTIC_CONVERTER_PASSWORD_FOR_TEST_ONLY","repository_version":2,"unknown":{"keep":true}}`,
			wantChange: true,
		},
		{
			name:       "false selects default",
			input:      `{"append_only":false,"repository_version":1,"unknown":"keep"}`,
			want:       `{"unknown":"keep"}`,
			wantChange: true,
		},
		{
			name:       "matching explicit version remains canonical",
			input:      `{"append_only":true,"repository_version":2}`,
			want:       `{"repository_version":2}`,
			wantChange: true,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, changed, err := MigrateLegacyResticConfig(testCase.input)
			if err != nil {
				t.Fatalf("migration error: %v", err)
			}
			if changed != testCase.wantChange || got != testCase.want {
				t.Fatalf("migration got changed=%v config=%s, want changed=%v config=%s", changed, got, testCase.wantChange, testCase.want)
			}
		})
	}
}

func TestMigrateLegacyResticConfigRejectsAmbiguousOrMalformedValues(t *testing.T) {
	for _, raw := range []string{
		`{"append_only":true,"repository_version":1}`,
		`{"append_only":false,"repository_version":2}`,
		`{"append_only":null}`,
		`{"append_only":true,"repository_version":"2"}`,
		`{"append_only":true} trailing`,
	} {
		t.Run(strings.ReplaceAll(raw, "\"", ""), func(t *testing.T) {
			if _, _, err := MigrateLegacyResticConfig(raw); err == nil {
				t.Fatalf("migration accepted ambiguous/malformed config %s", raw)
			}
		})
	}
}

func TestNormalizeImportedResticConfigDecryptsAndMigratesAtImportBoundary(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RESTIC_IMPORT_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	ciphertext, err := secure.EncryptString(`{"repository_password":"FAKE_RESTIC_IMPORT_PASSWORD_FOR_TEST_ONLY","append_only":true}`)
	if err != nil {
		t.Fatalf("encrypt imported config: %v", err)
	}
	migrated, err := NormalizeImportedResticConfig(ciphertext)
	if err != nil {
		t.Fatalf("normalize imported config: %v", err)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(migrated), &config); err != nil {
		t.Fatalf("decode migrated import: %v", err)
	}
	if _, exists := config["append_only"]; exists {
		t.Fatal("import migration retained append_only")
	}
	if got := string(config["repository_version"]); got != "2" {
		t.Fatalf("import migration repository_version=%s, want 2", got)
	}
	if got := string(config["repository_password"]); got != `"FAKE_RESTIC_IMPORT_PASSWORD_FOR_TEST_ONLY"` {
		t.Fatalf("import migration changed secret: %s", got)
	}
}
