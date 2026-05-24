package snapshot

import (
	"errors"
	"strings"
	"testing"

	"xirang/backend/internal/task/executor"
)

func TestEscapeLikePattern_Normal(t *testing.T) {
	result := EscapeLikePattern("nginx.conf")
	if result != "nginx.conf" {
		t.Fatalf("期望 nginx.conf 不变，实际: %s", result)
	}
}

func TestEscapeLikePattern_Percent(t *testing.T) {
	result := EscapeLikePattern("100%")
	if result != `100\%` {
		t.Fatalf("期望 %% 被转义为 \\%%，实际: %s", result)
	}
}

func TestEscapeLikePattern_Underscore(t *testing.T) {
	result := EscapeLikePattern("file_name")
	if result != `file\_name` {
		t.Fatalf("期望 _ 被转义为 \\_，实际: %s", result)
	}
}

func TestEscapeLikePattern_Both(t *testing.T) {
	result := EscapeLikePattern("100%_file")
	if result != `100\%\_file` {
		t.Fatalf("期望 %% 和 _ 同时转义，实际: %s", result)
	}
}

func TestEscapeLikePattern_Empty(t *testing.T) {
	result := EscapeLikePattern("")
	if result != "" {
		t.Fatalf("期望空字符串不变，实际: %s", result)
	}
}

func TestEscapeLikePattern_Backslash(t *testing.T) {
	result := EscapeLikePattern(`path\to\file`)
	if result != `path\\to\\file` {
		t.Fatalf("期望 \\ 被转义为 \\\\，实际: %s", result)
	}
}

func TestResticFindFailureErrorHidesNonEmptyOutput(t *testing.T) {
	rawOutput := strings.Join([]string{
		"repository password rejected for token=FAKE_TOKEN_FOR_TEST_ONLY",
		"host backup.internal.example.com endpoint https://backup.internal.example.com/repo",
		"stdout: /srv/backup/private/path FAKE_COMMAND_OUTPUT_FOR_TEST_ONLY",
	}, "\n")

	err := newResticFindFailureError(errors.New("exit status 1"), rawOutput)
	errText := err.Error()
	if !strings.Contains(errText, "[输出已隐藏]") {
		t.Fatalf("期望非空输出使用隐藏占位符，实际: %s", errText)
	}
	for _, forbidden := range []string{
		"FAKE_TOKEN_FOR_TEST_ONLY",
		"backup.internal.example.com",
		"https://backup.internal.example.com/repo",
		"/srv/backup/private/path",
		"FAKE_COMMAND_OUTPUT_FOR_TEST_ONLY",
	} {
		if strings.Contains(errText, forbidden) {
			t.Fatalf("错误字符串泄露原始输出片段 %q: %s", forbidden, errText)
		}
	}
}

func TestResticFindFailureErrorOmitsOutputForEmptyOutput(t *testing.T) {
	err := newResticFindFailureError(errors.New("exit status 1"), " \n\t ")
	errText := err.Error()
	if strings.Contains(errText, "输出:") || strings.Contains(errText, "[输出已隐藏]") {
		t.Fatalf("空输出不应附加输出字段或占位符，实际: %s", errText)
	}
}

func TestParseResticFindOutput_Empty(t *testing.T) {
	entries := parseResticFindOutput("")
	if len(entries) != 0 {
		t.Fatalf("期望空输出返回 0 条目，实际: %d", len(entries))
	}
}

func TestParseResticFindOutput_SingleMatch(t *testing.T) {
	output := `{"matches":[{"path":"/etc/nginx/nginx.conf","type":"file","size":1234,"mtime":"2024-01-01T00:00:00Z"}],"hits":1}`
	entries := parseResticFindOutput(output)
	if len(entries) != 1 {
		t.Fatalf("期望 1 条目，实际: %d", len(entries))
	}
	if entries[0].Path != "/etc/nginx/nginx.conf" {
		t.Fatalf("期望路径 /etc/nginx/nginx.conf，实际: %s", entries[0].Path)
	}
	if entries[0].Size != 1234 {
		t.Fatalf("期望大小 1234，实际: %d", entries[0].Size)
	}
	if entries[0].Mtime != "2024-01-01T00:00:00Z" {
		t.Fatalf("期望 mtime 2024-01-01T00:00:00Z，实际: %s", entries[0].Mtime)
	}
}

