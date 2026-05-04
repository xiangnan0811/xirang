package handlers

import (
	"strings"
	"testing"
)

func TestValidatePathChars_AcceptsLegalPaths(t *testing.T) {
	cases := []string{
		"/data/app",
		"/data/with space/sub",
		"/data/with-dash_and.dot",
		"./relative/path",
		"中文路径/sub",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			if err := validatePathChars(p, "test"); err != nil {
				t.Fatalf("合法路径被拒绝: %q -> %v", p, err)
			}
		})
	}
}

func TestValidatePathChars_RejectsInjectionPatterns(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string // 错误信息片段
	}{
		{"NUL", "/safe/\x00bad", "NUL"},
		{"LF", "line1\nline2", "换行"},
		{"CR", "line1\rline2", "换行"},
		{"backtick", "x`whoami`y", "反引号"},
		{"dollar paren", "x$(id)y", "$("},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePathChars(tc.input, "rsync_source")
			if err == nil {
				t.Fatalf("期望被拒绝但通过: %q", tc.input)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息缺关键词 %q: %v", tc.want, err)
			}
			if !strings.Contains(err.Error(), "rsync_source") {
				t.Fatalf("错误信息缺 label: %v", err)
			}
		})
	}
}

func TestValidatePathChars_BypassEnv(t *testing.T) {
	t.Setenv("BACKUP_PATH_ALLOW_SHELL_META", "true")
	if err := validatePathChars("x`whoami`y", "test"); err != nil {
		t.Fatalf("BACKUP_PATH_ALLOW_SHELL_META=true 时应放行: %v", err)
	}
}
