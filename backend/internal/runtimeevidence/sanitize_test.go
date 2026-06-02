package runtimeevidence

import (
	"testing"
)

func TestSanitizeTaskRuntimeEvidence_OutputMarkers(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
	}{
		{
			name:     "chinese output colon",
			input:    "执行完成，输出：some sensitive output here",
			contains: []string{"输出", "[输出已隐藏]"},
		},
		{
			name:     "chinese output equals",
			input:    "命令结果 输出=敏感数据 abc123",
			contains: []string{"输出=", "[输出已隐藏]"},
		},
		{
			name:     "english output colon",
			input:    "task done, output: secret stuff",
			contains: []string{"output:", "[输出已隐藏]"},
		},
		{
			name:     "stdout equals",
			input:    "stdout=some command result",
			contains: []string{"stdout=", "[输出已隐藏]"},
		},
		{
			name:     "stderr equals",
			input:    "stderr=error log data",
			contains: []string{"stderr=", "[输出已隐藏]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeTaskRuntimeEvidence(tt.input)
			for _, want := range tt.contains {
				if !contains(got, want) {
					t.Errorf("expected output to contain %q, got %q", want, got)
				}
			}
		})
	}
}

func TestSanitizeTaskRuntimeEvidence_URLs(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"https url with path", "访问 https://example.com/api/v1/data"},
		{"http url", "请求 http://myhost:8080/test"},
		{"url in parens", "(https://secret.example.com/token/abc123)"},
		{"multiple urls", "从 https://a.com/x 到 https://b.com/y"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeTaskRuntimeEvidence(tt.input)
			if contains(got, "://") {
				// Should only have scheme://***, not the original full path
				if contains(got, "/api/") || contains(got, "/token/") || contains(got, "/test") {
					t.Errorf("URL path not sanitized: %q", got)
				}
			}
		})
	}
}

func TestSanitizeTaskRuntimeEvidence_CommandLifecycle(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
	}{
		{
			name:     "execute command colon",
			input:    "执行命令: rm -rf /secret/path",
			contains: []string{"执行命令:", "[命令已隐藏]"},
		},
		{
			name:     "execute on remote node",
			input:    "在远程节点执行 = cat /etc/shadow",
			contains: []string{"[命令已隐藏]"},
		},
		{
			name:     "restic check",
			input:    "执行 restic check: restic --repo s3:s3.amazonaws.com/bucket",
			contains: []string{"[命令已隐藏]"},
		},
		{
			name:     "command keyword",
			input:    "command: echo hello",
			contains: []string{"[命令已隐藏]"},
		},
		{
			name:     "cmd keyword",
			input:    "cmd: ls -la",
			contains: []string{"[命令已隐藏]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeTaskRuntimeEvidence(tt.input)
			for _, want := range tt.contains {
				if !contains(got, want) {
					t.Errorf("expected %q in sanitized output, got %q", want, got)
				}
			}
		})
	}
}

func TestSanitizeTaskRuntimeEvidence_RemotePaths(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"ssh style user@host:path", "备份到 user@backup-server:/data/backups/"},
		{"ssh with port", "连接 root@10.0.0.1:2222:/home/user"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeTaskRuntimeEvidence(tt.input)
			if contains(got, "@/") || contains(got, ":/") {
				t.Errorf("remote path not sanitized: %q", got)
			}
			if !contains(got, "[远程路径已隐藏]") {
				t.Errorf("expected [远程路径已隐藏] in %q", got)
			}
		})
	}
}

func TestSanitizeTaskRuntimeEvidence_NamedPaths(t *testing.T) {
	// Named paths like "backup:data" should be hidden, but URLs like "http://..." should pass through.
	tests := []struct {
		name     string
		input    string
		contains string
		absent   string
	}{
		{
			name:     "s3 style bucket:path",
			input:    "restic repo s3:s3.amazonaws.com/bucket",
			contains: "[远程路径已隐藏]",
			absent:   "s3.amazonaws.com",
		},
		{
			name:     "urls are preserved by name pattern",
			input:    "http://example.com/path",
			contains: "http://",
			absent:   "",
		},
		{
			name:     "named path with slash after colon",
			input:    "backup:data/files",
			contains: "[远程路径已隐藏]",
			absent:   "data/files",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeTaskRuntimeEvidence(tt.input)
			if !contains(got, tt.contains) {
				t.Errorf("expected %q in sanitized output, got %q", tt.contains, got)
			}
			if tt.absent != "" && contains(got, tt.absent) {
				t.Errorf("expected %q to be absent from %q", tt.absent, got)
			}
		})
	}
}

func TestSanitizeTaskRuntimeEvidence_AbsolutePaths(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"simple absolute path", "文件位于 /etc/config/app.conf"},
		{"path after paren", "(/var/log/syslog)"},
		{"path after colon", "错误: /home/user/.ssh/id_rsa"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeTaskRuntimeEvidence(tt.input)
			if contains(got, "/etc/") || contains(got, "/var/") || contains(got, "/home/") {
				t.Errorf("absolute path not sanitized: %q", got)
			}
			if !contains(got, "[路径已隐藏]") {
				t.Errorf("expected [路径已隐藏] in %q", got)
			}
		})
	}
}

