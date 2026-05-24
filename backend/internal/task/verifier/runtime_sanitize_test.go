package verifier

import (
	"strings"
	"testing"
)

func TestSanitizeVerifierRuntimeEvidenceHidesPathHostAndTokens(t *testing.T) {
	message := `文件校验不一致: /srv/private/file.txt on backup.internal.example output=/tmp/raw-output token=FAKE_VERIFIER_TOKEN_FOR_TEST_ONLY`

	got := sanitizeVerifierRuntimeEvidence(message)

	for _, forbidden := range []string{
		"/srv/private/file.txt",
		"backup.internal.example",
		"/tmp/raw-output",
		"FAKE_VERIFIER_TOKEN_FOR_TEST_ONLY",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("verifier runtime evidence leaked %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "[输出已隐藏]") {
		t.Fatalf("expected output placeholder in %q", got)
	}
}

func TestSanitizeVerifierRuntimeEvidenceHidesNamedRemotePaths(t *testing.T) {
	message := `rclone check remote:backup/tenant-a target:private/tenant-b token=FAKE_VERIFIER_NAMED_REMOTE_TOKEN_FOR_TEST_ONLY`

	got := sanitizeVerifierRuntimeEvidence(message)

	for _, forbidden := range []string{"remote:backup/tenant-a", "target:private/tenant-b", "FAKE_VERIFIER_NAMED_REMOTE_TOKEN_FOR_TEST_ONLY"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("verifier runtime evidence leaked %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "[远程路径已隐藏]") {
		t.Fatalf("expected remote path placeholder in %q", got)
	}
}
