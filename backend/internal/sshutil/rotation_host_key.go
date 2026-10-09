package sshutil

import (
	"errors"
	"net"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ResolveSSHRotationHostKeyCallback returns a read-only, strict known_hosts
// verifier for SSH key rotation prevalidation. Unlike the normal callback, it
// never consults auto-accept or strict-mode settings and never creates or
// writes the known_hosts file.
func ResolveSSHRotationHostKeyCallback() (ssh.HostKeyCallback, error) {
	knownHostsPath, err := knownHostsPathFromEnv()
	if err != nil {
		return nil, errors.New("加载 known_hosts 失败")
	}
	callback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, errors.New("加载 known_hosts 失败")
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if callbackErr := callback(hostname, remote, key); callbackErr != nil {
			var keyErr *knownhosts.KeyError
			if errors.As(callbackErr, &keyErr) {
				kind := HostKeyUnknown
				if len(keyErr.Want) > 0 {
					kind = HostKeyMismatch
				}
				return &HostKeyError{Kind: kind, Key: key}
			}
			return errors.New("knownhosts: host key verification failed")
		}
		return nil
	}, nil
}
