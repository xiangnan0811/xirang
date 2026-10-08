package handlers

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSSHKeyHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_32_BYTES_FOR_TEST_ONLY")
	// Mirror production's concurrent SQLite settings while retaining this
	// fixture's historical foreign-key behavior for synthetic visibility rows.
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_txlock=immediate&_loc=UTC", filepath.Join(t.TempDir(), "handler.db"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取测试数据库连接池失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	if err := db.AutoMigrate(&model.SSHKey{}, &model.Node{}, &model.NodeOwner{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	return db
}

func buildSSHKeyPrivateKeyForHandlerTest(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("生成测试私钥失败: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if len(pemBytes) == 0 {
		t.Fatalf("编码测试私钥失败")
	}
	return string(pemBytes)
}

func seedSSHKeyForVisibility(t *testing.T, db *gorm.DB, name string) model.SSHKey {
	t.Helper()
	key := model.SSHKey{
		Name:        name,
		Username:    "root",
		KeyType:     "auto",
		PrivateKey:  buildSSHKeyPrivateKeyForHandlerTest(t),
		Fingerprint: "SHA256:" + name,
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("创建 SSH key %s 失败: %v", name, err)
	}
	return key
}

func seedNodeWithSSHKey(t *testing.T, db *gorm.DB, name string, keyID uint) model.Node {
	t.Helper()
	node := model.Node{
		Name:      name,
		Host:      "10.0.10." + fmt.Sprint(keyID),
		Port:      22,
		Username:  "root",
		AuthType:  "key",
		SSHKeyID:  &keyID,
		BackupDir: name,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点 %s 失败: %v", name, err)
	}
	return node
}

func newSSHKeyHandlerRouter(db *gorm.DB, role string, userID uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, role)
		c.Set(middleware.CtxUserID, userID)
		c.Next()
	})
	handler := NewSSHKeyHandler(db)
	r.POST("/ssh-keys/preview", handler.Preview)
	r.GET("/ssh-keys", handler.List)
	r.POST("/ssh-keys", handler.Create)
	r.GET("/ssh-keys/export", handler.Export)
	r.GET("/ssh-keys/:id", handler.Get)
	r.PUT("/ssh-keys/:id", handler.Update)
	r.POST("/ssh-keys/:id/test-connection", handler.TestConnection)
	return r
}

func newSSHKeyVisibilityRouter(db *gorm.DB, role string, userID uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, role)
		c.Set(middleware.CtxUserID, userID)
		c.Next()
	})
	handler := NewSSHKeyHandler(db)
	r.GET("/ssh-keys", handler.List)
	r.GET("/ssh-keys/export", handler.Export)
	r.GET("/ssh-keys/:id", handler.Get)
	return r
}

func requestSSHKeyVisibility(r *gin.Engine, method string, path string) *httptest.ResponseRecorder {
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	r.ServeHTTP(resp, req)
	return resp
}

