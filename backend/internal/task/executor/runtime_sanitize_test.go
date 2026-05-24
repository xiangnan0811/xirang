package executor

import (
	"strings"
	"testing"
)

func TestSanitizeExecutorRuntimeEvidenceHidesSecretsAndEndpoints(t *testing.T) {
	message := `copied /srv/private/source to root@backup.internal.example:/repo/tenant-a via https://backup.internal.example/api?token=FAKE_EXECUTOR_TOKEN_FOR_TEST_ONLY output=/tmp/raw-output password=FAKE_EXECUTOR_PASSWORD_FOR_TEST_ONLY`

	got := sanitizeExecutorRuntimeEvidence(message)

	for _, forbidden := range []string{
		"/srv/private/source",
		"backup.internal.example",
		"/repo/tenant-a",
		"/tmp/raw-output",
		"FAKE_EXECUTOR_TOKEN_FOR_TEST_ONLY",
		"FAKE_EXECUTOR_PASSWORD_FOR_TEST_ONLY",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("executor runtime evidence leaked %q: %s", forbidden, got)
		}
	}
	for _, expected := range []string{"https://***", "[输出已隐藏]"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected sanitized placeholder %q in %q", expected, got)
		}
	}
}

func TestSanitizeExecutorRuntimeEvidenceHidesNamedRemotePaths(t *testing.T) {
	message := `rclone copied remote:backup/tenant-a to target:private/tenant-b token=FAKE_EXECUTOR_NAMED_REMOTE_TOKEN_FOR_TEST_ONLY`

	got := sanitizeExecutorRuntimeEvidence(message)

	for _, forbidden := range []string{"remote:backup/tenant-a", "target:private/tenant-b", "FAKE_EXECUTOR_NAMED_REMOTE_TOKEN_FOR_TEST_ONLY"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("executor runtime evidence leaked %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "[远程路径已隐藏]") {
		t.Fatalf("expected remote path placeholder in %q", got)
	}
}

func TestSanitizeExecutorRuntimeOutputSuppressesRawOutput(t *testing.T) {
	output := `restic output contains /srv/private/source backup.internal.example token=FAKE_EXECUTOR_OUTPUT_TOKEN_FOR_TEST_ONLY`

	got := sanitizeExecutorRuntimeOutput(output)

	if got != "[输出已隐藏]" {
		t.Fatalf("expected output placeholder, got %q", got)
	}
	for _, forbidden := range []string{"/srv/private/source", "backup.internal.example", "FAKE_EXECUTOR_OUTPUT_TOKEN_FOR_TEST_ONLY"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("executor runtime output leaked %q: %s", forbidden, got)
		}
	}
}
