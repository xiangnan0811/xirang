package executor

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestResolveSSHUserReturnsConfiguredUsername(t *testing.T) {
	node := model.Node{Name: "test-node", Username: "deploy"}
	user := ResolveSSHUser(node)
	if user != "deploy" {
		t.Fatalf("期望用户名=deploy，实际=%s", user)
	}
}

func TestResolveSSHUserDefaultsToRootWhenEmpty(t *testing.T) {
	node := model.Node{Name: "test-node", Username: ""}
	user := ResolveSSHUser(node)
	if user != "root" {
		t.Fatalf("期望空用户名回退到 root，实际=%s", user)
	}
}

func TestResolveSSHUserTrimsWhitespace(t *testing.T) {
	node := model.Node{Name: "test-node", Username: "  admin  "}
	user := ResolveSSHUser(node)
	if user != "admin" {
		t.Fatalf("期望去除空白后=admin，实际=%s", user)
	}
}

func TestResolveSSHAuthMethodsRejectsEmptyAuthType(t *testing.T) {
	node := model.Node{Name: "test-node", AuthType: ""}
	_, err := resolveSSHAuthMethods(node)
	if err == nil {
		t.Fatal("期望空认证类型报错")
	}
}

func TestResolveSSHAuthMethodsRejectsPasswordWithoutPassword(t *testing.T) {
	node := model.Node{Name: "test-node", AuthType: "password", Password: ""}
	_, err := resolveSSHAuthMethods(node)
	if err == nil {
		t.Fatal("期望无密码时报错")
	}
}

func TestResolveSSHAuthMethodsRejectsKeyWithoutKey(t *testing.T) {
	node := model.Node{Name: "test-node", AuthType: "key"}
	_, err := resolveSSHAuthMethods(node)
	if err == nil {
		t.Fatal("期望无密钥时报错")
	}
}

func TestResolveSSHAuthMethodsUsesLocalProviderForPassword(t *testing.T) {
	node := model.Node{Name: "test-node", AuthType: "password", Password: "FAKE_PASSWORD_FOR_TEST_ONLY"}
	authMethods, credential, err := resolveSSHAuthMethodsForPurpose(node, sshutil.PurposeTaskCommand)
	if err != nil {
		t.Fatalf("期望密码认证解析成功，实际失败: %v", err)
	}
	if len(authMethods) != 1 {
		t.Fatalf("期望 1 个认证方法，实际=%d", len(authMethods))
	}
	if credential.Kind != "password" || credential.Source != "node.password" || credential.Provider != sshutil.CredentialProviderLocal {
		t.Fatalf("credential metadata 不符合预期: %+v", credential)
	}
}

func TestResolveSSHAuthMethodsUsesLocalProviderForManagedKey(t *testing.T) {
	keyID := uint(42)
	node := model.Node{
		ID:       7,
		AuthType: "key",
		SSHKeyID: &keyID,
		SSHKey: &model.SSHKey{
			ID:              keyID,
			PrivateKey:      buildExecutorTestPrivateKey(t),
			AllowedPurposes: sshutil.PurposeTaskCommand,
			AllowedNodeIDs:  "7",
		},
	}

	authMethods, credential, err := resolveSSHAuthMethodsForPurpose(node, sshutil.PurposeTaskCommand)
	if err != nil {
		t.Fatalf("期望托管密钥认证解析成功，实际失败: %v", err)
	}
	if len(authMethods) != 1 {
		t.Fatalf("期望 1 个认证方法，实际=%d", len(authMethods))
	}
	if credential.Kind != "ssh_key" || credential.Source != "ssh_key_id=42" || credential.Provider != sshutil.CredentialProviderLocal || credential.KeyID == nil || *credential.KeyID != keyID {
		t.Fatalf("credential metadata 不符合预期: %+v", credential)
	}
}

func TestResolveSSHAuthMethodsUsesLocalProviderForInlinePrivateKey(t *testing.T) {
	node := model.Node{
		AuthType:   "key",
		PrivateKey: buildExecutorTestPrivateKey(t),
	}

	authMethods, credential, err := resolveSSHAuthMethodsForPurpose(node, sshutil.PurposeTaskCommand)
	if err != nil {
		t.Fatalf("期望内联私钥认证解析成功，实际失败: %v", err)
	}
	if len(authMethods) != 1 {
		t.Fatalf("期望 1 个认证方法，实际=%d", len(authMethods))
	}
	if credential.Kind != "node_private_key" || credential.Source != "node.private_key" || credential.Provider != sshutil.CredentialProviderLocal {
		t.Fatalf("credential metadata 不符合预期: %+v", credential)
	}
}

