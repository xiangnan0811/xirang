package sshutil

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"xirang/backend/internal/util"

	"golang.org/x/crypto/ssh"
)

var (
	ErrHostKeyCheckingDisabled   = errors.New("SSH 主机密钥校验已关闭，无需信任指纹")
	ErrHostKeyFingerprintChanged = errors.New("服务器当前主机指纹与确认的指纹不一致，请重新测试连接后再确认")
	ErrHostKeyUnreachable        = errors.New("无法连接节点读取主机密钥，请检查主机地址与端口")

	// errHostKeyCaptured aborts the probe handshake right after the server
	// presents its host key, before any user authentication is attempted.
	errHostKeyCaptured = errors.New("host key captured")
)

const (
	hostKeyProbeTimeout = 5 * time.Second
	hostKeyProbeUser    = "xirang-host-key-probe"
)

// HostKeyTrustResult describes the host key that is now trusted for an address.
type HostKeyTrustResult struct {
	Algorithm      string
	Fingerprint    string
	AlreadyTrusted bool
}

// TrustNewHostKey dials address, captures the presented host key before user
// authentication, and appends it to known_hosts only if its SHA256 fingerprint
// equals expectedFingerprint and the host has no conflicting entry.
func TrustNewHostKey(ctx context.Context, address, expectedFingerprint string) (HostKeyTrustResult, error) {
	strictHostCheck, err := util.ReadBoolEnv("SSH_STRICT_HOST_KEY_CHECKING", true)
	if err != nil {
		return HostKeyTrustResult{}, err
	}
	if !strictHostCheck {
		return HostKeyTrustResult{}, ErrHostKeyCheckingDisabled
	}

	knownHostsPath, err := resolveKnownHostsPath()
	if err != nil {
		return HostKeyTrustResult{}, err
	}

	key, remote, err := captureHostKey(ctx, address)
	if err != nil {
		return HostKeyTrustResult{}, err
	}
	result := HostKeyTrustResult{Algorithm: key.Type(), Fingerprint: ssh.FingerprintSHA256(key)}

	// The confirmation is bound to the key the operator saw: a different current
	// key is rejected even when that key happens to be trusted already.
	if strings.TrimSpace(expectedFingerprint) != result.Fingerprint {
		return result, ErrHostKeyFingerprintChanged
	}
	// known_hosts is re-read under the write lock after the probe, so a record
	// added concurrently (another trust or auto-accept) is honored, never duplicated
	// with a conflicting key.
	alreadyTrusted, err := recordNewKnownHost(knownHostsPath, address, remote, key)
	result.AlreadyTrusted = alreadyTrusted
	return result, err
}

// captureHostKey performs the SSH key exchange only far enough to receive the
// server host key. The handshake is aborted inside the host key callback, so no
// credentials are ever sent to the (not yet trusted) server. Key types already
// recorded for the host are negotiated first, so a multi-key server presents
// its trusted key instead of Go's default choice.
func captureHostKey(ctx context.Context, address string) (ssh.PublicKey, net.Addr, error) {
	dialer := net.Dialer{Timeout: hostKeyProbeTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, nil, ErrHostKeyUnreachable
	}
	defer conn.Close() //nolint:errcheck

	var capturedKey ssh.PublicKey
	var capturedRemote net.Addr
	config := &ssh.ClientConfig{
		User: hostKeyProbeUser,
		HostKeyCallback: func(_ string, remote net.Addr, key ssh.PublicKey) error {
			capturedKey = key
			capturedRemote = remote
			return errHostKeyCaptured
		},
		HostKeyAlgorithms: HostKeyAlgorithmsForAddress(address),
		Timeout:           hostKeyProbeTimeout,
	}
	_, _, _, err = newClientConnWithContext(ctx, conn, address, config)
	if !errors.Is(err, errHostKeyCaptured) || capturedKey == nil {
		return nil, nil, ErrHostKeyUnreachable
	}
	return capturedKey, capturedRemote, nil
}
