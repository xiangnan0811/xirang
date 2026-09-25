package sshutil

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// AutoAcceptNewHostsSettingKey is the dynamic setting controlling whether
// unknown SSH host keys are appended to known_hosts on first connection.
const AutoAcceptNewHostsSettingKey = "ssh.auto_accept_new_hosts"

var autoAcceptNewHostsSource atomic.Pointer[func() string]

// SetAutoAcceptNewHostsSource installs the effective-value reader
// (settings.Service.GetEffective) at startup; nil restores env fallback.
func SetAutoAcceptNewHostsSource(source func() string) {
	if source == nil {
		autoAcceptNewHostsSource.Store(nil)
		return
	}
	autoAcceptNewHostsSource.Store(&source)
}

// AutoAcceptNewHosts parses the effective ssh.auto_accept_new_hosts value
// (DB > SSH_AUTO_ACCEPT_NEW_HOSTS > false). Empty means false.
func AutoAcceptNewHosts() (bool, error) {
	var raw string
	if source := autoAcceptNewHostsSource.Load(); source != nil {
		raw = (*source)()
	} else {
		raw = os.Getenv("SSH_AUTO_ACCEPT_NEW_HOSTS")
	}
	return ParseAutoAcceptNewHosts(raw)
}

// ParseAutoAcceptNewHosts parses a raw ssh.auto_accept_new_hosts value.
func ParseAutoAcceptNewHosts(raw string) (bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(trimmed)
	if err != nil {
		return false, errors.New("SSH 自动接受未知主机密钥配置值无效")
	}
	return value, nil
}

// HostKeyErrorKind classifies a rejected SSH host key.
type HostKeyErrorKind string

const (
	HostKeyUnknown  HostKeyErrorKind = "unknown"
	HostKeyMismatch HostKeyErrorKind = "mismatch"
)

// HostKeyError reports a host key rejected by known_hosts verification. Its
// message never contains the host name or address.
type HostKeyError struct {
	Kind HostKeyErrorKind
	Key  ssh.PublicKey
}

func (e *HostKeyError) Error() string {
	if e.Kind == HostKeyMismatch {
		return "主机密钥与 known_hosts 记录不一致，已拒绝连接：请先核实服务器是否重装或存在中间人攻击，确认可信后由管理员更新 known_hosts"
	}
	return "未知主机密钥被拒绝：请在节点页「测试连接」中核对并信任主机指纹，或由管理员在 系统设置 → 安全 中开启“自动接受新主机密钥”"
}

// Algorithm returns the presented host key algorithm.
func (e *HostKeyError) Algorithm() string {
	if e.Key == nil {
		return ""
	}
	return e.Key.Type()
}

// Fingerprint returns the presented host key SHA256 fingerprint.
func (e *HostKeyError) Fingerprint() string {
	if e.Key == nil {
		return ""
	}
	return ssh.FingerprintSHA256(e.Key)
}

// knownHostsPathFromEnv resolves SSH_KNOWN_HOSTS_PATH without touching disk.
func knownHostsPathFromEnv() (string, error) {
	rawPath := strings.TrimSpace(util.GetEnvOrDefault("SSH_KNOWN_HOSTS_PATH", "~/.ssh/known_hosts"))
	knownHostsPath, err := util.ExpandHomePath(rawPath)
	if err != nil {
		return "", fmt.Errorf("解析 SSH_KNOWN_HOSTS_PATH 失败")
	}
	if strings.TrimSpace(knownHostsPath) == "" {
		return "", fmt.Errorf("SSH_KNOWN_HOSTS_PATH 不能为空")
	}
	return knownHostsPath, nil
}

// resolveKnownHostsPath resolves SSH_KNOWN_HOSTS_PATH and ensures the file exists.
func resolveKnownHostsPath() (string, error) {
	knownHostsPath, err := knownHostsPathFromEnv()
	if err != nil {
		return "", err
	}
	if err := ensureKnownHostsFile(knownHostsPath); err != nil {
		return "", fmt.Errorf("准备 known_hosts 失败")
	}
	return knownHostsPath, nil
}

// knownHostsLookupKey is an all-zero Ed25519 key that never matches a real
// record; checking it yields the KeyError listing every key recorded for a host.
var knownHostsLookupKey = func() ssh.PublicKey {
	key, err := ssh.NewPublicKey(ed25519.PublicKey(make([]byte, ed25519.PublicKeySize)))
	if err != nil {
		panic(err)
	}
	return key
}()

// HostKeyAlgorithmsForAddress returns a ClientConfig.HostKeyAlgorithms list
// that prefers the key types already recorded in known_hosts for address and
// then offers every other algorithm, mirroring OpenSSH. Without it, a server
// offering several host keys negotiates Go's default (ECDSA first) and a host
// recorded with, e.g., its Ed25519 key would be reported as a mismatch. A
// matching @cert-authority record keeps certificate algorithms first (a CA's
// own key type says nothing about the server's raw key). It returns nil (Go
// defaults) when strict checking is off or the host has no record.
func HostKeyAlgorithmsForAddress(address string) []string {
	strictHostCheck, err := util.ReadBoolEnv("SSH_STRICT_HOST_KEY_CHECKING", true)
	if err != nil || !strictHostCheck {
		return nil
	}
	knownHostsPath, err := knownHostsPathFromEnv()
	if err != nil {
		return nil
	}
	content, err := os.ReadFile(knownHostsPath)
	if err != nil {
		return nil
	}
	verify, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil
	}
	var keyErr *knownhosts.KeyError
	if !errors.As(verify(address, &net.TCPAddr{IP: net.IPv4zero}, knownHostsLookupKey), &keyErr) || len(keyErr.Want) == 0 {
		return nil
	}
	certAuthorityLines := knownHostsCertAuthorityLines(content)
	allAlgorithms := slices.Concat(ssh.SupportedAlgorithms().HostKeys, ssh.InsecureAlgorithms().HostKeys)
	preferred := make([]string, 0, len(allAlgorithms))
	for _, known := range keyErr.Want {
		if certAuthorityLines[known.Line] {
			for _, algorithm := range allAlgorithms {
				if strings.Contains(algorithm, "-cert-") {
					preferred = append(preferred, algorithm)
				}
			}
			continue
		}
		if known.Key.Type() == ssh.KeyAlgoRSA {
			preferred = append(preferred, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
			continue
		}
		preferred = append(preferred, known.Key.Type())
	}
	algorithms := make([]string, 0, len(allAlgorithms))
	for _, algorithm := range slices.Concat(preferred, allAlgorithms) {
		if !slices.Contains(algorithms, algorithm) {
			algorithms = append(algorithms, algorithm)
		}
	}
	return algorithms
}

