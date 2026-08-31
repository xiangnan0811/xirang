package handlers

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSSHKeyHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_32_BYTES_FOR_TEST_ONLY")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.SSHKey{}, &model.Node{}, &model.NodeOwner{}); err != nil {
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
	r.GET("/ssh-keys", handler.List)
	r.POST("/ssh-keys", handler.Create)
	r.GET("/ssh-keys/export", handler.Export)
	r.GET("/ssh-keys/:id", handler.Get)
	r.PUT("/ssh-keys/:id", handler.Update)
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
