package sshutil

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLocalCredentialProviderManagedSSHKeyUsesEncryptedLocalStorage(t *testing.T) {
	db := newCredentialProviderTestDB(t)
	privateKey := buildRSAPrivateKeyForTest(t)
	nodeID := uint(7)
	sshKey := model.SSHKey{
		Name:            "managed-key",
		Username:        "root",
		KeyType:         SSHKeyTypeRSA,
		PrivateKey:      privateKey,
		Fingerprint:     "SHA256:managed-key",
		AllowedPurposes: PurposeTerminal,
		AllowedNodeIDs:  fmt.Sprintf("%d", nodeID),
		AllowedNodeTags: "prod",
	}
	if err := db.Create(&sshKey).Error; err != nil {
		t.Fatalf("创建测试 SSH Key 失败: %v", err)
	}
	assertStoredValueEncrypted(t, db, "ssh_keys", "private_key", sshKey.ID)

	startedAt := time.Now().UTC().Add(-time.Second)
	node := model.Node{ID: nodeID, AuthType: "key", Username: "root", SSHKeyID: &sshKey.ID, Tags: "prod"}
	auth, preparedKey, credential, err := BuildSSHAuthWithKeyForPurpose(node, db, PurposeTerminal)
	if err != nil {
		t.Fatalf("托管 SSH Key 应通过 local provider 构建认证: %v", err)
	}
	if len(auth) != 1 || strings.TrimSpace(preparedKey) == "" {
		t.Fatalf("托管 SSH Key 应返回认证方法和内存私钥")
	}
	assertLocalCredential(t, credential, "ssh_key", fmt.Sprintf("ssh_key_id=%d", sshKey.ID), &sshKey.ID)
	assertCredentialMetadataSafe(t, credential, privateKey)

	var refreshed model.SSHKey
	if err := db.First(&refreshed, sshKey.ID).Error; err != nil {
		t.Fatalf("重新读取 SSH Key 失败: %v", err)
	}
	if refreshed.LastUsedAt == nil || refreshed.LastUsedAt.Before(startedAt) {
		t.Fatalf("托管 SSH Key 成功使用后应更新 LastUsedAt")
	}
}

func TestLocalCredentialProviderInlinePrivateKeyUsesEncryptedNodeStorage(t *testing.T) {
	db := newCredentialProviderTestDB(t)
	privateKey := buildRSAPrivateKeyForTest(t)
	node := model.Node{
		Name:       "inline-key-node",
		Host:       "127.0.0.1",
		Port:       22,
		Username:   "root",
		AuthType:   "key",
		PrivateKey: privateKey,
		BackupDir:  "inline-key-backup",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建测试节点失败: %v", err)
	}
	assertStoredValueEncrypted(t, db, "nodes", "private_key", node.ID)

	var loaded model.Node
	if err := db.First(&loaded, node.ID).Error; err != nil {
		t.Fatalf("读取测试节点失败: %v", err)
	}
	auth, preparedKey, credential, err := BuildSSHAuthWithKeyForPurpose(loaded, db, PurposeTerminal)
	if err != nil {
		t.Fatalf("内联私钥应通过 local provider 构建认证: %v", err)
	}
	if len(auth) != 1 || strings.TrimSpace(preparedKey) == "" {
		t.Fatalf("内联私钥应返回认证方法和内存私钥")
	}
	assertLocalCredential(t, credential, "node_private_key", "node.private_key", nil)
	assertCredentialMetadataSafe(t, credential, privateKey)
}

