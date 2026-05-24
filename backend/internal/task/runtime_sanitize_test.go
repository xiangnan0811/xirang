package task

import (
	"strings"
	"testing"
)

func assertTaskRuntimeTextSanitized(t *testing.T, text string, forbidden []string) {
	t.Helper()
	for _, item := range forbidden {
		if strings.Contains(text, item) {
			t.Fatalf("runtime text leaked sensitive fragment %q: %s", item, text)
		}
	}
}

func TestSanitizeRuntimeEvidenceForReadHidesLegacyStoredEvidence(t *testing.T) {
	msg := `执行命令: restic restore --target /srv/private/restore; stdout: https://legacy.internal.example/api?token=FAKE_READ_BOUNDARY_TOKEN_FOR_TEST_ONLY root@legacy.internal.example:/backup/tenant-a`

	got := SanitizeRuntimeEvidenceForRead(msg)

	assertTaskRuntimeTextSanitized(t, got, []string{"restic", "/srv/private/restore", "legacy.internal.example", "FAKE_READ_BOUNDARY_TOKEN_FOR_TEST_ONLY", "/backup/tenant-a"})
	if !strings.Contains(got, "[命令已隐藏]") {
		t.Fatalf("expected command placeholder in %q", got)
	}
}

func TestSanitizeTaskLastErrorHidesRuntimeEvidence(t *testing.T) {
	msg := `Post "https://backup.internal.example/api?token=FAKE_RUNTIME_TOKEN_FOR_TEST_ONLY": lookup backup.internal.example: no such host; source=/srv/private/db.sql; output=/tmp/raw-output root@db.internal.example:/backup/tenant-a`
	got := sanitizeTaskLastError(msg)

	assertTaskRuntimeTextSanitized(t, got, []string{"backup.internal.example", "FAKE_RUNTIME_TOKEN_FOR_TEST_ONLY", "/srv/private/db.sql", "/tmp/raw-output", "db.internal.example", "/backup/tenant-a"})
	for _, expected := range []string{"https://***", "[路径已隐藏]", "[输出已隐藏]"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected sanitized placeholder %q in %q", expected, got)
		}
	}
}

func TestSanitizeRuntimeEvidenceForReadHidesNamedRemotePaths(t *testing.T) {
	msg := `legacy rclone line copied remote:backup/tenant-a to target:private/tenant-b token=FAKE_NAMED_REMOTE_TOKEN_FOR_TEST_ONLY`

	got := SanitizeRuntimeEvidenceForRead(msg)

	assertTaskRuntimeTextSanitized(t, got, []string{"remote:backup/tenant-a", "target:private/tenant-b", "FAKE_NAMED_REMOTE_TOKEN_FOR_TEST_ONLY"})
	if !strings.Contains(got, "[远程路径已隐藏]") {
		t.Fatalf("expected remote path placeholder in %q", got)
	}
}

func TestSanitizeTaskLogMessageHidesCommandLifecycleText(t *testing.T) {
	tests := []string{
		`执行命令: curl https://hooks.example.test/services/FAKE_TASK_LOG_TOKEN_FOR_TEST_ONLY?secret=FAKE_QUERY_FOR_TEST_ONLY && rsync /srv/private/source root@db.internal.example:/backup/tenant-a`,
		`legacy failed command: curl https://hooks.example.test/services/FAKE_TASK_LOG_TOKEN_FOR_TEST_ONLY?secret=FAKE_QUERY_FOR_TEST_ONLY && rsync /srv/private/source root@db.internal.example:/backup/tenant-a`,
	}

	for _, message := range tests {
		got := sanitizeTaskLogMessage(message)

		assertTaskRuntimeTextSanitized(t, got, []string{"curl", "rsync", "hooks.example.test", "FAKE_TASK_LOG_TOKEN_FOR_TEST_ONLY", "FAKE_QUERY_FOR_TEST_ONLY", "/srv/private/source", "db.internal.example", "/backup/tenant-a"})
		if !strings.Contains(got, "[命令已隐藏]") {
			t.Fatalf("expected command placeholder in %q", got)
		}
	}
}

func TestSanitizeTaskRuntimeEvidenceHidesHostSensitiveLabels(t *testing.T) {
	message := `恢复演练失败: sandbox=tenant-sandbox-a node=backup-node-prod restore-host private-host output=/tmp/raw-output`

	got := sanitizeTaskLastError(message)

	assertTaskRuntimeTextSanitized(t, got, []string{"tenant-sandbox-a", "backup-node-prod", "restore-host", "private-host", "/tmp/raw-output"})
	if !strings.Contains(got, "[主机信息已隐藏]") {
		t.Fatalf("expected host-sensitive placeholder in %q", got)
	}
}

func TestSanitizeTaskRuntimeOutputSuppressesRawOutput(t *testing.T) {
	output := "copied /srv/private/source to backup.internal.example token=FAKE_RUNTIME_OUTPUT_TOKEN_FOR_TEST_ONLY"

	got := sanitizeTaskRuntimeOutput(output)

	assertTaskRuntimeTextSanitized(t, got, []string{"/srv/private/source", "backup.internal.example", "FAKE_RUNTIME_OUTPUT_TOKEN_FOR_TEST_ONLY"})
	if got != "[输出已隐藏]" {
		t.Fatalf("expected output placeholder, got %q", got)
	}
}
