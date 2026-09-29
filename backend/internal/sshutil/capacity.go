package sshutil

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/model"

	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

// CapacityResult contains one on-demand SSH capacity reading. The result is
// deliberately detached from model.Node: callers must not persist the
// capacity values as node health or probe state.
type CapacityResult struct {
	Latency       int // ms
	DiskUsed      int // GB
	DiskTotal     int // GB
	DiskAvailable bool
}

// CheckNodeCapacityForPurpose establishes an SSH connection for purpose and
// reads the root filesystem capacity once. Authentication, host-key, and SSH
// connection failures are returned as errors. A remote df/session/parse
// failure leaves DiskAvailable false while preserving the successful SSH
// connection result, matching the historical probe error boundary.
func CheckNodeCapacityForPurpose(node model.Node, db *gorm.DB, purpose string) (CapacityResult, ResolvedCredential, error) {
	authMethods, credential, err := BuildSSHAuthForPurpose(node, db, purpose)
	if err != nil {
		return CapacityResult{}, credential, fmt.Errorf("构建 SSH 认证失败: %w", err)
	}

	hostKeyCallback, err := ResolveSSHHostKeyCallback()
	if err != nil {
		return CapacityResult{}, credential, fmt.Errorf("解析主机密钥回调失败: %w", err)
	}

	address := net.JoinHostPort(node.Host, strconv.Itoa(node.Port))
	start := time.Now()
	client, err := ssh.Dial("tcp", address, &ssh.ClientConfig{
		User:              node.Username,
		Auth:              authMethods,
		HostKeyCallback:   hostKeyCallback,
		HostKeyAlgorithms: HostKeyAlgorithmsForAddress(address),
		Timeout:           5 * time.Second,
	})
	if err != nil {
		return CapacityResult{}, credential, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck

	latency := int(time.Since(start).Milliseconds())
	if latency <= 0 {
		latency = 1
	}

	result := CapacityResult{Latency: latency}
	session, err := client.NewSession()
	if err != nil {
		return result, credential, nil
	}
	output, runErr := session.Output("df -BG / | awk 'NR==2 {print $2\" \"$3}'")
	_ = session.Close()
	if runErr != nil {
		return result, credential, nil
	}
	used, total, ok := ParseDiskProbe(string(output))
	if !ok {
		return result, credential, nil
	}
	result.DiskUsed = used
	result.DiskTotal = total
	result.DiskAvailable = true
	return result, credential, nil
}

// ParseDiskProbe parses df -BG output like "100G 42G" where the first field is
// total and the second is used. Returns (used, total, ok).
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
