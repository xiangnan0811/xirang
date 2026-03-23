package executor

import (
	"testing"

	"xirang/backend/internal/model"
)

func TestNeedsSudo(t *testing.T) {
	tests := []struct {
		name     string
		node     model.Node
		expected bool
	}{
		{
			name:     "root user with UseSudo=true should return false",
			node:     model.Node{Username: "root", UseSudo: true},
			expected: false,
		},
		{
			name:     "empty username with UseSudo=true should return false",
			node:     model.Node{Username: "", UseSudo: true},
			expected: false,
		},
		{
			name:     "deploy user with UseSudo=false should return false",
			node:     model.Node{Username: "deploy", UseSudo: false},
			expected: false,
		},
		{
			name:     "deploy user with UseSudo=true should return true",
			node:     model.Node{Username: "deploy", UseSudo: true},
			expected: true,
		},
		{
			name:     "whitespace-only username with UseSudo=true should return false",
			node:     model.Node{Username: "  ", UseSudo: true},
			expected: false,
		},
		{
			name:     "root with spaces with UseSudo=true should return false",
			node:     model.Node{Username: " root ", UseSudo: true},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NeedsSudo(tt.node)
			if got != tt.expected {
				t.Errorf("NeedsSudo() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestWrapWithSudo(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		expected string
	}{
		{
			name:     "simple command",
			command:  "rsync -avz /src /dst",
			expected: "sudo rsync -avz /src /dst",
		},
		{
			name:     "empty command",
			command:  "",
			expected: "sudo ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WrapWithSudo(tt.command)
			if got != tt.expected {
				t.Errorf("WrapWithSudo() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestWrapWithSudoShell(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		expected string
	}{
		{
			name:     "simple command",
			command:  "echo hi",
			expected: "sudo sh -c 'echo hi'",
		},
		{
			name:     "command with pipe",
			command:  "echo hi | wc -l",
			expected: "sudo sh -c 'echo hi | wc -l'",
		},
		{
			name:     "command with single quotes",
			command:  "echo 'hello world'",
			expected: "sudo sh -c 'echo '\\''hello world'\\'''",
		},
		{
			name:     "command with && operator",
			command:  "mysqldump db > /tmp/dump.sql && gzip /tmp/dump.sql",
			expected: "sudo sh -c 'mysqldump db > /tmp/dump.sql && gzip /tmp/dump.sql'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WrapWithSudoShell(tt.command)
			if got != tt.expected {
				t.Errorf("WrapWithSudoShell() = %q, want %q", got, tt.expected)
			}
		})
	}
}