func TestLocalCredentialProviderPasswordAuthUsesEncryptedNodeStorage(t *testing.T) {
	db := newCredentialProviderTestDB(t)
	authSecret := "FAKE_NODE_PASSWORD_" + strings.ReplaceAll(t.Name(), "/", "_") + "_FOR_TEST_ONLY"
	node := model.Node{
		Name:      "password-node",
		Host:      "127.0.0.1",
		Port:      22,
		Username:  "root",
		AuthType:  "password",
		Password:  authSecret,
		BackupDir: "password-backup",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建测试节点失败: %v", err)
	}
	assertStoredValueEncrypted(t, db, "nodes", "password", node.ID)

	var loaded model.Node
	if err := db.First(&loaded, node.ID).Error; err != nil {
		t.Fatalf("读取测试节点失败: %v", err)
	}
	auth, preparedKey, credential, err := BuildSSHAuthWithKeyForPurpose(loaded, db, PurposeTerminal)
	if err != nil {
		t.Fatalf("密码认证应通过 local provider 构建认证: %v", err)
	}
	if len(auth) != 1 || preparedKey != "" {
		t.Fatalf("密码认证应返回认证方法且不返回私钥")
	}
	assertLocalCredential(t, credential, "password", "node.password", nil)
	assertCredentialMetadataSafe(t, credential, authSecret)
}

func TestLocalCredentialProviderDeniesManagedSSHKeyScopeBeforeUse(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour)
	expired := time.Now().UTC().Add(-time.Hour)
	cases := []struct {
		name    string
		key     model.SSHKey
		node    model.Node
		wantErr string
	}{
		{
			name: "disabled",
			key: model.SSHKey{
				Disabled:        true,
				AllowedPurposes: PurposeTerminal,
			},
			node:    model.Node{ID: 9, Tags: "prod"},
			wantErr: "已禁用",
		},
		{
			name: "expired",
			key: model.SSHKey{
				ExpiresAt:       &expired,
				AllowedPurposes: PurposeTerminal,
			},
			node:    model.Node{ID: 9, Tags: "prod"},
			wantErr: "已过期",
		},
		{
			name: "purpose denied",
			key: model.SSHKey{
				ExpiresAt:       &future,
				AllowedPurposes: PurposeTaskCommand,
			},
			node:    model.Node{ID: 9, Tags: "prod"},
			wantErr: "不允许用于当前操作",
		},
		{
			name: "node denied",
			key: model.SSHKey{
				ExpiresAt:       &future,
				AllowedPurposes: PurposeTerminal,
				AllowedNodeIDs:  "8",
			},
			node:    model.Node{ID: 9, Tags: "prod"},
			wantErr: "不允许用于该节点",
		},
		{
			name: "tag denied",
			key: model.SSHKey{
				ExpiresAt:       &future,
				AllowedPurposes: PurposeTerminal,
				AllowedNodeTags: "backup",
			},
			node:    model.Node{ID: 9, Tags: "prod"},
			wantErr: "不允许用于该节点标签",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newCredentialProviderTestDB(t)
			privateKey := buildRSAPrivateKeyForTest(t)
			sshKey := tc.key
			sshKey.Name = "scope-denied-key-" + strings.ReplaceAll(tc.name, " ", "-")
			sshKey.Username = "root"
			sshKey.KeyType = SSHKeyTypeRSA
			sshKey.PrivateKey = privateKey
			sshKey.Fingerprint = "SHA256:" + sshKey.Name
			if err := db.Create(&sshKey).Error; err != nil {
				t.Fatalf("创建测试 SSH Key 失败: %v", err)
			}

			node := tc.node
			node.AuthType = "key"
			node.Username = "root"
			node.SSHKeyID = &sshKey.ID
			auth, preparedKey, credential, err := BuildSSHAuthWithKeyForPurpose(node, db, PurposeTerminal)
			if err == nil {
				t.Fatalf("不满足 scope 的托管 SSH Key 应被拒绝")
			}
			if len(auth) != 0 || preparedKey != "" {
				t.Fatalf("scope denial 不应返回认证材料")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("scope denial 应返回 %q，实际: %v", tc.wantErr, err)
			}
			if strings.Contains(err.Error(), privateKey) {
				t.Fatalf("scope denial 错误不应包含原始私钥")
			}
			assertLocalCredential(t, credential, "ssh_key", fmt.Sprintf("ssh_key_id=%d", sshKey.ID), &sshKey.ID)

			var refreshed model.SSHKey
			if err := db.First(&refreshed, sshKey.ID).Error; err != nil {
				t.Fatalf("重新读取 SSH Key 失败: %v", err)
			}
			if refreshed.LastUsedAt != nil {
				t.Fatalf("scope denial 不应更新 LastUsedAt")
			}
		})
	}
}

