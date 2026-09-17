package model

import (
	"strings"
	"testing"

	"xirang/backend/internal/secure"
)

func TestServiceMonitorHeaderHooksEncryptAndDecrypt(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	defer secure.ResetForTesting()

	const plaintext = `{"Authorization":"FAKE_MONITOR_SECRET_FOR_TEST_ONLY"}`
	monitor := &ServiceMonitor{HTTPHeaders: plaintext}
	if err := monitor.BeforeSave(nil); err != nil {
		t.Fatalf("BeforeSave: %v", err)
	}
	if !strings.HasPrefix(monitor.HTTPHeaders, "enc:v2:") {
		t.Fatalf("headers not encrypted: %q", monitor.HTTPHeaders)
	}
	if strings.Contains(monitor.HTTPHeaders, "FAKE_MONITOR_SECRET_FOR_TEST_ONLY") {
		t.Fatal("ciphertext contains plaintext secret")
	}
	if err := monitor.AfterFind(nil); err != nil {
		t.Fatalf("AfterFind: %v", err)
	}
	if monitor.HTTPHeaders != plaintext {
		t.Fatalf("decrypted headers=%q, want %q", monitor.HTTPHeaders, plaintext)
	}
}

func TestServiceMonitorHeaderHooksSealEmptyJSONDefault(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	defer secure.ResetForTesting()

	monitor := &ServiceMonitor{}
	if err := monitor.BeforeSave(nil); err != nil {
		t.Fatalf("BeforeSave: %v", err)
	}
	if !strings.HasPrefix(monitor.HTTPHeaders, "enc:v2:") {
		t.Fatalf("default headers not encrypted: %q", monitor.HTTPHeaders)
	}
	if err := monitor.AfterFind(nil); err != nil {
		t.Fatalf("AfterFind: %v", err)
	}
	if monitor.HTTPHeaders != "{}" {
		t.Fatalf("default headers=%q, want {}", monitor.HTTPHeaders)
	}
}
