package executor

import (
	"os/exec"
	"strings"
	"testing"
)

// TestShellEscape_NeutralizesAdversarialInputs 用真实 shell 验证 ShellEscape
// 对各种攻击载荷的无害化效果。每条用例：
//   - 把待转义字符串 X 经过 ShellEscape 得到 Y
//   - 在本地 shell 执行 `printf '%s' Y`，期望输出 == X
//   - 同时确保不会执行 X 中可能内嵌的命令（输出不会出现 INJECTED 标记）
func TestShellEscape_NeutralizesAdversarialInputs(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"single quote", `it's a test`},
		{"double quote", `say "hi"`},
		{"backtick command sub", "x`echo INJECTED`y"},
		{"dollar paren command sub", "x$(echo INJECTED)y"},
		{"semicolon", "/path/;rm -rf INJECTED"},
		{"pipe", "/path/|cat /etc/passwd"},
		{"and", "/path/&&echo INJECTED"},
		{"newline", "line1\nINJECTED"},
		{"carriage return", "line1\rINJECTED"},
		{"backslash", `back\slash`},
		{"glob star", "/path/*"},
		{"variable", "$HOME/x"},
		// 注：NUL 不能通过 execve(2) 传递，无法到达 shell，因此 ShellEscape
		// 在 NUL 上的行为 ill-defined。validatePathChars 在 API 层强制拒绝
		// 含 NUL 的输入，覆盖见 helpers_test.go::TestValidatePathChars_RejectsInjectionPatterns。
		{"tab", "tab\there"},
		{"unicode", "中文 + emoji 🦀"},
		{"empty", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			escaped := ShellEscape(tc.input)
			// 用 sh -c "printf '%s' <escaped>" 让 shell 真正解析转义后的字符串
			out, err := exec.Command("sh", "-c", "printf '%s' "+escaped).Output()
			if err != nil {
				t.Fatalf("shell 执行失败: %v (escaped=%q)", err, escaped)
			}
			got := string(out)
			// 还原性 == 安全性：如果发生命令注入（如 `echo INJECTED` 被执行），
			// shell 输出会与原输入不同；只要 printf '%s' 还原回原字符串，就证明
			// 整个 escape 后的字符串被 shell 视为单个 literal 字面量而非可执行片段。
			if got != tc.input {
				t.Fatalf("escape 后还原不一致（可能有命令注入）\n输入   : %q\n转义后 : %s\n实际   : %q",
					tc.input, escaped, got)
			}
		})
	}
}

// TestShellEscape_ProducesQuotedForm 断言转义结果总是单引号包裹
// （避免回归到无引号形式而失去基本保护）。
func TestShellEscape_ProducesQuotedForm(t *testing.T) {
	cases := []string{
		"plain",
		"with space",
		`with'quote`,
		"",
	}
	for _, in := range cases {
		out := ShellEscape(in)
		if !strings.HasPrefix(out, "'") || !strings.HasSuffix(out, "'") {
			t.Fatalf("ShellEscape 输出未被单引号包裹: %q -> %q", in, out)
		}
	}
}