// knownHostsCertAuthorityLines returns the 1-based numbers of @cert-authority
// lines, numbered exactly as knownhosts.KnownKey.Line reports them.
func knownHostsCertAuthorityLines(content []byte) map[int]bool {
	lines := make(map[int]bool)
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		if fields := strings.Fields(scanner.Text()); len(fields) > 0 && fields[0] == "@cert-authority" {
			lines[lineNumber] = true
		}
	}
	return lines
}

// ResolveSSHHostKeyCallback returns the host key callback based on env config
// and the dynamic ssh.auto_accept_new_hosts setting.
func ResolveSSHHostKeyCallback() (ssh.HostKeyCallback, error) {
	strictHostCheck, err := util.ReadBoolEnv("SSH_STRICT_HOST_KEY_CHECKING", true)
	if err != nil {
		return nil, err
	}
	if !strictHostCheck {
		log.Printf("warn: SSH 主机密钥校验已禁用，建议在生产环境启用 SSH_STRICT_HOST_KEY_CHECKING=true")
		return ssh.InsecureIgnoreHostKey(), nil
	}

	knownHostsPath, err := resolveKnownHostsPath()
	if err != nil {
		return nil, err
	}

	callback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("加载 known_hosts 失败")
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if callbackErr := callback(hostname, remote, key); callbackErr != nil {
			var keyErr *knownhosts.KeyError
			if errors.As(callbackErr, &keyErr) {
				if len(keyErr.Want) > 0 {
					return &HostKeyError{Kind: HostKeyMismatch, Key: key}
				}
				autoAccept, autoAcceptErr := AutoAcceptNewHosts()
				if autoAcceptErr != nil || !autoAccept {
					return &HostKeyError{Kind: HostKeyUnknown, Key: key}
				}
				log.Printf("info: 自动接受未知主机密钥并写入 known_hosts；可在 系统设置 → 安全 中关闭")
				if _, recordErr := recordNewKnownHost(knownHostsPath, hostname, remote, key); recordErr != nil {
					var hostKeyErr *HostKeyError
					if errors.As(recordErr, &hostKeyErr) {
						return hostKeyErr
					}
					return fmt.Errorf("knownhosts: accept new host failed")
				}
				refreshedCallback, refreshErr := knownhosts.New(knownHostsPath)
				if refreshErr != nil {
					return fmt.Errorf("加载 known_hosts 失败")
				}
				callback = refreshedCallback
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
		User:              user,
		Auth:              auth,
		HostKeyCallback:   hostKey,
		HostKeyAlgorithms: HostKeyAlgorithmsForAddress(addr),
		Timeout:           5 * time.Second,
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}

	sshConn, chans, reqs, err := newClientConnWithContext(ctx, conn, addr, config)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("SSH 握手失败: %w", err)
	}

	return ssh.NewClient(sshConn, chans, reqs), nil
}

func newClientConnWithContext(ctx context.Context, conn net.Conn, addr string, config *ssh.ClientConfig) (ssh.Conn, <-chan ssh.NewChannel, <-chan *ssh.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else if config != nil && config.Timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(config.Timeout))
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	sshConn, channels, requests, err := ssh.NewClientConn(conn, addr, config)
	close(done)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, nil, ctx.Err()
		}
		return nil, nil, nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return sshConn, channels, requests, nil
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
	return appendKnownHostLocked(path, hostname, key)
}

// recordNewKnownHost re-reads known_hosts while holding the write lock and
// appends key only when the file has no entry for hostname yet, so concurrent
// writers can never record two different keys for the same host. It reports
// alreadyTrusted when the exact key is already recorded and a mismatch
// HostKeyError when a different key is.
func recordNewKnownHost(path, hostname string, remote net.Addr, key ssh.PublicKey) (alreadyTrusted bool, err error) {
	knownHostsWriteMu.Lock()
	defer knownHostsWriteMu.Unlock()

	if err := ensureKnownHostsFile(path); err != nil {
		return false, fmt.Errorf("准备 known_hosts 失败")
	}
	verify, err := knownhosts.New(path)
	if err != nil {
		return false, fmt.Errorf("加载 known_hosts 失败")
	}
	verifyErr := verify(hostname, remote, key)
	if verifyErr == nil {
		return true, nil
	}
	var keyErr *knownhosts.KeyError
	if !errors.As(verifyErr, &keyErr) {
		return false, verifyErr
	}
	if len(keyErr.Want) > 0 {
		return false, &HostKeyError{Kind: HostKeyMismatch, Key: key}
	}
	if err := appendKnownHostLocked(path, hostname, key); err != nil {
		return false, fmt.Errorf("写入 known_hosts 失败")
	}
	return false, nil
}

func appendKnownHostLocked(path, hostname string, key ssh.PublicKey) error {
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