func TestLocalCredentialProviderMissingManagedSSHKeyFailsClosed(t *testing.T) {
	db := newCredentialProviderTestDB(t)
	missingKeyID := uint(404)
	node := model.Node{ID: 10, AuthType: "key", Username: "root", SSHKeyID: &missingKeyID}

	auth, preparedKey, credential, err := BuildSSHAuthWithKeyForPurpose(node, db, PurposeTerminal)
	if err == nil {
		t.Fatalf("缺失的托管 SSH Key 应 fail closed")
	}
	if len(auth) != 0 || preparedKey != "" {
		t.Fatalf("托管 SSH Key 缺失不应返回认证材料")
	}
	if !strings.Contains(err.Error(), "节点绑定的密钥不存在") {
		t.Fatalf("托管 SSH Key 缺失应返回安全错误，实际: %v", err)
	}
	assertLocalCredential(t, credential, "ssh_key", fmt.Sprintf("ssh_key_id=%d", missingKeyID), &missingKeyID)
}

func TestLocalCredentialProviderInvalidKeyErrorIsSecretFree(t *testing.T) {
	invalidSecret := "FAKE_INVALID_PRIVATE_KEY_FOR_TEST_ONLY"
	node := model.Node{AuthType: "key", PrivateKey: invalidSecret}
	auth, preparedKey, credential, err := BuildSSHAuthWithKeyForPurpose(node, nil, PurposeTerminal)
	if err == nil {
		t.Fatalf("无效私钥应构建失败")
	}
	if len(auth) != 0 || preparedKey != "" {
		t.Fatalf("无效私钥不应返回认证材料")
	}
	if strings.Contains(err.Error(), invalidSecret) {
		t.Fatalf("私钥校验错误不应包含原始输入")
	}
	assertLocalCredential(t, credential, "node_private_key", "node.private_key", nil)
	assertCredentialMetadataSafe(t, credential, invalidSecret)
}

func newCredentialProviderTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_CREDENTIAL_PROVIDER_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.SSHKey{}, &model.Node{}); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	return db
}

func assertStoredValueEncrypted(t *testing.T, db *gorm.DB, table string, column string, id uint) {
	t.Helper()
	var stored string
	if err := db.Table(table).Select(column).Where("id = ?", id).Scan(&stored).Error; err != nil {
		t.Fatalf("读取测试存储值失败: %v", err)
	}
	if !secure.IsEncrypted(stored) {
		t.Fatalf("本地数据库应保存加密后的凭据值")
	}
}

func assertLocalCredential(t *testing.T, credential ResolvedCredential, kind string, source string, keyID *uint) {
	t.Helper()
	if credential.Kind != kind || credential.Source != source || credential.Provider != CredentialProviderLocal {
		t.Fatalf("credential metadata 不符合预期: kind=%q source=%q provider=%q", credential.Kind, credential.Source, credential.Provider)
	}
	if keyID == nil {
		if credential.KeyID != nil {
			t.Fatalf("credential metadata 不应包含 SSH Key ID")
		}
		return
	}
	if credential.KeyID == nil || *credential.KeyID != *keyID {
		t.Fatalf("credential metadata SSH Key ID 不符合预期")
	}
}

func assertCredentialMetadataSafe(t *testing.T, credential ResolvedCredential, forbidden ...string) {
	t.Helper()
	fields := []string{credential.Kind, credential.Source, credential.Provider}
	for _, field := range fields {
		for _, value := range forbidden {
			if value != "" && strings.Contains(field, value) {
				t.Fatalf("credential metadata 不应包含原始凭据材料")
			}
		}
	}
}
