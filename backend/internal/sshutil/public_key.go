package sshutil

import (
	"strings"

	"golang.org/x/crypto/ssh"
)

// DerivePublicKey 从 PEM 格式私钥派生 OpenSSH 格式公钥字符串。
// 空私钥返回空字符串。
func DerivePublicKey(privateKeyPEM string) (string, error) {
	trimmed := strings.TrimSpace(privateKeyPEM)
	if trimmed == "" {
		return "", nil
	}

	signer, err := ssh.ParsePrivateKey([]byte(trimmed))
	if err != nil {
		return "", err
	}

	pubKey := signer.PublicKey()
	authorizedKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubKey)))
	return authorizedKey, nil
}
