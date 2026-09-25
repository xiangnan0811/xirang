package sshutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xirang/backend/internal/util"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const openSSHHostKeyProbeTimeout = 30 * time.Second

// OpenSSHKnownHostsWriteGuard lists the options every external OpenSSH
// transport must carry so that ssh itself never writes known_hosts: strict
// checking refuses unknown keys and UpdateHostKeys=no stops post-authentication
// key rotation (hostkeys-00@openssh.com) that would otherwise append keys even
// under StrictHostKeyChecking=yes. First registration happens only through
// RegisterNewHostKeyWithOpenSSH, under the known_hosts write lock.
func OpenSSHKnownHostsWriteGuard(knownHostsPath string) []string {
	return []string{
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UpdateHostKeys=no",
		"-o", "UserKnownHostsFile=" + knownHostsPath,
	}
}

// RegisterNewHostKeyWithOpenSSH performs first registration for consumers that
// run external OpenSSH, used when ssh.auto_accept_new_hosts is enabled.
//
// sshBase is the transport's ssh command prefix without host-key options (for
// example ["ssh", "-p", "2222"] or with "-F", "/dev/null"). The probe runs that
// same client and configuration, so it follows the identical route (ProxyJump,
// ProxyCommand, HostName, HostKeyAlias, @cert-authority, recorded key type
// preference), against a private copy of known_hosts with accept-new.
// PreferredAuthentications=none ends the session before any key, password or
// agent credential is offered to the target. Entries OpenSSH appended to the
// copy are then merged into the real file under the write lock, re-checked
// against concurrent writers; a conflicting record yields a mismatch
// HostKeyError. When OpenSSH records nothing (unreachable, already known, or a
// changed key it refused) this returns nil and the strict transport reports the
// outcome itself. Strict checking disabled makes this a no-op.
func RegisterNewHostKeyWithOpenSSH(ctx context.Context, sshBase []string, user, host string) error {
	strictHostCheck, err := util.ReadBoolEnv("SSH_STRICT_HOST_KEY_CHECKING", true)
	if err != nil || !strictHostCheck {
		return err
	}
	if len(sshBase) == 0 {
		return errors.New("SSH 主机密钥登记缺少 ssh 命令")
	}
	knownHostsPath, err := resolveKnownHostsPath()
	if err != nil {
		return err
	}
	original, err := os.ReadFile(knownHostsPath)
	if err != nil {
		return fmt.Errorf("读取 known_hosts 失败")
	}
	probeDir, err := os.MkdirTemp("", "xirang-known-hosts-probe-*")
	if err != nil {
		return fmt.Errorf("准备主机密钥登记目录失败")
	}
	defer os.RemoveAll(probeDir) //nolint:errcheck
	probeKnownHosts := filepath.Join(probeDir, "known_hosts")
	if err := os.WriteFile(probeKnownHosts, original, 0o600); err != nil {
		return fmt.Errorf("准备主机密钥登记文件失败")
	}

	probeCtx, cancel := context.WithTimeout(ctx, openSSHHostKeyProbeTimeout)
	defer cancel()
	args := append([]string{}, sshBase[1:]...)
	args = append(args,
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile="+probeKnownHosts,
		"-o", "UpdateHostKeys=no",
		"-o", "HashKnownHosts=no",
		"-o", "BatchMode=yes",
		"-o", "PreferredAuthentications=none",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "ClearAllForwardings=yes",
		"-o", "ForwardAgent=no",
		"-o", "PermitLocalCommand=no",
		"-o", "ConnectTimeout=10",
		"-o", "LogLevel=ERROR",
		"-l", user,
		"--", host, "true",
	)
	// The exit status is irrelevant: authentication is expected to fail. Only
	// the entries OpenSSH appended after verifying the key are used.
	runOpenSSHProbe(probeCtx, sshBase[0], args)

	probed, err := os.ReadFile(probeKnownHosts)
	if err != nil || !bytes.HasPrefix(probed, original) {
		return nil
	}
	return mergeDiscoveredKnownHosts(knownHostsPath, probed[len(original):])
}

// mergeDiscoveredKnownHosts appends entries OpenSSH recorded in a probe copy to
// path under the write lock, after re-reading path: hosts already holding the
// key are skipped and a host holding a different key is a mismatch.
func mergeDiscoveredKnownHosts(path string, discovered []byte) error {
	knownHostsWriteMu.Lock()
	defer knownHostsWriteMu.Unlock()

	for rest := discovered; ; {
		marker, hosts, key, _, next, err := ssh.ParseKnownHosts(rest)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("解析 OpenSSH 登记的主机密钥失败")
		}
		rest = next
		if marker != "" {
			continue
		}
		for _, host := range hosts {
			address, ok := knownHostsEntryAddress(host)
			if !ok {
				continue
			}
			verify, err := knownhosts.New(path)
			if err != nil {
				return fmt.Errorf("加载 known_hosts 失败")
			}
			verifyErr := verify(address, &net.TCPAddr{IP: net.IPv4zero}, key)
			if verifyErr == nil {
				continue
			}
			var keyErr *knownhosts.KeyError
			if !errors.As(verifyErr, &keyErr) {
				return verifyErr
			}
			if len(keyErr.Want) > 0 {
				return &HostKeyError{Kind: HostKeyMismatch, Key: key}
			}
			if err := appendKnownHostLocked(path, address, key); err != nil {
				return fmt.Errorf("写入 known_hosts 失败")
			}
		}
	}
}

// knownHostsEntryAddress converts a plain known_hosts host pattern ("host",
// "[host]:port", bare IPv6) to the host:port form knownhosts callbacks and
// knownhosts.Normalize expect. Hashed and wildcard patterns are not addresses.
func knownHostsEntryAddress(pattern string) (string, bool) {
	if pattern == "" || strings.HasPrefix(pattern, "|") || strings.ContainsAny(pattern, "*?!") {
		return "", false
	}
	if strings.HasPrefix(pattern, "[") {
		return pattern, true
	}
	return net.JoinHostPort(pattern, "22"), true
}
