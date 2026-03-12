package sshutil

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/model"

	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

// ProbeResult holds the result of a node probe.
type ProbeResult struct {
	Latency   int // ms
	DiskUsed  int // GB
	DiskTotal int // GB
}

// ProbeNode performs SSH connection test and disk probe on a node.
func ProbeNode(node model.Node, db *gorm.DB) (ProbeResult, error) {
	authMethods, err := BuildSSHAuth(node, db)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("构建 SSH 认证失败: %w", err)
	}

	hostKeyCallback, err := ResolveSSHHostKeyCallback()
	if err != nil {
		return ProbeResult{}, fmt.Errorf("解析主机密钥回调失败: %w", err)
	}

	address := fmt.Sprintf("%s:%d", node.Host, node.Port)
	start := time.Now()
	client, err := ssh.Dial("tcp", address, &ssh.ClientConfig{
		User:            node.Username,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         5 * time.Second,
	})
	if err != nil {
		return ProbeResult{}, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()

	latency := int(time.Since(start).Milliseconds())
	if latency <= 0 {
		latency = 1
	}

	result := ProbeResult{Latency: latency}

	// Probe disk usage
	if session, err := client.NewSession(); err == nil {
		output, runErr := session.Output("df -BG / | awk 'NR==2 {print $2\" \"$3}'")
		_ = session.Close()
		if runErr == nil {
			if used, total, ok := ParseDiskProbe(string(output)); ok {
				result.DiskUsed = used
				result.DiskTotal = total
			}
		}
	}

	return result, nil
}

// ParseDiskProbe parses df -BG output like "100G 42G" where the first field is
// total and the second field is used. Returns (used, total, ok).
func ParseDiskProbe(output string) (int, int, bool) {
	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) < 2 {
		return 0, 0, false
	}

	parseGB := func(raw string) (int, bool) {
		trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(raw, "Gi"), "G"))
		value, err := strconv.Atoi(trimmed)
		if err != nil {
			return 0, false
		}
		return value, true
	}
	total, okTotal := parseGB(fields[0])
	used, okUsed := parseGB(fields[1])
	if !okTotal || !okUsed || total <= 0 || used < 0 || used > total {
		return 0, 0, false
	}
	return used, total, true
}