func decodeSSHKeyEnvelope(t *testing.T, body string) []sshKeyResponseItem {
	t.Helper()
	var envelope struct {
		Data []sshKeyResponseItem `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("解析 SSH key 响应失败: %v body=%s", err, body)
	}
	return envelope.Data
}

func assertSSHKeyNames(t *testing.T, items []sshKeyResponseItem, want []string) {
	t.Helper()
	if len(items) != len(want) {
		t.Fatalf("SSH key 数量不符合预期，want=%v got=%+v", want, items)
	}
	for i := range want {
		if items[i].Name != want[i] {
			t.Fatalf("SSH key 顺序/名称不符合预期，want=%v got=%+v", want, items)
		}
	}
}
func buildSSHKeyPrivateKeyForHandlerTestType(t *testing.T, keyType string) string {
	t.Helper()
	switch keyType {
	case "rsa":
		return buildSSHKeyPrivateKeyForHandlerTest(t)
	case "ecdsa":
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("生成 ECDSA 测试私钥失败: %v", err)
		}
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatalf("编码 ECDSA 测试私钥失败: %v", err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	case "ed25519":
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("生成 ED25519 测试私钥失败: %v", err)
		}
		block, err := ssh.MarshalPrivateKey(key, "")
		if err != nil {
			t.Fatalf("编码 ED25519 OpenSSH 测试私钥失败: %v", err)
		}
		return string(pem.EncodeToMemory(block))
	default:
		t.Fatalf("不支持的测试密钥类型: %s", keyType)
		return ""
	}
}

func buildSSHKeyPassphraseProtectedForHandlerTest(t *testing.T) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成受保护 ED25519 测试私钥失败: %v", err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte("test-passphrase"))
	if err != nil {
		t.Fatalf("编码受保护 ED25519 测试私钥失败: %v", err)
	}
	return string(pem.EncodeToMemory(block))
}

func requestSSHKeyPreview(t *testing.T, r *gin.Engine, privateKey, keyType string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(sshKeyPreviewRequest{PrivateKey: privateKey, KeyType: keyType})
	if err != nil {
		t.Fatalf("编码 SSH key preview 请求失败: %v", err)
	}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ssh-keys/preview", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(resp, req)
	return resp
}

func TestSSHKeyPreviewSupportsKeyEncodingsAndFingerprints(t *testing.T) {
	router := newSSHKeyHandlerRouter(nil, "admin", 1)
	tests := []struct {
		name         string
		keyType      string
		selectedType string
		publicPrefix string
	}{
		{name: "rsa_pkcs1_auto", keyType: "rsa", selectedType: "auto", publicPrefix: "ssh-rsa "},
		{name: "ecdsa_sec1_explicit", keyType: "ecdsa", selectedType: "ecdsa", publicPrefix: "ecdsa-sha2-nistp256 "},
		{name: "ed25519_openssh_explicit", keyType: "ed25519", selectedType: "ed25519", publicPrefix: "ssh-ed25519 "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			privateKey := buildSSHKeyPrivateKeyForHandlerTestType(t, tt.keyType)
			resp := requestSSHKeyPreview(t, router, privateKey, tt.selectedType)
			if resp.Code != http.StatusOK {
				t.Fatalf("预览 status=%d body=%s", resp.Code, resp.Body.String())
			}
			if got := resp.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("预览应禁止共享缓存，实际 Cache-Control=%q", got)
			}

			var envelope struct {
				Data sshKeyPreviewResponse `json:"data"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("解析预览响应失败: %v body=%s", err, resp.Body.String())
			}
			prepared, expectedType, err := sshutil.ValidateAndPreparePrivateKey(privateKey, tt.selectedType)
			if err != nil {
				t.Fatalf("准备预期私钥失败: %v", err)
			}
			expectedPublic, err := sshutil.DerivePublicKey(prepared)
			if err != nil {
				t.Fatalf("派生预期公钥失败: %v", err)
			}
			expectedParsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(expectedPublic))
			if err != nil {
				t.Fatalf("解析预期公钥失败: %v", err)
			}
			if envelope.Data.KeyType != expectedType {
				t.Fatalf("预览 key_type=%q，期望 %q", envelope.Data.KeyType, expectedType)
			}
			if !strings.HasPrefix(envelope.Data.PublicKey, tt.publicPrefix) {
				t.Fatalf("预览 public_key=%q，不符合 %s", envelope.Data.PublicKey, tt.publicPrefix)
			}
			if envelope.Data.PublicKeyFingerprint != ssh.FingerprintSHA256(expectedParsed) {
				t.Fatalf("预览公钥指纹不符合预期，实际=%q", envelope.Data.PublicKeyFingerprint)
			}
			if strings.Contains(resp.Body.String(), privateKey) || strings.Contains(resp.Body.String(), `"private_key"`) {
				t.Fatalf("预览响应不得回显私钥: %s", resp.Body.String())
			}
		})
	}
}

