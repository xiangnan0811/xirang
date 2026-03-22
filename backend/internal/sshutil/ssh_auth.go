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
// Returns (keyContent, keySource, error).
func ResolveKeyContent(node model.Node, db *gorm.DB) (string, string, error) {
	if node.SSHKey != nil {
		if key := strings.TrimSpace(node.SSHKey.PrivateKey); key != "" {
			if node.SSHKeyID != nil {
				return key, fmt.Sprintf("ssh_key_id=%d", *node.SSHKeyID), nil
			}
			return key, "ssh_key_ref", nil
		}
	}

	if node.SSHKeyID != nil {
		keyID := *node.SSHKeyID
		var key model.SSHKey
		if err := db.First(&key, keyID).Error; err != nil {
			return "", fmt.Sprintf("ssh_key_id=%d", keyID), fmt.Errorf("节点绑定的密钥不存在，请重新选择")
		}
		if content := strings.TrimSpace(key.PrivateKey); content != "" {
			return content, fmt.Sprintf("ssh_key_id=%d", keyID), nil
		}
		return "", fmt.Sprintf("ssh_key_id=%d", keyID), fmt.Errorf("节点绑定的密钥内容为空，请重新配置")
	}

	if content := strings.TrimSpace(node.PrivateKey); content != "" {
		return content, "node.private_key", nil
	}
	return "", "", nil
}

// BuildSSHAuth builds SSH authentication methods for a node.
// Returns (authMethods, error). For key auth, it validates and parses the private key.
func BuildSSHAuth(node model.Node, db *gorm.DB) ([]ssh.AuthMethod, error) {
	switch node.AuthType {
	case "password":
		if node.Password == "" {
			return nil, fmt.Errorf("密码认证模式下请填写密码")
		}
		return []ssh.AuthMethod{ssh.Password(node.Password)}, nil
	case "key":
		keyContent, keySource, resolveErr := ResolveKeyContent(node, db)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if keyContent == "" {
			return nil, fmt.Errorf("密钥认证模式下请选择已有密钥或填写私钥内容")
		}
		preparedKey, _, err := ValidateAndPreparePrivateKey(keyContent, SSHKeyTypeAuto)
		if err != nil {
			if strings.TrimSpace(keySource) == "" {
				keySource = "unknown"
			}
			return nil, fmt.Errorf("私钥校验失败(来源: %s)，请检查密钥内容是否正确", keySource)
		}
		signer, err := ssh.ParsePrivateKey([]byte(preparedKey))
		if err != nil {
			return nil, fmt.Errorf("解析私钥失败")
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default:
		return nil, fmt.Errorf("不支持的认证方式")
	}
}

// BuildSSHAuthWithKey builds SSH authentication methods and also returns the prepared key content.
// This is used by handlers that need the prepared key (e.g., for updating SSHKey.LastUsedAt).
func BuildSSHAuthWithKey(node model.Node, db *gorm.DB) ([]ssh.AuthMethod, string, error) {
	switch node.AuthType {
	case "password":
		if node.Password == "" {
			return nil, "", fmt.Errorf("密码认证模式下请填写密码")
		}
		return []ssh.AuthMethod{ssh.Password(node.Password)}, "", nil
	case "key":
		keyContent, keySource, resolveErr := ResolveKeyContent(node, db)
		if resolveErr != nil {
			return nil, "", resolveErr
		}
		if keyContent == "" {
			return nil, "", fmt.Errorf("密钥认证模式下请选择已有密钥或填写私钥内容")
		}
		preparedKey, _, err := ValidateAndPreparePrivateKey(keyContent, SSHKeyTypeAuto)
		if err != nil {
			if strings.TrimSpace(keySource) == "" {
				keySource = "unknown"
			}
			return nil, "", fmt.Errorf("私钥校验失败(来源: %s)，请检查密钥内容是否正确", keySource)
		}
		signer, err := ssh.ParsePrivateKey([]byte(preparedKey))
		if err != nil {
			return nil, "", fmt.Errorf("解析私钥失败")
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, preparedKey, nil
	default:
		return nil, "", fmt.Errorf("不支持的认证方式")
	}
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
		return nil, fmt.Errorf("解析 SSH_KNOWN_HOSTS_PATH 失败: path=%s, err=%v", rawPath, err)
	}
	if strings.TrimSpace(knownHostsPath) == "" {
		return nil, fmt.Errorf("SSH_KNOWN_HOSTS_PATH 不能为空")
	}
	if err := ensureKnownHostsFile(knownHostsPath); err != nil {
		return nil, fmt.Errorf("准备 known_hosts 失败: path=%s, err=%v", knownHostsPath, err)
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
					return fmt.Errorf("未知主机密钥被拒绝(host=%s)，如需自动接受请设置 SSH_AUTO_ACCEPT_NEW_HOSTS=true", hostname)
				}
				log.Printf("info: 自动接受未知主机密钥(host=%s)，可通过 SSH_AUTO_ACCEPT_NEW_HOSTS=false 禁用", hostname)
				if appendErr := AppendKnownHost(knownHostsPath, hostname, key); appendErr != nil {
					return fmt.Errorf("knownhosts: accept new host failed: %w", appendErr)
				}
				refreshedCallback, refreshErr := knownhosts.New(knownHostsPath)
				if refreshErr != nil {
					return fmt.Errorf("加载 known_hosts 失败")
				}
				callback = refreshedCallback
				return callback(hostname, remote, key)
			}
			return callbackErr
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
		conn.Close()
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
	defer file.Close()
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