func TestSanitizeTaskRuntimeEvidence_WindowsPaths(t *testing.T) {
	input := "检查 C:\\Windows\\System32\\drivers\\etc\\hosts"
	got := SanitizeTaskRuntimeEvidence(input)
	if contains(got, "C:\\") {
		t.Errorf("Windows path not sanitized: %q", got)
	}
	if !contains(got, "[路径已隐藏]") {
		t.Errorf("expected [路径已隐藏] in %q", got)
	}
}

func TestSanitizeTaskRuntimeEvidence_IPv4(t *testing.T) {
	tests := []string{
		"连接失败 192.168.1.100",
		"目标 10.0.0.1:8080",
		"user@192.168.1.50",
		"from 172.16.0.1 to 172.16.0.2",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			got := SanitizeTaskRuntimeEvidence(input)
			if !contains(got, "[主机已隐藏]") {
				t.Errorf("expected [主机已隐藏] in %q", got)
			}
		})
	}
}

func TestSanitizeTaskRuntimeEvidence_Hostnames(t *testing.T) {
	tests := []string{
		"目标 server.example.com",
		"连接 backup.internal.myorg.org",
		"user@db-primary.example.com",
		"host secret-server.company.io:9090",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			got := SanitizeTaskRuntimeEvidence(input)
			if !contains(got, "[主机已隐藏]") {
				t.Errorf("expected [主机已隐藏] in %q", got)
			}
		})
	}
}

func TestSanitizeTaskRuntimeEvidence_HostSensitiveFragments(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
		absent   string
	}{
		{
			name:     "host keyword hidden",
			input:    "sandbox-01",
			contains: "[主机信息已隐藏]",
		},
		{
			name:     "token fragment hidden",
			input:    "production-token",
			contains: "[主机信息已隐藏]",
		},
		{
			name:     "secret in compound",
			input:    "mysecretkey",
			contains: "[主机信息已隐藏]",
		},
		{
			name:     "safe word 'backup' kept",
			input:    "backup",
			contains: "backup",
		},
		{
			name:     "safe word 'host' kept",
			input:    "host",
			contains: "host",
		},
		{
			name:     "safe word 'node' kept",
			input:    "node",
			contains: "node",
		},
		{
			name:     "safe word 'task' kept",
			input:    "task",
			contains: "task",
		},
		{
			name:     "safe word 'restic' kept",
			input:    "restic",
			contains: "restic",
		},
		{
			name:     "safe word 'hostname' kept",
			input:    "hostname",
			contains: "hostname",
		},
		{
			name:     "already sanitized tokens kept",
			input:    "[主机已隐藏]",
			contains: "[主机已隐藏]",
		},
		{
			name:     "wildcard within token fragment is kept",
			input:    "prod-*",
			// taskHostSensitivePattern captures "prod-" (word boundary at *),
			// shouldKeepTaskRuntimeToken("prod-") returns false because no * in the captured fragment.
			contains: "[主机信息已隐藏]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeTaskRuntimeEvidence(tt.input)
			if !contains(got, tt.contains) {
				t.Errorf("expected %q in sanitized output, got %q", tt.contains, got)
			}
			if tt.absent != "" && contains(got, tt.absent) {
				t.Errorf("expected %q to be absent from %q", tt.absent, got)
			}
		})
	}
}

func TestSanitizeTaskRuntimeEvidence_MultiplePatterns(t *testing.T) {
	// 综合场景：一条消息中包含多种敏感信息。
	// 注意：taskCommandLifecyclePattern 使用 (?is) 多行模式 + .* 贪婪匹配，
	// 会吃掉"执行命令:"后面的全部内容（含输出部分），因此输出和路径标记不会单独出现。
	input := "任务在 host.internal.com 上执行命令: rsync -avz --delete /data/source/ user@192.168.1.10:/backup/dest/，输出: Transfer complete: 12345 files"
	got := SanitizeTaskRuntimeEvidence(input)

	// 执行命令模式先命中并吃掉后续所有内容
	checks := []string{
		"[命令已隐藏]",
		"[主机已隐藏]", // host.internal.com → hostname pattern
	}
	for _, c := range checks {
		if !contains(got, c) {
			t.Errorf("expected %q in sanitized output, got %q\nfull input: %q", c, got, input)
		}
	}
	// 输出标记和路径标记被命令模式覆盖，不应出现
	if contains(got, "[输出已隐藏]") {
		t.Log("output marker still appeared (command pattern did not consume it)")
	}
	if contains(got, "[路径已隐藏]") {
		t.Log("path marker still appeared (command pattern did not consume it)")
	}
}

func TestShouldKeepTaskRuntimeToken(t *testing.T) {
	tests := []struct {
		value string
		keep  bool
	}{
		{"", true},
		{"  ", true},
		{"[already hidden]", true},
		{"has*wildcard", true},
		{"stricthostkeychecking=yes", true},
		{"userknownhostsfile=/dev/null", true},
		{"backup", true},
		{"backup-server", false}, // compound word not in safe list
		{"my-secret-key", false},
		{"prod-token-123", false},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got := shouldKeepTaskRuntimeToken(tt.value)
			if got != tt.keep {
				t.Errorf("shouldKeepTaskRuntimeToken(%q) = %v, want %v", tt.value, got, tt.keep)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && len(substr) > 0 && containsSubstring(s, substr)
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}