func TestSSHKeyPreviewDoesNotRequireDatabase(t *testing.T) {
	privateKey := buildSSHKeyPrivateKeyForHandlerTest(t)
	resp := requestSSHKeyPreview(t, newSSHKeyHandlerRouter(nil, "admin", 1), privateKey, "rsa")
	if resp.Code != http.StatusOK {
		t.Fatalf("nil DB 预览 status=%d body=%s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("nil DB 预览应禁止共享缓存，实际 Cache-Control=%q", got)
	}
}

func TestSSHKeyPreviewRejectsUnsafeInputsWithoutParserDetails(t *testing.T) {
	privateKey := buildSSHKeyPrivateKeyForHandlerTest(t)
	ed25519PrivateKey := buildSSHKeyPrivateKeyForHandlerTestType(t, "ed25519")
	tests := []struct {
		name       string
		privateKey string
		keyType    string
		forbidden  []string
	}{
		{
			name:       "invalid",
			privateKey: "-----BEGIN RSA PRIVATE KEY-----\nnot-a-private-key\n-----END RSA PRIVATE KEY-----",
			keyType:    "auto",
			forbidden:  []string{"not-a-private-key", "ssh:", "parse"},
		},
		{
			name:       "protected",
			privateKey: buildSSHKeyPassphraseProtectedForHandlerTest(t),
			keyType:    "auto",
			forbidden:  []string{"passphrase", "protected", "ssh:"},
		},
		{
			name:       "type_mismatch",
			privateKey: privateKey,
			keyType:    "ed25519",
			forbidden:  []string{"RSA", "ED25519", "ssh:", "mismatch"},
		},
		{
			name:       "ed25519_candidate_as_rsa",
			privateKey: ed25519PrivateKey,
			keyType:    "rsa",
			forbidden:  []string{"RSA", "ED25519", "ssh:", "mismatch"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := requestSSHKeyPreview(t, newSSHKeyHandlerRouter(nil, "admin", 1), tt.privateKey, tt.keyType)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("预览拒绝 status=%d body=%s", resp.Code, resp.Body.String())
			}
			if got := resp.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("拒绝响应应禁止共享缓存，实际 Cache-Control=%q", got)
			}
			body := resp.Body.String()
			for _, forbidden := range tt.forbidden {
				if strings.Contains(body, forbidden) {
					t.Fatalf("预览错误响应泄漏 %q: %s", forbidden, body)
				}
			}
			if strings.Contains(body, `"private_key"`) || strings.Contains(body, tt.privateKey) {
				t.Fatalf("预览错误响应不得回显私钥: %s", body)
			}
		})
	}
}

func TestSSHKeyPreviewRejectsOversizeBody(t *testing.T) {
	payload := fmt.Sprintf(`{"private_key":%q,"key_type":"auto"}`, strings.Repeat("A", 1<<20))
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ssh-keys/preview", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	newSSHKeyHandlerRouter(nil, "admin", 1).ServeHTTP(resp, req)
	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大预览请求 status=%d body=%s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("超大请求响应应禁止共享缓存，实际 Cache-Control=%q", got)
	}
	if strings.Contains(resp.Body.String(), "A") || strings.Contains(resp.Body.String(), "private_key") {
		t.Fatalf("超大请求响应不得回显请求数据: %s", resp.Body.String())
	}
}

func TestSSHKeyPreviewConsumesEntireBoundedJSONBody(t *testing.T) {
	key := buildSSHKeyPrivateKeyForHandlerTest(t)
	prefix := fmt.Sprintf(`{"private_key":%q,"key_type":"auto"}`, key)
	for _, tc := range []struct {
		name          string
		body          string
		unknownLength bool
		want          int
	}{
		{"exact limit", prefix + strings.Repeat(" ", (1<<20)-len(prefix)), false, http.StatusOK},
		{"oversize trailing whitespace", prefix + strings.Repeat(" ", 1<<20), false, http.StatusRequestEntityTooLarge},
		{"oversize unknown length", prefix + strings.Repeat(" ", 1<<20), true, http.StatusRequestEntityTooLarge},
		{"second JSON document", prefix + `{}`, false, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/ssh-keys/preview", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.unknownLength {
				req.ContentLength = -1
			}
			resp := httptest.NewRecorder()
			newSSHKeyHandlerRouter(nil, "admin", 1).ServeHTTP(resp, req)
			if resp.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", resp.Code, tc.want, resp.Body.String())
			}
			if strings.Contains(resp.Body.String(), key) {
				t.Fatal("response exposed private key")
			}
		})
	}
}

