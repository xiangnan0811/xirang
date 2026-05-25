package sshutil

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/util"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"gorm.io/gorm"
)

var knownHostsWriteMu sync.Mutex

// ResolveKeyContent resolves the SSH private key content for a node.
// Returns (keyContent, keySource, error). Legacy callers should prefer
// ResolveKeyContentForPurpose so managed SSHKey metadata is enforced.
func ResolveKeyContent(node model.Node, db *gorm.DB) (string, string, error) {
	content, source, _, err := ResolveKeyContentForPurpose(node, db, "")
	return content, source, err
}

// ResolveKeyContentForPurpose resolves private key content and enforces managed
// SSHKey least-privilege metadata when the key comes from ssh_keys.
func ResolveKeyContentForPurpose(node model.Node, db *gorm.DB, purpose string) (string, string, ResolvedCredential, error) {
	return DefaultCredentialProvider().ResolveKeyContentForPurpose(node, db, purpose)
}

// BuildSSHAuth builds SSH authentication methods for a node.
// Returns (authMethods, error). For key auth, it validates and parses the private key.
func BuildSSHAuth(node model.Node, db *gorm.DB) ([]ssh.AuthMethod, error) {
	authMethods, _, err := BuildSSHAuthWithCredential(node, db, "")
	return authMethods, err
}

func BuildSSHAuthForPurpose(node model.Node, db *gorm.DB, purpose string) ([]ssh.AuthMethod, ResolvedCredential, error) {
	return BuildSSHAuthWithCredential(node, db, purpose)
}

// BuildSSHAuthWithKey builds SSH authentication methods and also returns the prepared key content.
// This is used by handlers that need the prepared key (e.g., for updating SSHKey.LastUsedAt).
func BuildSSHAuthWithKey(node model.Node, db *gorm.DB) ([]ssh.AuthMethod, string, error) {
	authMethods, preparedKey, _, err := BuildSSHAuthWithKeyForPurpose(node, db, "")
	return authMethods, preparedKey, err
}

func BuildSSHAuthWithKeyForPurpose(node model.Node, db *gorm.DB, purpose string) ([]ssh.AuthMethod, string, ResolvedCredential, error) {
	return DefaultCredentialProvider().BuildSSHAuthWithKeyForPurpose(node, db, purpose)
}

func BuildSSHAuthWithCredential(node model.Node, db *gorm.DB, purpose string) ([]ssh.AuthMethod, ResolvedCredential, error) {
	authMethods, _, credential, err := BuildSSHAuthWithKeyForPurpose(node, db, purpose)
	return authMethods, credential, err
}

func markSSHKeyLastUsed(db *gorm.DB, credential ResolvedCredential) {
	if db == nil || credential.KeyID == nil {
		return
	}
	now := time.Now().UTC()
	_ = db.Model(&model.SSHKey{}).Where("id = ?", *credential.KeyID).Update("last_used_at", now).Error
}

func credentialFromSSHKey(nodeSSHKeyID *uint, keyID uint) ResolvedCredential {
	resolvedID := keyID
	if nodeSSHKeyID != nil && *nodeSSHKeyID != 0 {
		resolvedID = *nodeSSHKeyID
	}
	return ResolvedCredential{Kind: "ssh_key", Source: fmt.Sprintf("ssh_key_id=%d", resolvedID), Provider: CredentialProviderLocal, KeyID: &resolvedID}
}

// ResolveSSHHostKeyCallback returns the host key callback based on env config.
func ResolveSSHHostKeyCallback() (ssh.HostKeyCallback, error) {
	strictHostCheck, err := util.ReadBoolEnv("SSH_STRICT_HOST_KEY_CHECKING", true)
	if err != nil {
		return nil, err
	}
	if !strictHostCheck {
		log.Printf("warn: SSH 主机密钥校验已禁用，建议在生产环境启用 SSH_STRICT_HOST_KEY_CHECKING=true")
		return ssh.InsecureIgnoreHostKey(), nil
	}

	rawPath := strings.TrimSpace(util.GetEnvOrDefault("SSH_KNOWN_HOSTS_PATH", "~/.ssh/known_hosts"))
	knownHostsPath, err := util.ExpandHomePath(rawPath)
	if err != nil {
		return nil, fmt.Errorf("解析 SSH_KNOWN_HOSTS_PATH 失败")
	}
	if strings.TrimSpace(knownHostsPath) == "" {
		return nil, fmt.Errorf("SSH_KNOWN_HOSTS_PATH 不能为空")
	}
	if err := ensureKnownHostsFile(knownHostsPath); err != nil {
		return nil, fmt.Errorf("准备 known_hosts 失败")
	}

	callback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("加载 known_hosts 失败")
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if callbackErr := callback(hostname, remote, key); callbackErr != nil {
			var keyErr *knownhosts.KeyError
			if errors.As(callbackErr, &keyErr) && len(keyErr.Want) == 0 {
				autoAccept, _ := util.ReadBoolEnv("SSH_AUTO_ACCEPT_NEW_HOSTS", true)
				if !autoAccept {
					return fmt.Errorf("未知主机密钥被拒绝，当前已禁用自动接受(SSH_AUTO_ACCEPT_NEW_HOSTS=false)")
				}
				log.Printf("info: 自动接受未知主机密钥并写入 known_hosts；如需禁用可设置 SSH_AUTO_ACCEPT_NEW_HOSTS=false")
				if appendErr := AppendKnownHost(knownHostsPath, hostname, key); appendErr != nil {
					return fmt.Errorf("knownhosts: accept new host failed")
				}
				refreshedCallback, refreshErr := knownhosts.New(knownHostsPath)
				if refreshErr != nil {
					return fmt.Errorf("加载 known_hosts 失败")
				}
				callback = refreshedCallback
				if verifyErr := callback(hostname, remote, key); verifyErr != nil {
					return fmt.Errorf("knownhosts: host key verification failed")
				}
				return nil
			}
			return fmt.Errorf("knownhosts: host key verification failed")
		}
		return nil
	}, nil
}

// DialSSH 建立 SSH 连接，支持 context 取消。
func DialSSH(ctx context.Context, addr, user string, auth []ssh.AuthMethod, hostKey ssh.HostKeyCallback) (*ssh.Client, error) {
	config := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: hostKey,
		Timeout:         5 * time.Second,
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close() //nolint:errcheck
		return nil, fmt.Errorf("SSH 握手失败: %w", err)
	}

	return ssh.NewClient(sshConn, chans, reqs), nil
}

func ensureKnownHostsFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	return file.Close()
}

func AppendKnownHost(path, hostname string, key ssh.PublicKey) error {
	knownHostsWriteMu.Lock()
	defer knownHostsWriteMu.Unlock()

	if err := ensureKnownHostsFile(path); err != nil {
		return err
	}
	entry := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if knownHostEntryExists(content, hostname, key) {
		return nil
	}
	prefix := ""
	if len(content) > 0 && content[len(content)-1] != '\n' {
		prefix = "\n"
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close() //nolint:errcheck
	_, err = file.WriteString(prefix + entry + "\n")
	return err
}

func knownHostEntryExists(content []byte, hostname string, key ssh.PublicKey) bool {
	normalizedHost := knownhosts.Normalize(hostname)
	keyFields := strings.Fields(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))))
	if len(keyFields) < 2 {
		return false
	}

	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		hosts := strings.Split(fields[0], ",")
		if !slices.Contains(hosts, normalizedHost) {
			continue
		}
		if fields[1] == keyFields[0] && fields[2] == keyFields[1] {
			return true
		}
	}
	return false
}