func TestParseResticFindOutput_MultipleMatches(t *testing.T) {
	output := `{"matches":[{"path":"/etc/nginx/nginx.conf","type":"file","size":1234,"mtime":"2024-01-01T00:00:00Z"},{"path":"/etc/nginx/sites/default","type":"file","size":5678,"mtime":"2024-01-02T00:00:00Z"}],"hits":2}`
	entries := parseResticFindOutput(output)
	if len(entries) != 2 {
		t.Fatalf("期望 2 条目，实际: %d", len(entries))
	}
}

func TestParseResticFindOutput_MultipleLines(t *testing.T) {
	output := `{"matches":[{"path":"/file1.txt","type":"file","size":100,"mtime":"2024-01-01T00:00:00Z"}],"hits":1}
{"matches":[{"path":"/file2.txt","type":"file","size":200,"mtime":"2024-01-02T00:00:00Z"}],"hits":1}`
	entries := parseResticFindOutput(output)
	if len(entries) != 2 {
		t.Fatalf("期望 2 条目，实际: %d", len(entries))
	}
}

func TestParseResticFindOutput_IgnoresNonJSON(t *testing.T) {
	output := `some error message
{"matches":[{"path":"/file.txt","type":"file","size":100,"mtime":"2024-01-01T00:00:00Z"}],"hits":1}
another non-json line`
	entries := parseResticFindOutput(output)
	if len(entries) != 1 {
		t.Fatalf("期望跳过非 JSON 行后得到 1 条目，实际: %d", len(entries))
	}
}

func TestParseResticFindOutput_EmptyMatches(t *testing.T) {
	output := `{"matches":[],"hits":0}`
	entries := parseResticFindOutput(output)
	if len(entries) != 0 {
		t.Fatalf("期望 0 条目，实际: %d", len(entries))
	}
}

func TestResolveResticRepositoryAccessForIndex_Empty(t *testing.T) {
	access, err := executor.ResolveResticRepositoryAccess("")
	if err != nil {
		t.Fatalf("ResolveResticRepositoryAccess 不应失败: %v", err)
	}
	if access.Password() != "" {
		t.Fatalf("期望空访问口令，实际: %s", access.Password())
	}
}

func TestResolveResticRepositoryAccessForIndex_WithPassword(t *testing.T) {
	access, err := executor.ResolveResticRepositoryAccess(`{"repository_password":"FAKE_PASSWORD_FOR_TEST_ONLY"}`)
	if err != nil {
		t.Fatalf("ResolveResticRepositoryAccess 不应失败: %v", err)
	}
	if access.Password() != "FAKE_PASSWORD_FOR_TEST_ONLY" {
		t.Fatalf("期望访问口令匹配，实际: %s", access.Password())
	}
}

func TestBuildResticEnvPrefixForIndex_EmptyPassword(t *testing.T) {
	result := executor.BuildResticEnvPrefix(executor.NewResticRepositoryAccess(""))
	if result != "RESTIC_PASSWORD=''" {
		t.Fatalf("期望空访问口令，实际: %s", result)
	}
}

func TestBuildResticEnvPrefixForIndex_WithPassword(t *testing.T) {
	result := executor.BuildResticEnvPrefix(executor.NewResticRepositoryAccess("FAKE_PASSWORD_FOR_TEST_ONLY"))
	if result == "" || result == "RESTIC_PASSWORD=''" || !strings.HasPrefix(result, "RESTIC_PASSWORD=") {
		t.Fatalf("期望访问口令被设置，实际: %s", result)
	}
}