func TestSSHKeyResponsePreservesPrivateDigestAndAddsPublicFingerprint(t *testing.T) {
	db := openSSHKeyHandlerTestDB(t)
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	privateKey := buildSSHKeyPrivateKeyForHandlerTest(t)
	key := model.SSHKey{
		Name:        "response-compatibility-key",
		Username:    "root",
		KeyType:     "rsa",
		PrivateKey:  privateKey,
		Fingerprint: "SHA256:historical-private-digest",
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("创建兼容性测试 SSH key 失败: %v", err)
	}

	resp := requestSSHKeyVisibility(newSSHKeyHandlerRouter(db, "admin", 1), http.MethodGet, fmt.Sprintf("/ssh-keys/%d", key.ID))
	if resp.Code != http.StatusOK {
		t.Fatalf("读取兼容性测试 SSH key status=%d body=%s", resp.Code, resp.Body.String())
	}
	var envelope struct {
		Data sshKeyResponseItem `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	publicKey, err := sshutil.DerivePublicKey(privateKey)
	if err != nil {
		t.Fatalf("派生兼容性测试公钥失败: %v", err)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil {
		t.Fatalf("解析兼容性测试公钥失败: %v", err)
	}
	if envelope.Data.Fingerprint != key.Fingerprint {
		t.Fatalf("历史 fingerprint 被改变，实际=%q 期望=%q", envelope.Data.Fingerprint, key.Fingerprint)
	}
	if envelope.Data.PublicKeyFingerprint != ssh.FingerprintSHA256(parsed) {
		t.Fatalf("标准公钥指纹不符合预期，实际=%q", envelope.Data.PublicKeyFingerprint)
	}
	if strings.Contains(resp.Body.String(), privateKey) || strings.Contains(resp.Body.String(), `"private_key"`) {
		t.Fatalf("存储 SSH key 响应不得回显私钥: %s", resp.Body.String())
	}

	invalidResponse := toSSHKeyResponse(model.SSHKey{
		Fingerprint: "SHA256:private-only",
		PrivateKey:  "not-a-private-key",
	})
	if invalidResponse.PublicKeyFingerprint != "" {
		t.Fatalf("无法从公钥派生时不得回退私钥摘要，实际=%q", invalidResponse.PublicKeyFingerprint)
	}
}

func TestSSHKeyUpdatePreservesScopeMetadataWhenOmitted(t *testing.T) {
	db := openSSHKeyHandlerTestDB(t)
	secure.ResetForTesting()
	future := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	key := seedSSHKeyForVisibility(t, db, "scoped-update-key")
	key.Disabled = true
	key.ExpiresAt = &future
	key.AllowedPurposes = "terminal"
	key.AllowedNodeIDs = "7"
	key.AllowedNodeTags = "prod"
	if err := db.Save(&key).Error; err != nil {
		t.Fatalf("更新测试 SSH key scope 失败: %v", err)
	}

	body := `{"name":"scoped-update-key-renamed","username":"deploy","key_type":"auto"}`
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/ssh-keys/%d", key.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	newSSHKeyHandlerRouter(db, "admin", 1).ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("更新 SSH key 期望 200，实际 status=%d body=%s", resp.Code, resp.Body.String())
	}

	var updated model.SSHKey
	if err := db.First(&updated, key.ID).Error; err != nil {
		t.Fatalf("读取更新后 SSH key 失败: %v", err)
	}
	if !updated.Disabled || updated.ExpiresAt == nil || !updated.ExpiresAt.Equal(future) || updated.AllowedPurposes != "terminal" || updated.AllowedNodeIDs != "7" || updated.AllowedNodeTags != "prod" {
		t.Fatalf("省略 scope 字段时不应清空限制，实际: disabled=%v expires=%v purposes=%q nodes=%q tags=%q", updated.Disabled, updated.ExpiresAt, updated.AllowedPurposes, updated.AllowedNodeIDs, updated.AllowedNodeTags)
	}
}

func TestSSHKeyUpdateAllowsExplicitScopeClearing(t *testing.T) {
	db := openSSHKeyHandlerTestDB(t)
	secure.ResetForTesting()
	future := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	key := seedSSHKeyForVisibility(t, db, "scoped-clear-key")
	key.Disabled = true
	key.ExpiresAt = &future
	key.AllowedPurposes = "terminal"
	key.AllowedNodeIDs = "7"
	key.AllowedNodeTags = "prod"
	if err := db.Save(&key).Error; err != nil {
		t.Fatalf("更新测试 SSH key scope 失败: %v", err)
	}

	payload := []byte(`{"name":"scoped-clear-key","username":"root","key_type":"auto","disabled":false,"expires_at":null,"allowed_purposes":"","allowed_node_ids":"","allowed_node_tags":""}`)
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/ssh-keys/%d", key.ID), bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	newSSHKeyHandlerRouter(db, "admin", 1).ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("清空 SSH key scope 期望 200，实际 status=%d body=%s", resp.Code, resp.Body.String())
	}

	var updated model.SSHKey
	if err := db.First(&updated, key.ID).Error; err != nil {
		t.Fatalf("读取更新后 SSH key 失败: %v", err)
	}
	if updated.Disabled || updated.ExpiresAt != nil || updated.AllowedPurposes != "" || updated.AllowedNodeIDs != "" || updated.AllowedNodeTags != "" {
		t.Fatalf("显式空 scope 字段应允许清空限制，实际: disabled=%v expires=%v purposes=%q nodes=%q tags=%q", updated.Disabled, updated.ExpiresAt, updated.AllowedPurposes, updated.AllowedNodeIDs, updated.AllowedNodeTags)
	}
}

func TestSSHKeyListRejectsMissingRoleContext(t *testing.T) {
	db := openSSHKeyHandlerTestDB(t)
	seedSSHKeyForVisibility(t, db, "missing-role-key")

	resp := requestSSHKeyVisibility(newSSHKeyVisibilityRouter(db, "", 0), http.MethodGet, "/ssh-keys")
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("missing role context 应 fail-closed，实际 status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestSSHKeyListRestrictsNonAdminToVisibleNodeKeys(t *testing.T) {
	db := openSSHKeyHandlerTestDB(t)
	ownedKey := seedSSHKeyForVisibility(t, db, "owned-key")
	unownedKey := seedSSHKeyForVisibility(t, db, "unowned-key")
	seedSSHKeyForVisibility(t, db, "unbound-key")
	ownedNode := seedNodeWithSSHKey(t, db, "owned-node", ownedKey.ID)
	seedNodeWithSSHKey(t, db, "unowned-node", unownedKey.ID)
	operatorID := uint(101)
	if err := db.Create(&model.NodeOwner{NodeID: ownedNode.ID, UserID: operatorID}).Error; err != nil {
		t.Fatalf("创建节点 owner 失败: %v", err)
	}

	adminResp := requestSSHKeyVisibility(newSSHKeyVisibilityRouter(db, "admin", 1), http.MethodGet, "/ssh-keys")
	if adminResp.Code != http.StatusOK {
		t.Fatalf("admin list status=%d body=%s", adminResp.Code, adminResp.Body.String())
	}
	assertSSHKeyNames(t, decodeSSHKeyEnvelope(t, adminResp.Body.String()), []string{"owned-key", "unowned-key", "unbound-key"})

	operatorResp := requestSSHKeyVisibility(newSSHKeyVisibilityRouter(db, "operator", operatorID), http.MethodGet, "/ssh-keys")
	if operatorResp.Code != http.StatusOK {
		t.Fatalf("operator list status=%d body=%s", operatorResp.Code, operatorResp.Body.String())
	}
	assertSSHKeyNames(t, decodeSSHKeyEnvelope(t, operatorResp.Body.String()), []string{"owned-key"})

	viewerResp := requestSSHKeyVisibility(newSSHKeyVisibilityRouter(db, "viewer", 202), http.MethodGet, "/ssh-keys")
	if viewerResp.Code != http.StatusOK {
		t.Fatalf("viewer list status=%d body=%s", viewerResp.Code, viewerResp.Body.String())
	}
	assertSSHKeyNames(t, decodeSSHKeyEnvelope(t, viewerResp.Body.String()), []string{"owned-key", "unowned-key"})
}

func TestSSHKeyExportAndGetRestrictNonAdminVisibility(t *testing.T) {
	db := openSSHKeyHandlerTestDB(t)
	ownedKey := seedSSHKeyForVisibility(t, db, "owned-export-key")
	unownedKey := seedSSHKeyForVisibility(t, db, "unowned-export-key")
	unboundKey := seedSSHKeyForVisibility(t, db, "unbound-export-key")
	ownedNode := seedNodeWithSSHKey(t, db, "owned-export-node", ownedKey.ID)
	seedNodeWithSSHKey(t, db, "unowned-export-node", unownedKey.ID)
	operatorID := uint(303)
	if err := db.Create(&model.NodeOwner{NodeID: ownedNode.ID, UserID: operatorID}).Error; err != nil {
		t.Fatalf("创建节点 owner 失败: %v", err)
	}

	adminRouter := newSSHKeyVisibilityRouter(db, "admin", 1)
	adminExportResp := requestSSHKeyVisibility(adminRouter, http.MethodGet, "/ssh-keys/export?format=json&scope=all")
	if adminExportResp.Code != http.StatusOK {
		t.Fatalf("admin export status=%d body=%s", adminExportResp.Code, adminExportResp.Body.String())
	}
	var adminExported []sshKeyResponseItem
	if err := json.Unmarshal(adminExportResp.Body.Bytes(), &adminExported); err != nil {
		t.Fatalf("解析 admin 导出 JSON 失败: %v", err)
	}
	assertSSHKeyNames(t, adminExported, []string{"owned-export-key", "unowned-export-key", "unbound-export-key"})

	router := newSSHKeyVisibilityRouter(db, "operator", operatorID)
	exportResp := requestSSHKeyVisibility(router, http.MethodGet, "/ssh-keys/export?format=json&scope=all")
	if exportResp.Code != http.StatusOK {
		t.Fatalf("operator export status=%d body=%s", exportResp.Code, exportResp.Body.String())
	}
	var exported []sshKeyResponseItem
	if err := json.Unmarshal(exportResp.Body.Bytes(), &exported); err != nil {
		t.Fatalf("解析导出 JSON 失败: %v", err)
	}
	assertSSHKeyNames(t, exported, []string{"owned-export-key"})

	inUseResp := requestSSHKeyVisibility(router, http.MethodGet, "/ssh-keys/export?format=json&scope=in_use")
	if inUseResp.Code != http.StatusOK {
		t.Fatalf("operator in_use export status=%d body=%s", inUseResp.Code, inUseResp.Body.String())
	}
	var inUseExported []sshKeyResponseItem
	if err := json.Unmarshal(inUseResp.Body.Bytes(), &inUseExported); err != nil {
		t.Fatalf("解析 in_use 导出 JSON 失败: %v", err)
	}
	assertSSHKeyNames(t, inUseExported, []string{"owned-export-key"})

	getResp := requestSSHKeyVisibility(router, http.MethodGet, fmt.Sprintf("/ssh-keys/%d", unownedKey.ID))
	if getResp.Code != http.StatusNotFound {
		t.Fatalf("operator get unowned key 应隐藏为 404，实际 status=%d body=%s", getResp.Code, getResp.Body.String())
	}
	getUnboundResp := requestSSHKeyVisibility(router, http.MethodGet, fmt.Sprintf("/ssh-keys/%d", unboundKey.ID))
	if getUnboundResp.Code != http.StatusNotFound {
		t.Fatalf("operator get unbound key 应隐藏为 404，实际 status=%d body=%s", getUnboundResp.Code, getUnboundResp.Body.String())
	}
}

func TestSSHKeyCreateDuplicateNameReturnsSanitizedConflict(t *testing.T) {
	db := openSSHKeyHandlerTestDB(t)
	secure.ResetForTesting()
	key := buildSSHKeyPrivateKeyForHandlerTest(t)
	router := newSSHKeyHandlerRouter(db, "admin", 1)
	payload := fmt.Sprintf(`{"name":"duplicate-key","username":"root","key_type":"auto","private_key":%q}`, key)

	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ssh-keys", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(first, req)
	if first.Code != http.StatusCreated {
		t.Fatalf("首次创建 SSH key 期望 201，实际 status=%d body=%s", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/ssh-keys", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(second, req)
	if second.Code != http.StatusConflict {
		t.Fatalf("重复 SSH key 名称期望 409，实际 status=%d body=%s", second.Code, second.Body.String())
	}
	var envelope Response
	if err := json.Unmarshal(second.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析重复 SSH key 响应失败: %v", err)
	}
	if envelope.Code != http.StatusConflict || envelope.Message != sshKeyDuplicateMessage {
		t.Fatalf("重复 SSH key 响应不符合安全契约: %+v", envelope)
	}
	for _, forbidden := range []string{"UNIQUE constraint", "ssh_keys", "duplicate-key", "constraint"} {
		if strings.Contains(second.Body.String(), forbidden) {
			t.Fatalf("重复 SSH key 响应泄漏存储细节 %q: %s", forbidden, second.Body.String())
		}
	}
}

func TestSSHKeyBatchCreateEncryptionFailureIsSanitized(t *testing.T) {
	db := openSSHKeyHandlerTestDB(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATA_ENCRYPTION_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	gin.SetMode(gin.TestMode)
	handler := NewSSHKeyHandler(db)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("role", "admin")
		c.Set("userID", uint(1))
		c.Next()
	})
	router.POST("/ssh-keys/batch", handler.BatchCreate)
	key := buildSSHKeyPrivateKeyForHandlerTest(t)
	payload := fmt.Sprintf(`{"keys":[{"name":"encrypted-batch-key","username":"root","key_type":"auto","private_key":%q}]}`, key)
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ssh-keys/batch", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("批量 SSH key 加密失败仍应返回结构化 200，实际 status=%d body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	for _, forbidden := range []string{"必须设置 DATA_ENCRYPTION_KEY", "enc:v2", "cipher", "sql:", "database"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("批量 SSH key 响应泄漏加密/驱动错误 %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, sshKeyPersistenceMessage) || !strings.Contains(body, sshKeyPersistenceCode) {
		t.Fatalf("批量 SSH key 响应应返回通用持久化错误: %s", body)
	}
}

const remoteSSHFailureMarker = "REMOTE_SSH_DISCONNECT_MARKER_5e7c"

func appendSSHUint32(dst []byte, value uint32) []byte {
	return append(dst,
		byte(value>>24),
		byte(value>>16),
		byte(value>>8),
		byte(value),
	)
}

func writeRawSSHDisconnect(conn net.Conn, marker string) error {
	payload := []byte{1}
	payload = appendSSHUint32(payload, 2)
	payload = appendSSHUint32(payload, uint32(len(marker)))
	payload = append(payload, marker...)
	payload = appendSSHUint32(payload, 0)

	paddingLength := 4
	for (1+len(payload)+paddingLength)%8 != 0 {
		paddingLength++
	}
	packetLength := 1 + len(payload) + paddingLength
	packet := make([]byte, 4+packetLength)
	packet = packet[:4]
	packet[0] = byte(packetLength >> 24)
	packet[1] = byte(packetLength >> 16)
	packet[2] = byte(packetLength >> 8)
	packet[3] = byte(packetLength)
	packet = append(packet, byte(paddingLength))
	packet = append(packet, payload...)
	packet = append(packet, make([]byte, paddingLength)...)
	_, err := conn.Write(packet)
	return err
}

func startRawSSHDisconnectServer(t *testing.T, marker string) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听 SSH 测试服务失败: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, readErr := bufio.NewReader(conn).ReadString('\n'); readErr != nil {
			return
		}
		if _, writeErr := conn.Write([]byte("SSH-2.0-xirang-test\r\n")); writeErr != nil {
			return
		}
		_ = writeRawSSHDisconnect(conn, marker)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("SSH 测试服务未及时退出")
		}
	})

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("SSH 测试服务地址类型错误: %T", listener.Addr())
	}
	return tcpAddr.IP.String(), tcpAddr.Port
}

func TestSSHKeyTestConnectionSanitizesRemoteSSHDisconnect(t *testing.T) {
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	db := openSSHKeyHandlerTestDB(t)
	key := seedSSHKeyForVisibility(t, db, "remote-error-key")
	host, port := startRawSSHDisconnectServer(t, remoteSSHFailureMarker)
	node := model.Node{
		Name:      "remote-error-node",
		Host:      host,
		Port:      port,
		Username:  "root",
		AuthType:  "key",
		SSHKeyID:  &key.ID,
		BackupDir: "remote-error-backup",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建 SSH 失败测试节点失败: %v", err)
	}

	router := newSSHKeyHandlerRouter(db, "admin", 1)
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/ssh-keys/%d/test-connection", key.ID),
		strings.NewReader(fmt.Sprintf(`{"node_ids":[%d]}`, node.ID)),
	)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("SSH 连通性失败应返回结构化 200，实际 status=%d body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	if strings.Contains(body, remoteSSHFailureMarker) {
		t.Fatalf("SSH 连通性响应泄漏远端断开文本 %q: %s", remoteSSHFailureMarker, body)
	}
	if !strings.Contains(body, `"success":false`) {
		t.Fatalf("SSH 连通性响应应记录失败结果: %s", body)
	}
}
