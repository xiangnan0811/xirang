package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSSHKeyHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_32_BYTES_FOR_TEST_ONLY")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.SSHKey{}, &model.Node{}, &model.NodeOwner{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	return db
}

func seedSSHKeyForVisibility(t *testing.T, db *gorm.DB, name string) model.SSHKey {
	t.Helper()
	key := model.SSHKey{
		Name:        name,
		Username:    "root",
		KeyType:     "auto",
		PrivateKey:  "FAKE_SSH_PRIVATE_KEY_" + name + "_FOR_TEST_ONLY",
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