func TestWriteRuntimeCredentialAuditStoresSafeLocalProviderMetadata(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}

	keyID := uint(42)
	nodeID := uint(7)
	taskID := uint(100)
	runID := uint(200)
	privateKey := buildExecutorTestPrivateKey(t)
	ctx := credentialaudit.WithRuntimeContext(context.Background(), db, credentialaudit.Event{
		Username:  "system",
		Role:      "system",
		TaskID:    &taskID,
		TaskRunID: &runID,
	})

	credential := sshutil.ResolvedCredential{
		Kind:     "ssh_key",
		Source:   "ssh_key_id=42",
		Provider: sshutil.CredentialProviderLocal,
		KeyID:    &keyID,
	}
	writeRuntimeCredentialAudit(ctx, model.Node{ID: nodeID, AuthType: "key"}, sshutil.PurposeTaskCommand, credential, credentialaudit.OutcomeFailure, "dial", fmt.Errorf("ssh failed with private key: %s", privateKey), 15)

	var event model.CredentialAuditEvent
	if err := db.Where("action = ?", "task.credential.use").First(&event).Error; err != nil {
		t.Fatalf("期望写入凭据审计事件: %v", err)
	}
	if event.CredentialKind != "ssh_key" || event.CredentialSource != "ssh_key_id=42" || event.SSHKeyID == nil || *event.SSHKeyID != keyID {
		t.Fatalf("审计凭据字段不符合预期: %+v", event)
	}
	if event.ErrorMessage != "dial failed" {
		t.Fatalf("期望安全阶段错误，实际: %q", event.ErrorMessage)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
		t.Fatalf("解析审计 metadata 失败: %v", err)
	}
	if metadata["provider"] != sshutil.CredentialProviderLocal || metadata["stage"] != "dial" || metadata["auth_type"] != "key" {
		t.Fatalf("期望审计 metadata 包含安全 local provider/stage/auth_type，实际: %#v", metadata)
	}
	if strings.Contains(event.Metadata, "PRIVATE KEY") || strings.Contains(event.Metadata, privateKey) || strings.Contains(event.ErrorMessage, "PRIVATE KEY") || strings.Contains(event.ErrorMessage, privateKey) {
		t.Fatalf("凭据审计不应包含私钥内容: metadata=%s error=%s", event.Metadata, event.ErrorMessage)
	}
}

func TestResolveSSHAuthMethodsFailsClosedForMissingManagedKey(t *testing.T) {
	keyID := uint(42)
	stalePrivateKey := "FAKE_STALE_PRIVATE_KEY_FOR_TEST_ONLY"
	node := model.Node{
		AuthType:   "key",
		SSHKeyID:   &keyID,
		PrivateKey: stalePrivateKey,
	}

	authMethods, credential, err := resolveSSHAuthMethodsForPurpose(node, sshutil.PurposeTaskCommand)
	if err == nil {
		t.Fatal("期望缺失托管密钥时报错")
	}
	if len(authMethods) != 0 {
		t.Fatalf("失败时不应返回认证方法，实际=%d", len(authMethods))
	}
	if !strings.Contains(err.Error(), "节点绑定的密钥不存在") {
		t.Fatalf("期望缺失托管密钥错误，实际: %v", err)
	}
	if strings.Contains(err.Error(), "私钥格式无效") || strings.Contains(err.Error(), stalePrivateKey) {
		t.Fatalf("不应回退使用 node.private_key，实际: %v", err)
	}
	if credential.Kind != "ssh_key" || credential.Source != "ssh_key_id=42" || credential.Provider != sshutil.CredentialProviderLocal || credential.KeyID == nil || *credential.KeyID != keyID {
		t.Fatalf("credential metadata 不符合预期: %+v", credential)
	}
}

func buildExecutorTestPrivateKey(t *testing.T) string {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("生成测试私钥失败: %v", err)
	}
	privateKey := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(rsaKey),
	})
	if len(privateKey) == 0 {
		t.Fatal("编码测试私钥失败")
	}
	return string(privateKey)
}
