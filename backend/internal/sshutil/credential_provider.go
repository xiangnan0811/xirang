package sshutil

import (
	"fmt"
	"strings"

	"xirang/backend/internal/model"

	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

const CredentialProviderLocal = "local"

type CredentialProvider interface {
	ResolveKeyContentForPurpose(node model.Node, db *gorm.DB, purpose string) (string, string, ResolvedCredential, error)
	BuildSSHAuthWithKeyForPurpose(node model.Node, db *gorm.DB, purpose string) ([]ssh.AuthMethod, string, ResolvedCredential, error)
}

type LocalCredentialProvider struct{}

func DefaultCredentialProvider() CredentialProvider {
	return LocalCredentialProvider{}
}

func (LocalCredentialProvider) ResolveKeyContentForPurpose(node model.Node, db *gorm.DB, purpose string) (string, string, ResolvedCredential, error) {
	if node.SSHKey != nil {
		if key := strings.TrimSpace(node.SSHKey.PrivateKey); key != "" {
			credential := credentialFromSSHKey(node.SSHKeyID, node.SSHKey.ID)
			if err := ValidateSSHKeyScope(*node.SSHKey, node, purpose); err != nil {
				return "", credential.Source, credential, err
			}
			return key, credential.Source, credential, nil
		}
	}

	if node.SSHKeyID != nil {
		keyID := *node.SSHKeyID
		credential := credentialFromSSHKey(&keyID, keyID)
		if db == nil {
			return "", credential.Source, credential, fmt.Errorf("节点绑定的密钥不存在，请重新选择")
		}
		var key model.SSHKey
		if err := db.First(&key, keyID).Error; err != nil {
			return "", credential.Source, credential, fmt.Errorf("节点绑定的密钥不存在，请重新选择")
		}
		if err := ValidateSSHKeyScope(key, node, purpose); err != nil {
			return "", credential.Source, credential, err
		}
		if content := strings.TrimSpace(key.PrivateKey); content != "" {
			return content, credential.Source, credential, nil
		}
		return "", credential.Source, credential, fmt.Errorf("节点绑定的密钥内容为空，请重新配置")
	}

	if content := strings.TrimSpace(node.PrivateKey); content != "" {
		return content, "node.private_key", ResolvedCredential{Kind: "node_private_key", Source: "node.private_key", Provider: CredentialProviderLocal}, nil
	}
	return "", "", ResolvedCredential{}, nil
}

func (provider LocalCredentialProvider) BuildSSHAuthWithKeyForPurpose(node model.Node, db *gorm.DB, purpose string) ([]ssh.AuthMethod, string, ResolvedCredential, error) {
	switch node.AuthType {
	case "password":
		credential := ResolvedCredential{Kind: "password", Source: "node.password", Provider: CredentialProviderLocal}
		if node.Password == "" {
			return nil, "", credential, fmt.Errorf("密码认证模式下请填写密码")
		}
		return []ssh.AuthMethod{ssh.Password(node.Password)}, "", credential, nil
	case "key":
		keyContent, keySource, credential, resolveErr := provider.ResolveKeyContentForPurpose(node, db, purpose)
		if resolveErr != nil {
			return nil, "", credential, resolveErr
		}
		if keyContent == "" {
			return nil, "", credential, fmt.Errorf("密钥认证模式下请选择已有密钥或填写私钥内容")
		}
		preparedKey, _, err := ValidateAndPreparePrivateKey(keyContent, SSHKeyTypeAuto)
		if err != nil {
			if strings.TrimSpace(keySource) == "" {
				keySource = "unknown"
			}
			return nil, "", credential, fmt.Errorf("私钥校验失败(来源: %s)，请检查密钥内容是否正确", keySource)
		}
		signer, err := ssh.ParsePrivateKey([]byte(preparedKey))
		if err != nil {
			return nil, "", credential, fmt.Errorf("解析私钥失败")
		}
		markSSHKeyLastUsed(db, credential)
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, preparedKey, credential, nil
	default:
		return nil, "", ResolvedCredential{}, fmt.Errorf("不支持的认证方式")
	}
}
