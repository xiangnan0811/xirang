# SSH Keys 页面重新设计 - 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 重新设计 SSH Keys 页面，采用 Nodes 页面的 UI 框架（表格/卡片双视图、筛选、批量操作、分页），并加入密钥轮换向导、测试连接、批量导入/导出等专属功能。

**Architecture:** 后端新增 3 个 API（test-connection、batch-import、export）+ 公钥派生工具函数。前端重写 ssh-keys-page，拆分为 state hook + 主页面 + table/card/toolbar 子组件 + 5 个功能对话框。复用现有 FilterPanel、Pagination、DropdownMenu、Sheet 等 UI 组件，保留 SSHKeyEditorDialog。

**Tech Stack:** Go 1.24 + Gin (backend), React 18 + TypeScript + Radix UI + Tailwind CSS (frontend)

**Design spec:** `docs/superpowers/specs/2026-04-05-ssh-keys-page-redesign.md`

---

## 文件结构

### 后端新增/修改

| 文件 | 操作 | 职责 |
|------|------|------|
| `backend/internal/sshutil/public_key.go` | 新建 | 从私钥派生公钥的工具函数 |
| `backend/internal/sshutil/public_key_test.go` | 新建 | 公钥派生测试 |
| `backend/internal/api/handlers/ssh_key_handler.go` | 修改 | 新增 TestConnection / BatchCreate / Export handler + 响应中加入 public_key |
| `backend/internal/api/handlers/ssh_key_handler_test.go` | 新建 | 新增 handler 测试 |
| `backend/internal/api/router.go` | 修改 | 注册 3 条新路由 |

### 前端新增/修改

| 文件 | 操作 | 职责 |
|------|------|------|
| `web/src/lib/api/ssh-keys-api.ts` | 修改 | 新增 testConnection / batchCreate / exportKeys / deleteKeys API |
| `web/src/types/domain.ts` | 修改 | SSHKeyRecord 新增 publicKey 字段 |
| `web/src/pages/ssh-keys-page.tsx` | 重写 | 主页面（统计卡片 + 工具栏 + 筛选 + 视图切换 + 分页） |
| `web/src/pages/ssh-keys-page.state.ts` | 新建 | 页面状态 hook（筛选、选择、排序、视图、对话框状态） |
| `web/src/pages/ssh-keys-page.table.tsx` | 新建 | 表格视图组件 |
| `web/src/pages/ssh-keys-page.grid.tsx` | 新建 | 卡片视图组件 |
| `web/src/pages/ssh-keys-page.toolbar.tsx` | 新建 | 工具栏组件 |
| `web/src/components/ssh-key-actions-menu.tsx` | 新建 | 行操作下拉菜单 |
| `web/src/components/ssh-key-test-connection-dialog.tsx` | 新建 | 测试连接对话框 |
| `web/src/components/ssh-key-associated-nodes-sheet.tsx` | 新建 | 关联节点侧滑面板 |
| `web/src/components/ssh-key-batch-import-dialog.tsx` | 新建 | 批量导入对话框 |
| `web/src/components/ssh-key-export-dialog.tsx` | 新建 | 导出公钥对话框 |
| `web/src/components/ssh-key-rotation-wizard.tsx` | 新建 | 密钥轮换多步骤向导 |
| `web/src/i18n/locales/zh.ts` | 修改 | 新增 sshKeys 翻译 key |
| `web/src/i18n/locales/en.ts` | 修改 | 新增 sshKeys 翻译 key |

---

## Task 1: 后端 — 公钥派生工具函数

**Files:**
- Create: `backend/internal/sshutil/public_key.go`
- Create: `backend/internal/sshutil/public_key_test.go`

- [ ] **Step 1: 编写公钥派生测试**

```go
// backend/internal/sshutil/public_key_test.go
package sshutil

import (
	"strings"
	"testing"
)

func TestDerivePublicKey_ED25519(t *testing.T) {
	// 使用 ssh-keygen -t ed25519 生成的测试密钥
	privateKey := `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACBfRkR0hpGG5LwMjKm0W3sDm1ByPE6DGjGf9CWFEDz+UAAAAJB1q4L9da
uC/QAAAAtzc2gtZWQyNTUxOQAAACBfRkR0hpGG5LwMjKm0W3sDm1ByPE6DGjGf9CWFEDz
+UAAAAEDtst4Uw0lYNiZgPQ89mDq5YSvC9J6B3bdIq3i3ky4AF9GRHSGEY bkvAyMqbRbe
wObUHI8ToMaMZ/0JYUQPP5QAAAAA
-----END OPENSSH PRIVATE KEY-----`

	pub, err := DerivePublicKey(privateKey)
	if err != nil {
		t.Fatalf("DerivePublicKey failed: %v", err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Errorf("expected ssh-ed25519 prefix, got: %s", pub[:30])
	}
}

func TestDerivePublicKey_InvalidKey(t *testing.T) {
	_, err := DerivePublicKey("not-a-key")
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
}

func TestDerivePublicKey_EmptyKey(t *testing.T) {
	pub, err := DerivePublicKey("")
	if err != nil {
		t.Fatalf("empty key should return empty string, got error: %v", err)
	}
	if pub != "" {
		t.Errorf("expected empty string, got: %s", pub)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

```bash
cd backend && go test ./internal/sshutil/ -run TestDerivePublicKey -v
```

预期：编译失败 — `DerivePublicKey` 未定义

- [ ] **Step 3: 实现公钥派生函数**

```go
// backend/internal/sshutil/public_key.go
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
	// ssh.MarshalAuthorizedKey 返回 "type base64\n"
	authorizedKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubKey)))
	return authorizedKey, nil
}
```

- [ ] **Step 4: 运行测试验证通过**

```bash
cd backend && go test ./internal/sshutil/ -run TestDerivePublicKey -v
```

预期：全部 PASS

- [ ] **Step 5: 提交**

```bash
cd backend && git add internal/sshutil/public_key.go internal/sshutil/public_key_test.go
git commit -m "feat(backend): add DerivePublicKey utility for SSH public key extraction"
```

---

## Task 2: 后端 — API 响应增加 public_key + 新增 3 个 API endpoint

**Files:**
- Modify: `backend/internal/api/handlers/ssh_key_handler.go`
- Modify: `backend/internal/api/router.go`

- [ ] **Step 1: 修改 sanitizeSSHKey 增加 public_key 派生**

在 `backend/internal/api/handlers/ssh_key_handler.go` 中：

1. 在 `sanitizeSSHKey` 调用前保存原始私钥用于派生公钥
2. 新增响应结构体，增加 `PublicKey` 字段
3. 新增 `sshKeyResponse` 函数处理响应映射

```go
// 在 sanitizeSSHKey 函数之后增加：

type sshKeyResponseItem struct {
	ID          uint       `json:"id"`
	Name        string     `json:"name"`
	Username    string     `json:"username"`
	KeyType     string     `json:"key_type"`
	Fingerprint string     `json:"fingerprint"`
	PublicKey   string     `json:"public_key,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func toSSHKeyResponse(item model.SSHKey) sshKeyResponseItem {
	publicKey, _ := sshutil.DerivePublicKey(item.PrivateKey)
	keyType := item.KeyType
	if strings.TrimSpace(keyType) == "" {
		keyType = sshutil.SSHKeyTypeAuto
	}
	return sshKeyResponseItem{
		ID:          item.ID,
		Name:        item.Name,
		Username:    item.Username,
		KeyType:     keyType,
		Fingerprint: item.Fingerprint,
		PublicKey:   publicKey,
		LastUsedAt:  item.LastUsedAt,
		CreatedAt:   item.CreatedAt,
		UpdatedAt:   item.UpdatedAt,
	}
}
```

然后修改 List、Get、Create、Update handler 中的响应，将 `sanitizeSSHKey(item)` 替换为 `toSSHKeyResponse(item)`。例如 List：

```go
func (h *SSHKeyHandler) List(c *gin.Context) {
	var items []model.SSHKey
	if err := h.db.Order("id asc").Find(&items).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	result := make([]sshKeyResponseItem, 0, len(items))
	for _, one := range items {
		result = append(result, toSSHKeyResponse(one))
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}
```

对 Get、Create、Update 做同样替换：`c.JSON(..., gin.H{"data": toSSHKeyResponse(item)})`。

- [ ] **Step 2: 新增 TestConnection handler**

```go
type testConnectionRequest struct {
	NodeIDs []uint `json:"node_ids" binding:"required"`
}

type testConnectionResult struct {
	NodeID  uint   `json:"node_id"`
	Name    string `json:"name"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Success bool   `json:"success"`
	Latency int64  `json:"latency_ms"`
	Error   string `json:"error,omitempty"`
}

func (h *SSHKeyHandler) TestConnection(c *gin.Context) {
	keyID, ok := parseID(c, "id")
	if !ok {
		return
	}

	var sshKey model.SSHKey
	if err := h.db.First(&sshKey, keyID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ssh key 不存在"})
		return
	}

	var req testConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	var nodes []model.Node
	if err := h.db.Where("id IN ?", req.NodeIDs).Find(&nodes).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	results := make([]testConnectionResult, 0, len(nodes))
	for _, node := range nodes {
		result := testConnectionResult{
			NodeID: node.ID,
			Name:   node.Name,
			Host:   node.Host,
			Port:   node.Port,
		}

		// 构建临时节点用于 SSH 连接测试
		testNode := node
		testNode.SSHKey = &sshKey

		authMethods, err := sshutil.BuildSSHAuth(&testNode, h.db)
		if err != nil {
			result.Error = err.Error()
			results = append(results, result)
			continue
		}

		hostKeyCallback, err := sshutil.ResolveSSHHostKeyCallback()
		if err != nil {
			result.Error = err.Error()
			results = append(results, result)
			continue
		}

		addr := fmt.Sprintf("%s:%d", node.Host, node.Port)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		start := time.Now()
		client, dialErr := sshutil.DialSSH(ctx, addr, sshKey.Username, authMethods, hostKeyCallback)
		elapsed := time.Since(start)
		cancel()

		if dialErr != nil {
			result.Error = dialErr.Error()
			result.Latency = elapsed.Milliseconds()
		} else {
			client.Close()
			result.Success = true
			result.Latency = elapsed.Milliseconds()
		}
		results = append(results, result)
	}

	c.JSON(http.StatusOK, gin.H{"data": results})
}
```

在文件顶部增加 `"context"` 到 import 列表。

- [ ] **Step 3: 新增 BatchCreate handler**

```go
type batchCreateRequest struct {
	Keys []sshKeyCreateRequest `json:"keys" binding:"required"`
}

type batchCreateResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "created" | "skipped" | "error"
	Error  string `json:"error,omitempty"`
	ID     uint   `json:"id,omitempty"`
}

func (h *SSHKeyHandler) BatchCreate(c *gin.Context) {
	var req batchCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	if len(req.Keys) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密钥列表不能为空"})
		return
	}

	if len(req.Keys) > 50 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "单次最多导入 50 个密钥"})
		return
	}

	results := make([]batchCreateResult, 0, len(req.Keys))
	for _, keyReq := range req.Keys {
		result := batchCreateResult{Name: keyReq.Name}

		// 检查重名
		var count int64
		h.db.Model(&model.SSHKey{}).Where("name = ?", strings.TrimSpace(keyReq.Name)).Count(&count)
		if count > 0 {
			result.Status = "skipped"
			result.Error = "名称已存在"
			results = append(results, result)
			continue
		}

		normalizedName, normalizedUsername, storedKeyType, preparedKey, err := normalizeSSHKeyInput(
			keyReq.Name, keyReq.Username, keyReq.KeyType, keyReq.PrivateKey,
		)
		if err != nil {
			result.Status = "error"
			result.Error = err.Error()
			results = append(results, result)
			continue
		}

		item := model.SSHKey{
			Name:        normalizedName,
			Username:    normalizedUsername,
			KeyType:     storedKeyType,
			PrivateKey:  preparedKey,
			Fingerprint: generateFingerprint(preparedKey),
		}
		if err := h.db.Create(&item).Error; err != nil {
			result.Status = "error"
			result.Error = err.Error()
			results = append(results, result)
			continue
		}

		result.Status = "created"
		result.ID = item.ID
		results = append(results, result)
	}

	c.JSON(http.StatusOK, gin.H{"data": results})
}
```

- [ ] **Step 4: 新增 Export handler**

```go
func (h *SSHKeyHandler) Export(c *gin.Context) {
	format := c.DefaultQuery("format", "authorized_keys")
	scope := c.DefaultQuery("scope", "all")
	idsParam := c.Query("ids")

	var items []model.SSHKey
	query := h.db.Order("id asc")

	if idsParam != "" {
		var ids []uint
		for _, idStr := range strings.Split(idsParam, ",") {
			idStr = strings.TrimSpace(idStr)
			if idStr == "" {
				continue
			}
			var id uint
			if _, err := fmt.Sscanf(idStr, "%d", &id); err == nil {
				ids = append(ids, id)
			}
		}
		query = query.Where("id IN ?", ids)
	} else if scope == "in_use" {
		subQuery := h.db.Model(&model.Node{}).Select("DISTINCT ssh_key_id").Where("ssh_key_id IS NOT NULL")
		query = query.Where("id IN (?)", subQuery)
	}

	if err := query.Find(&items).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	switch format {
	case "authorized_keys":
		var lines []string
		for _, item := range items {
			pub, err := sshutil.DerivePublicKey(item.PrivateKey)
			if err != nil || pub == "" {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s %s", pub, item.Name))
		}
		content := strings.Join(lines, "\n") + "\n"
		c.Header("Content-Disposition", "attachment; filename=authorized_keys")
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(content))

	case "json":
		type exportItem struct {
			Name        string `json:"name"`
			Username    string `json:"username"`
			KeyType     string `json:"key_type"`
			PublicKey   string `json:"public_key"`
			Fingerprint string `json:"fingerprint"`
		}
		var exportItems []exportItem
		for _, item := range items {
			pub, _ := sshutil.DerivePublicKey(item.PrivateKey)
			exportItems = append(exportItems, exportItem{
				Name:        item.Name,
				Username:    item.Username,
				KeyType:     item.KeyType,
				PublicKey:   pub,
				Fingerprint: item.Fingerprint,
			})
		}
		c.Header("Content-Disposition", "attachment; filename=ssh-keys.json")
		c.JSON(http.StatusOK, exportItems)

	case "csv":
		var lines []string
		lines = append(lines, "name,username,key_type,fingerprint,public_key")
		for _, item := range items {
			pub, _ := sshutil.DerivePublicKey(item.PrivateKey)
			// CSV 引用处理
			escapedPub := strings.ReplaceAll(pub, "\"", "\"\"")
			lines = append(lines, fmt.Sprintf("%s,%s,%s,%s,\"%s\"",
				item.Name, item.Username, item.KeyType, item.Fingerprint, escapedPub))
		}
		content := strings.Join(lines, "\n") + "\n"
		c.Header("Content-Disposition", "attachment; filename=ssh-keys.csv")
		c.Data(http.StatusOK, "text/csv; charset=utf-8", []byte(content))

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的导出格式"})
	}
}
```

- [ ] **Step 5: 新增 BatchDelete handler**

```go
type batchDeleteRequest struct {
	IDs []uint `json:"ids" binding:"required"`
}

func (h *SSHKeyHandler) BatchDelete(c *gin.Context) {
	var req batchDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	// 检查是否有密钥正在被使用
	var inUseIDs []uint
	h.db.Model(&model.Node{}).
		Select("DISTINCT ssh_key_id").
		Where("ssh_key_id IN ?", req.IDs).
		Pluck("ssh_key_id", &inUseIDs)

	inUseSet := make(map[uint]bool)
	for _, id := range inUseIDs {
		inUseSet[id] = true
	}

	var deleted int
	var skippedNames []string
	for _, id := range req.IDs {
		if inUseSet[id] {
			var key model.SSHKey
			if h.db.First(&key, id).Error == nil {
				skippedNames = append(skippedNames, key.Name)
			}
			continue
		}
		if err := h.db.Delete(&model.SSHKey{}, id).Error; err == nil {
			deleted++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"deleted":       deleted,
		"skipped_in_use": skippedNames,
	})
}
```

- [ ] **Step 6: 注册路由**

在 `backend/internal/api/router.go` 的 ssh-keys 路由部分之后追加：

```go
	secured.POST("/ssh-keys/batch", middleware.RBAC("ssh_keys:write"), sshKeyHandler.BatchCreate)
	secured.POST("/ssh-keys/batch-delete", middleware.RBAC("ssh_keys:write"), sshKeyHandler.BatchDelete)
	secured.POST("/ssh-keys/:id/test-connection", middleware.RBAC("ssh_keys:write"), sshKeyHandler.TestConnection)
	secured.GET("/ssh-keys/export", middleware.RBAC("ssh_keys:read"), sshKeyHandler.Export)
```

注意：`/ssh-keys/batch`、`/ssh-keys/batch-delete`、`/ssh-keys/export` 路由必须注册在 `/ssh-keys/:id` 之前，否则 Gin 会将 `batch`/`export` 当作 `:id` 参数匹配。检查现有顺序，如有需要调整路由注册顺序。

- [ ] **Step 7: 构建验证**

```bash
cd backend && go build ./cmd/server
```

预期：无错误输出

- [ ] **Step 8: 运行全部后端测试**

```bash
cd backend && go test ./... -count=1
```

预期：全部 PASS

- [ ] **Step 9: 提交**

```bash
cd backend && git add internal/api/handlers/ssh_key_handler.go internal/api/router.go
git commit -m "feat(backend): add public_key derivation, test-connection, batch-import, export, batch-delete APIs for SSH keys"
```

---

## Task 3: 前端 — API 客户端扩展 + 类型更新

**Files:**
- Modify: `web/src/lib/api/ssh-keys-api.ts`
- Modify: `web/src/types/domain.ts`

- [ ] **Step 1: 更新 SSHKeyRecord 类型**

在 `web/src/types/domain.ts` 的 `SSHKeyRecord` 接口中新增 `publicKey` 字段：

```typescript
export interface SSHKeyRecord {
  id: string;
  name: string;
  username: string;
  keyType: SSHKeyType;
  privateKey?: string;
  publicKey?: string;        // ← 新增
  fingerprint: string;
  createdAt: string;
  lastUsedAt?: string;
}
```

- [ ] **Step 2: 更新 API 客户端映射和新增方法**

在 `web/src/lib/api/ssh-keys-api.ts` 中：

1. 更新 `SSHKeyResponse` 类型增加 `public_key`
2. 更新 `mapSSHKey` 映射 `publicKey`
3. 新增 4 个 API 方法

```typescript
type SSHKeyResponse = {
  id: number;
  name: string;
  username: string;
  key_type?: "auto" | "rsa" | "ed25519" | "ecdsa";
  private_key?: string;
  public_key?: string;       // ← 新增
  fingerprint: string;
  created_at: string;
  last_used_at?: string | null;
};

function mapSSHKey(row: SSHKeyResponse): SSHKeyRecord {
  return {
    id: `key-${row.id}`,
    name: row.name,
    username: row.username,
    keyType: row.key_type ?? "auto",
    publicKey: row.public_key ?? "",   // ← 新增
    fingerprint: row.fingerprint,
    createdAt: formatTime(row.created_at),
    lastUsedAt: formatTime(row.last_used_at)
  };
}
```

在 `createSSHKeysApi()` 返回对象中追加：

```typescript
    async deleteSSHKeys(token: string, keyIds: string[]): Promise<{ deleted: number; skippedInUse: string[] }> {
      const numericIds = keyIds.map((id) => parseNumericId(id, "key"));
      const payload = await request<Envelope<{ deleted: number; skipped_in_use: string[] }>>("/ssh-keys/batch-delete", {
        method: "POST",
        token,
        body: { ids: numericIds },
      });
      const data = unwrapData(payload);
      return { deleted: data.deleted, skippedInUse: data.skipped_in_use ?? [] };
    },

    async testConnection(token: string, keyId: string, nodeIds: string[]): Promise<TestConnectionResult[]> {
      const numericKeyId = parseNumericId(keyId, "key");
      const numericNodeIds = nodeIds.map((id) => parseNumericId(id, "node"));
      const payload = await request<Envelope<TestConnectionResultRaw[]>>(`/ssh-keys/${numericKeyId}/test-connection`, {
        method: "POST",
        token,
        body: { node_ids: numericNodeIds },
      });
      return (unwrapData(payload) ?? []).map((r) => ({
        nodeId: `node-${r.node_id}`,
        name: r.name,
        host: r.host,
        port: r.port,
        success: r.success,
        latencyMs: r.latency_ms,
        error: r.error,
      }));
    },

    async batchCreate(token: string, keys: NewSSHKeyInput[]): Promise<BatchCreateResult[]> {
      const payload = await request<Envelope<BatchCreateResultRaw[]>>("/ssh-keys/batch", {
        method: "POST",
        token,
        body: {
          keys: keys.map((k) => ({
            name: k.name,
            username: k.username,
            key_type: k.keyType,
            private_key: k.privateKey,
          })),
        },
      });
      return (unwrapData(payload) ?? []).map((r) => ({
        name: r.name,
        status: r.status,
        error: r.error,
      }));
    },

    getExportUrl(format: "authorized_keys" | "json" | "csv", scope: "all" | "in_use", ids?: string[]): string {
      const params = new URLSearchParams({ format, scope });
      if (ids?.length) {
        const numericIds = ids.map((id) => parseNumericId(id, "key"));
        params.set("ids", numericIds.join(","));
      }
      return `/api/v1/ssh-keys/export?${params.toString()}`;
    },
```

在文件顶部新增类型定义：

```typescript
type TestConnectionResultRaw = {
  node_id: number;
  name: string;
  host: string;
  port: number;
  success: boolean;
  latency_ms: number;
  error?: string;
};

export type TestConnectionResult = {
  nodeId: string;
  name: string;
  host: string;
  port: number;
  success: boolean;
  latencyMs: number;
  error?: string;
};

type BatchCreateResultRaw = {
  name: string;
  status: "created" | "skipped" | "error";
  error?: string;
};

export type BatchCreateResult = {
  name: string;
  status: "created" | "skipped" | "error";
  error?: string;
};
```

- [ ] **Step 3: 构建验证**

```bash
cd web && npx tsc --noEmit
```

预期：无类型错误

- [ ] **Step 4: 提交**

```bash
git add web/src/lib/api/ssh-keys-api.ts web/src/types/domain.ts
git commit -m "feat(web): extend SSH keys API client with public key, test-connection, batch, export"
```

---

## Task 4: 前端 — 页面状态 Hook

**Files:**
- Create: `web/src/pages/ssh-keys-page.state.ts`

- [ ] **Step 1: 创建页面状态 Hook**

参照 `web/src/pages/nodes-page.state.ts` 的模式，创建 SSH Keys 页面的状态管理 Hook。

```typescript
// web/src/pages/ssh-keys-page.state.ts
import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useOutletContext } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import type { SSHKeyRecord, NewSSHKeyInput, NodeRecord } from "@/types/domain";
import { usePageFilters } from "@/hooks/use-page-filters";
import { useClientPagination } from "@/hooks/use-client-pagination";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { useConfirm } from "@/hooks/use-confirm";
import { toast } from "@/components/ui/toast";
import { getErrorMessage } from "@/lib/utils";

export type ViewMode = "table" | "cards";

const FILTER_CONFIG = {
  keyword: { key: "xirang.sshkeys.keyword", default: "" },
  keyType: { key: "xirang.sshkeys.keyType", default: "all" },
  usageStatus: { key: "xirang.sshkeys.usage", default: "all" },
  sortBy: { key: "xirang.sshkeys.sort", default: "name-asc" },
} as const;

export function useSSHKeysPageState() {
  const { t } = useTranslation();
  const ctx = useOutletContext<ConsoleOutletContext>();
  const {
    sshKeys, nodes, createSSHKey, updateSSHKey, deleteSSHKey,
    refreshSSHKeys, refreshNodes,
  } = ctx;

  // ─── Filters ───
  const filters = usePageFilters(FILTER_CONFIG);

  // ─── View mode ───
  const [viewMode, setViewMode] = usePersistentState<ViewMode>(
    "xirang.sshkeys.view", "table"
  );

  // ─── Selection ───
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());

  const toggleSelection = useCallback((id: string, checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);

  const clearSelection = useCallback(() => setSelectedIds(new Set()), []);

  // ─── Computed: key-node usage map ───
  const keyUsageMap = useMemo(() => {
    const map = new Map<string, NodeRecord[]>();
    nodes.forEach((node) => {
      if (!node.keyId) return;
      const existing = map.get(node.keyId) ?? [];
      existing.push(node);
      map.set(node.keyId, existing);
    });
    return map;
  }, [nodes]);

  // ─── Computed: stats ───
  const stats = useMemo(() => {
    let inUse = 0;
    let unused = 0;
    let totalNodes = 0;
    for (const key of sshKeys) {
      const nodeCount = keyUsageMap.get(key.id)?.length ?? 0;
      if (nodeCount > 0) {
        inUse++;
        totalNodes += nodeCount;
      } else {
        unused++;
      }
    }
    return { total: sshKeys.length, inUse, unused, totalNodes };
  }, [sshKeys, keyUsageMap]);

  // ─── Computed: filtered + sorted ───
  const filteredKeys = useMemo(() => {
    let result = [...sshKeys];

    // keyword filter
    const kw = filters.deferredKeyword.toLowerCase();
    if (kw) {
      result = result.filter((k) =>
        k.name.toLowerCase().includes(kw) ||
        k.username.toLowerCase().includes(kw) ||
        k.fingerprint.toLowerCase().includes(kw)
      );
    }

    // key type filter
    if (filters.keyType !== "all") {
      result = result.filter((k) => k.keyType === filters.keyType);
    }

    // usage status filter
    if (filters.usageStatus === "in_use") {
      result = result.filter((k) => (keyUsageMap.get(k.id)?.length ?? 0) > 0);
    } else if (filters.usageStatus === "unused") {
      result = result.filter((k) => (keyUsageMap.get(k.id)?.length ?? 0) === 0);
    }

    // sort
    switch (filters.sortBy) {
      case "name-asc":
        result.sort((a, b) => a.name.localeCompare(b.name));
        break;
      case "name-desc":
        result.sort((a, b) => b.name.localeCompare(a.name));
        break;
      case "created":
        result.sort((a, b) => b.createdAt.localeCompare(a.createdAt));
        break;
      case "last-used":
        result.sort((a, b) => (b.lastUsedAt ?? "").localeCompare(a.lastUsedAt ?? ""));
        break;
    }

    return result;
  }, [sshKeys, filters, keyUsageMap]);

  // ─── Pagination ───
  const pagination = useClientPagination(filteredKeys);

  // ─── Select all visible ───
  const allVisibleSelected = useMemo(
    () => pagination.pagedItems.length > 0 && pagination.pagedItems.every((k) => selectedIds.has(k.id)),
    [pagination.pagedItems, selectedIds]
  );

  const toggleSelectAllVisible = useCallback((checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      for (const item of pagination.pagedItems) {
        if (checked) next.add(item.id);
        else next.delete(item.id);
      }
      return next;
    });
  }, [pagination.pagedItems]);

  // ─── Dialogs ───
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingKey, setEditingKey] = useState<SSHKeyRecord | null>(null);
  const [testConnectionKey, setTestConnectionKey] = useState<SSHKeyRecord | null>(null);
  const [associatedNodesKey, setAssociatedNodesKey] = useState<SSHKeyRecord | null>(null);
  const [rotationKey, setRotationKey] = useState<SSHKeyRecord | null>(null);
  const [rotationOpen, setRotationOpen] = useState(false);
  const [batchImportOpen, setBatchImportOpen] = useState(false);
  const [exportOpen, setExportOpen] = useState(false);

  const { confirm, dialog } = useConfirm();

  // ─── Handlers ───
  const openCreateDialog = useCallback(() => {
    setEditingKey(null);
    setEditorOpen(true);
  }, []);

  const openEditDialog = useCallback((key: SSHKeyRecord) => {
    setEditingKey(key);
    setEditorOpen(true);
  }, []);

  const handleSave = useCallback(async (draft: { id?: string; name: string; username: string; keyType: string; privateKey: string }) => {
    const name = draft.name.trim();
    const username = draft.username.trim();
    const privateKey = draft.privateKey.trim();

    if (!name || !username) {
      toast.error(t("sshKeys.errorNameRequired"));
      return;
    }
    if (!draft.id && !privateKey) {
      toast.error(t("sshKeys.errorPrivateKeyRequired"));
      return;
    }

    const input: NewSSHKeyInput = { name, username, keyType: draft.keyType as NewSSHKeyInput["keyType"], privateKey };

    try {
      if (draft.id) {
        await updateSSHKey(draft.id, input);
        toast.success(t("sshKeys.keyUpdated", { name }));
      } else {
        await createSSHKey(input);
        toast.success(t("sshKeys.keyCreated", { name }));
      }
      setEditorOpen(false);
      setEditingKey(null);
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  }, [createSSHKey, updateSSHKey, t]);

  const handleDelete = useCallback(async (key: SSHKeyRecord) => {
    const ok = await confirm({
      title: t("common.confirmAction"),
      description: t("sshKeys.confirmDeleteDesc", { name: key.name }),
    });
    if (!ok) return;

    const success = await deleteSSHKey(key.id);
    if (!success) {
      toast.error(t("sshKeys.deleteFailedInUse", { name: key.name }));
      return;
    }
    toast.success(t("sshKeys.keyDeleted", { name: key.name }));
    setSelectedIds((prev) => { const next = new Set(prev); next.delete(key.id); return next; });
  }, [confirm, deleteSSHKey, t]);

  const openRotationWizard = useCallback((key?: SSHKeyRecord) => {
    setRotationKey(key ?? null);
    setRotationOpen(true);
  }, []);

  return {
    // data
    sshKeys, nodes, keyUsageMap, stats,
    filteredKeys, ...pagination,
    // filters
    filters,
    // view
    viewMode, setViewMode,
    // selection
    selectedIds, toggleSelection, toggleSelectAllVisible, allVisibleSelected, clearSelection,
    // dialogs
    editorOpen, setEditorOpen, editingKey, setEditingKey,
    testConnectionKey, setTestConnectionKey,
    associatedNodesKey, setAssociatedNodesKey,
    rotationKey, rotationOpen, setRotationOpen, openRotationWizard,
    batchImportOpen, setBatchImportOpen,
    exportOpen, setExportOpen,
    // handlers
    openCreateDialog, openEditDialog, handleSave, handleDelete,
    refreshSSHKeys, refreshNodes,
    // confirm dialog
    confirm, dialog,
  };
}
```

- [ ] **Step 2: 类型检查**

```bash
cd web && npx tsc --noEmit
```

预期：无错误

- [ ] **Step 3: 提交**

```bash
git add web/src/pages/ssh-keys-page.state.ts
git commit -m "feat(web): add useSSHKeysPageState hook for SSH Keys page redesign"
```

---

## Task 5: 前端 — 行操作菜单组件

**Files:**
- Create: `web/src/components/ssh-key-actions-menu.tsx`

- [ ] **Step 1: 创建行操作菜单**

```typescript
// web/src/components/ssh-key-actions-menu.tsx
import { useTranslation } from "react-i18next";
import {
  Copy, Monitor, Pencil, Plug, RefreshCw, Trash2,
} from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { toast } from "@/components/ui/toast";
import type { SSHKeyRecord } from "@/types/domain";

interface SSHKeyActionsMenuProps {
  sshKey: SSHKeyRecord;
  nodeCount: number;
  onEdit: (key: SSHKeyRecord) => void;
  onDelete: (key: SSHKeyRecord) => void;
  onTestConnection: (key: SSHKeyRecord) => void;
  onViewAssociatedNodes: (key: SSHKeyRecord) => void;
  onRotate: (key: SSHKeyRecord) => void;
}

export function SSHKeyActionsMenu({
  sshKey,
  nodeCount,
  onEdit,
  onDelete,
  onTestConnection,
  onViewAssociatedNodes,
  onRotate,
}: SSHKeyActionsMenuProps) {
  const { t } = useTranslation();

  const handleCopyPublicKey = async () => {
    if (!sshKey.publicKey) {
      toast.error(t("sshKeys.noPublicKey"));
      return;
    }
    try {
      await navigator.clipboard.writeText(sshKey.publicKey);
      toast.success(t("sshKeys.publicKeyCopied"));
    } catch {
      toast.error(t("sshKeys.copyFailed"));
    }
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="size-8 text-muted-foreground"
          aria-label={t("common.actions")}
        >
          <span className="text-lg">⋯</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-48">
        <DropdownMenuItem onClick={() => onEdit(sshKey)}>
          <Pencil className="mr-2 size-4" />
          {t("common.edit")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={handleCopyPublicKey}>
          <Copy className="mr-2 size-4" />
          {t("sshKeys.copyPublicKey")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => onTestConnection(sshKey)}>
          <Plug className="mr-2 size-4" />
          {t("sshKeys.testConnection")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => onViewAssociatedNodes(sshKey)}>
          <Monitor className="mr-2 size-4" />
          {t("sshKeys.viewAssociatedNodes")}
          {nodeCount > 0 && (
            <Badge variant="success" className="ml-auto">
              {nodeCount}
            </Badge>
          )}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => onRotate(sshKey)}>
          <RefreshCw className="mr-2 size-4" />
          {t("sshKeys.rotateKey")}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          className="text-destructive focus:text-destructive"
          onClick={() => onDelete(sshKey)}
        >
          <Trash2 className="mr-2 size-4" />
          {t("common.delete")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
```

- [ ] **Step 2: 类型检查**

```bash
cd web && npx tsc --noEmit
```

- [ ] **Step 3: 提交**

```bash
git add web/src/components/ssh-key-actions-menu.tsx
git commit -m "feat(web): add SSH key actions dropdown menu component"
```

---

## Task 6: 前端 — 表格视图 + 卡片视图 + 工具栏

**Files:**
- Create: `web/src/pages/ssh-keys-page.table.tsx`
- Create: `web/src/pages/ssh-keys-page.grid.tsx`
- Create: `web/src/pages/ssh-keys-page.toolbar.tsx`

- [ ] **Step 1: 创建表格视图组件**

参照 `nodes-page.table.tsx` 模式。文件：`web/src/pages/ssh-keys-page.table.tsx`

关键结构：
- `hidden md:block` 仅桌面显示
- 表头：复选框（全选）、名称、用户名、类型、指纹、最后使用、关联节点、操作
- 每行：复选框、KeyRound 图标 + 名称（加粗）、用户名（muted）、类型 Badge、指纹（monospace 截断）、相对时间/从未使用（斜体灰色）、节点数 Badge、SSHKeyActionsMenu
- 未使用密钥行 `opacity-60`
- 空状态使用 `FilteredEmptyState`

Props 类型：从 state hook 透传筛选后的分页数据 + 选择状态 + 操作回调。

- [ ] **Step 2: 创建卡片视图组件**

参照 `nodes-page.grid.tsx` 模式。文件：`web/src/pages/ssh-keys-page.grid.tsx`

关键结构：
- 响应式网格：`grid gap-3 md:grid-cols-2 lg:grid-cols-3`
- 每张卡片：`interactive-surface p-3`
  - 顶部：复选框 + KeyRound 图标 + 名称 + 用户名 + 类型 Badge + ⋯ 菜单
  - 中部：2 列 grid 展示指纹 + 最后使用
  - 底部：使用状态 Badge + 快捷按钮（复制公钥、测试连接）
- 未使用密钥 `opacity-60`

- [ ] **Step 3: 创建工具栏组件**

参照 `nodes-page.toolbar.tsx` 模式。文件：`web/src/pages/ssh-keys-page.toolbar.tsx`

关键结构：
- 左侧：新建密钥（主按钮）、批量导入、导出公钥、密钥轮换
- 选中时显示：批量删除按钮 + 已选数量提示
- 右侧：表格/卡片视图切换

- [ ] **Step 4: 类型检查**

```bash
cd web && npx tsc --noEmit
```

- [ ] **Step 5: 提交**

```bash
git add web/src/pages/ssh-keys-page.table.tsx web/src/pages/ssh-keys-page.grid.tsx web/src/pages/ssh-keys-page.toolbar.tsx
git commit -m "feat(web): add SSH keys table, card grid, and toolbar components"
```

---

## Task 7: 前端 — 主页面重写

**Files:**
- Rewrite: `web/src/pages/ssh-keys-page.tsx`

- [ ] **Step 1: 重写 SSH Keys 主页面**

参照 `nodes-page.tsx` 的整体布局组合。组合以下结构：

1. `useSSHKeysPageState()` 获取所有状态
2. `useEffect` 调用 `refreshSSHKeys()` + `refreshNodes()`
3. 统计卡片区（4 张：总计/在用/未用/关联节点）
4. `Card > CardContent` 容器
5. `SSHKeysPageToolbar`
6. `FilterPanel`（SearchInput + AppSelect × 3）
7. `FilterSummary`
8. 条件渲染 `SSHKeysGrid`（卡片视图）或 `SSHKeysTable`（表格视图）
9. `Pagination`
10. 功能对话框：SSHKeyEditorDialog、TestConnectionDialog、AssociatedNodesSheet、RotationWizard、BatchImportDialog、ExportDialog
11. `{dialog}` 渲染确认弹窗

- [ ] **Step 2: 类型检查 + 构建**

```bash
cd web && npm run check
```

预期：typecheck + test + build 全部通过

- [ ] **Step 3: 提交**

```bash
git add web/src/pages/ssh-keys-page.tsx
git commit -m "feat(web): rewrite SSH Keys page with table/card dual view, filtering, bulk selection"
```

---

## Task 8: 前端 — 测试连接对话框

**Files:**
- Create: `web/src/components/ssh-key-test-connection-dialog.tsx`

- [ ] **Step 1: 创建测试连接对话框**

关键实现：
- Props：`open: boolean`、`onOpenChange`、`sshKey: SSHKeyRecord | null`、`associatedNodes: NodeRecord[]`
- 节点多选列表（默认全选关联节点）
- "开始测试"按钮，调用 `apiClient.testConnection(token, keyId, selectedNodeIds)`
- 逐节点显示结果：✓ 成功 + 延迟 / ✗ 失败 + 错误信息
- "重新测试"按钮
- loading 状态管理

使用 `Dialog` + `DialogContent` 组件。结果列表用 `border rounded-lg` 容器 + 逐行展示。

- [ ] **Step 2: 类型检查**

```bash
cd web && npx tsc --noEmit
```

- [ ] **Step 3: 提交**

```bash
git add web/src/components/ssh-key-test-connection-dialog.tsx
git commit -m "feat(web): add SSH key test connection dialog"
```

---

## Task 9: 前端 — 关联节点侧滑面板

**Files:**
- Create: `web/src/components/ssh-key-associated-nodes-sheet.tsx`

- [ ] **Step 1: 创建关联节点 Sheet**

关键实现：
- 使用 `Sheet` / `SheetContent` 组件（Radix UI），从右侧滑入
- Props：`open: boolean`、`onOpenChange`、`sshKey: SSHKeyRecord | null`、`nodes: NodeRecord[]`
- 标题：密钥名称 + 节点数量
- 节点列表：名称（可点击，使用 `Link` 跳转到 `/app/nodes`）、IP:Port、在线状态指示灯
- 空状态处理

检查项目中是否已有 Sheet 组件：

```bash
ls web/src/components/ui/sheet*
```

如果不存在，需要先用 Radix Dialog 包装一个简单的 Sheet 组件，或用 Dialog + side position 模拟。

- [ ] **Step 2: 类型检查**

```bash
cd web && npx tsc --noEmit
```

- [ ] **Step 3: 提交**

```bash
git add web/src/components/ssh-key-associated-nodes-sheet.tsx
git commit -m "feat(web): add SSH key associated nodes sheet panel"
```

---

## Task 10: 前端 — 批量导入对话框

**Files:**
- Create: `web/src/components/ssh-key-batch-import-dialog.tsx`

- [ ] **Step 1: 创建批量导入对话框**

关键实现：
- 拖拽上传区域（`onDrop` + `onDragOver`）或文件选择（`<input type="file" accept=".json">`）
- 解析 JSON 后客户端预校验：
  - 必填字段检查（name、username、privateKey）
  - 与现有 `sshKeys` 列表比对检测重名
  - 逐项显示状态：✓ 有效 / ⚠ 名称已存在 / ✗ 缺少必填字段
- JSON 格式提示区（代码样例）
- "导入 N 个有效密钥"按钮，调用 `apiClient.batchCreate(token, validKeys)`
- 导入完成后 toast + `refreshSSHKeys()`
- 状态：idle → parsing → preview → importing → done

- [ ] **Step 2: 类型检查**

```bash
cd web && npx tsc --noEmit
```

- [ ] **Step 3: 提交**

```bash
git add web/src/components/ssh-key-batch-import-dialog.tsx
git commit -m "feat(web): add SSH key batch import dialog with JSON validation"
```

---

## Task 11: 前端 — 导出公钥对话框

**Files:**
- Create: `web/src/components/ssh-key-export-dialog.tsx`

- [ ] **Step 1: 创建导出对话框**

关键实现：
- 格式选择：3 个 radio 卡片（authorized_keys / JSON / CSV），默认 authorized_keys
- 范围选择：3 个 radio（全部 / 仅在用 / 当前选中），当前选中仅在有选择时可用
- 预览区：根据格式和范围，从前端 `sshKeys` 数据生成前几行预览（不调用 API）
- "下载文件"按钮：构建 URL 使用 `apiClient.getExportUrl(format, scope, ids)` + 使用 `window.open()` 或 `<a download>` 触发下载
- 需要在请求中附带 token，使用 fetch + blob 下载方式：

```typescript
const url = apiClient.getExportUrl(format, scope, selectedKeyIds);
const response = await fetch(url, { headers: { Authorization: `Bearer ${token}` } });
const blob = await response.blob();
const a = document.createElement("a");
a.href = URL.createObjectURL(blob);
a.download = filename;
a.click();
```

- [ ] **Step 2: 类型检查**

```bash
cd web && npx tsc --noEmit
```

- [ ] **Step 3: 提交**

```bash
git add web/src/components/ssh-key-export-dialog.tsx
git commit -m "feat(web): add SSH key export dialog with format and scope selection"
```

---

## Task 12: 前端 — 密钥轮换向导

**Files:**
- Create: `web/src/components/ssh-key-rotation-wizard.tsx`

- [ ] **Step 1: 创建轮换向导**

这是最复杂的组件，使用多步骤对话框模式。

关键实现：
- 4 步状态机：`step: 1 | 2 | 3 | 4`
- Props：`open`、`onOpenChange`、`sshKeys`（密钥列表）、`keyUsageMap`（密钥→节点映射）、`preselectedKey?: SSHKeyRecord`（从行菜单进入时预选）
- preselectedKey 不为空时初始 step = 2

**Step 1 — 选择密钥：**
- 仅展示有关联节点的密钥（`keyUsageMap.get(key.id)?.length > 0`）
- 单选列表，显示密钥名称 + 用户名 + 类型 + 节点数
- "下一步"按钮

**Step 2 — 上传新密钥：**
- 复用 SSHKeyEditorDialog 的表单逻辑（密钥名称预填、类型选择、私钥输入/文件上传）
- 但不复用组件本身，内联表单字段
- "上一步" + "下一步"

**Step 3 — 确认影响：**
- InlineAlert 警告横幅
- 受影响节点列表（名称 + 在线状态）
- 新旧指纹对比（旧指纹取自选中密钥，新指纹需后端返回 — 先调用前端指纹计算或在执行后获取）
- "确认轮换"按钮（amber 色）

**Step 4 — 执行结果：**
- 调用 `updateSSHKey(selectedKey.id, { name, username, keyType, privateKey })`
- 成功后调用 `testConnection(selectedKey.id, onlineNodeIds)` 验证连通性
- 展示逐节点验证结果
- "完成"按钮，关闭对话框 + `refreshSSHKeys()`

- [ ] **Step 2: 类型检查**

```bash
cd web && npx tsc --noEmit
```

- [ ] **Step 3: 提交**

```bash
git add web/src/components/ssh-key-rotation-wizard.tsx
git commit -m "feat(web): add SSH key rotation wizard with 4-step flow"
```

---

## Task 13: i18n — 翻译键

**Files:**
- Modify: `web/src/i18n/locales/zh.ts`
- Modify: `web/src/i18n/locales/en.ts`

- [ ] **Step 1: 扩展 sshKeys 翻译**

在 `zh.ts` 的 `sshKeys` 对象中追加以下键（保留所有已有键）：

```typescript
    // 页面标题和统计
    total: "总计",
    inUseCount: "在用",
    unusedCount: "未用",
    associatedNodes: "关联节点",

    // 工具栏
    batchImport: "批量导入",
    exportPublicKeys: "导出公钥",
    rotateKeys: "密钥轮换",
    batchDelete: "批量删除",
    selectedCount: "已选 {{count}} 个",

    // 筛选
    allTypes: "所有类型",
    allStatus: "所有状态",
    sortByName: "按名称排序",
    sortByNameDesc: "按名称倒序",
    sortByCreated: "按创建时间",
    sortByLastUsed: "按最后使用",

    // 行操作
    copyPublicKey: "复制公钥",
    publicKeyCopied: "公钥已复制到剪贴板",
    noPublicKey: "无法获取公钥",
    copyFailed: "复制失败",
    testConnection: "测试连接",
    viewAssociatedNodes: "查看关联节点",
    rotateKey: "轮换密钥",

    // 测试连接
    testConnectionTitle: "测试 SSH 连接",
    testConnectionDesc: "使用 {{name}} 测试与节点的连通性",
    selectTestNodes: "选择测试节点",
    startTest: "开始测试",
    retest: "重新测试",
    connectionSuccess: "连接成功",
    connectionFailed: "连接失败",
    testHint: "仅测试 SSH 握手和密钥认证，不执行任何命令",

    // 关联节点
    associatedNodesTitle: "关联节点",
    associatedNodesDesc: "{{name}} · {{count}} 个节点",
    clickToNavigate: "点击节点名称可跳转到节点详情",
    noAssociatedNodes: "此密钥未被任何节点使用",

    // 批量导入
    batchImportTitle: "批量导入密钥",
    batchImportDesc: "从 JSON 文件批量导入多个 SSH 密钥",
    dropOrUpload: "拖拽文件到此处或点击上传",
    jsonFormatOnly: "支持 .json 格式",
    jsonFormatHint: "JSON 格式示例：",
    previewTitle: "预览（{{count}} 个密钥待导入）：",
    validKey: "有效",
    nameExists: "名称已存在",
    formatError: "格式错误",
    importValidKeys: "导入 {{count}} 个有效密钥",
    importSuccess: "成功导入 {{count}} 个密钥",

    // 导出
    exportTitle: "导出公钥列表",
    exportDesc: "导出所有密钥的公钥信息，便于部署到服务器",
    exportFormat: "导出格式",
    exportScope: "导出范围",
    formatAuthorizedKeys: "authorized_keys",
    formatAuthorizedKeysDesc: "标准 SSH 格式",
    formatJSON: "JSON",
    formatJSONDesc: "结构化数据",
    formatCSV: "CSV",
    formatCSVDesc: "表格格式",
    scopeAll: "所有密钥（{{count}} 个）",
    scopeInUse: "仅在用密钥（{{count}} 个）",
    scopeSelected: "当前选中（{{count}} 个）",
    downloadFile: "下载文件",

    // 轮换向导
    rotationTitle: "密钥轮换",
    rotationStep1: "选择密钥",
    rotationStep2: "上传新密钥",
    rotationStep3: "确认影响",
    rotationStep4: "执行结果",
    rotationSelectKey: "选择要轮换的密钥",
    rotationSelectKeyDesc: "选择一个当前在用的密钥进行轮换替换",
    rotationUploadKey: "上传替换密钥",
    rotationUploadKeyDesc: "为 {{name}} 提供新的私钥",
    rotationConfirmTitle: "确认轮换影响",
    rotationWarning: "以下 {{count}} 个节点将使用新密钥，请确保新密钥已部署到目标服务器",
    rotationAffectedNodes: "受影响的节点",
    rotationOldFingerprint: "旧密钥指纹",
    rotationNewFingerprint: "新密钥指纹",
    rotationConfirm: "确认轮换",
    rotationComplete: "轮换完成",
    rotationSuccess: "密钥 {{name}} 已成功更新",
    rotationVerifyResults: "连通性验证结果",
    rotationVerified: "验证通过",
    rotationSkipped: "跳过（离线）",
    rotationFailed: "验证失败",
    rotationOfflineHint: "{{count}} 个离线节点已跳过，上线后需手动验证密钥配置",
    rotationDone: "完成",
    rotationNext: "下一步",
    rotationPrev: "上一步",
    rotationCancel: "取消",

    // 表格表头
    colName: "名称",
    colUsername: "用户名",
    colType: "类型",
    colFingerprint: "指纹",
    colLastUsed: "最后使用",
    colNodes: "节点数",
    colActions: "操作",
    neverUsed: "从未使用",

    // 批量删除
    batchDeleteConfirm: "确认删除 {{count}} 个 SSH Key？在用的密钥将被跳过。",
    batchDeleteSuccess: "成功删除 {{deleted}} 个密钥",
    batchDeleteSkipped: "{{count}} 个在用密钥已跳过：{{names}}",
```

对 `en.ts` 做对应的英文翻译。

- [ ] **Step 2: 类型检查**

```bash
cd web && npx tsc --noEmit
```

- [ ] **Step 3: 提交**

```bash
git add web/src/i18n/locales/zh.ts web/src/i18n/locales/en.ts
git commit -m "feat(web): add i18n translation keys for SSH Keys page redesign"
```

---

## Task 14: 集成测试 + 全量验证

**Files:**
- All modified files

- [ ] **Step 1: 后端全量测试**

```bash
cd backend && go build ./cmd/server && go test ./... -count=1
```

预期：编译通过 + 全部 PASS

- [ ] **Step 2: 前端全量检查**

```bash
cd web && npm run check
```

预期：typecheck + vitest + vite build 全部通过

- [ ] **Step 3: 手动冒烟测试**

启动开发环境验证核心功能：

```bash
# 终端 1
make backend-run

# 终端 2
make web-dev
```

验证清单：
- [ ] SSH Keys 页面加载，统计卡片显示正确
- [ ] 表格/卡片视图切换正常
- [ ] 搜索筛选功能正常
- [ ] 新建/编辑/删除密钥正常
- [ ] 复制公钥功能正常
- [ ] 批量选择 + 批量删除正常
- [ ] 测试连接对话框正常
- [ ] 关联节点面板正常
- [ ] 批量导入正常
- [ ] 导出公钥正常
- [ ] 密钥轮换向导正常（工具栏入口 + 行菜单入口）
- [ ] 分页正常
- [ ] 移动端响应式布局正常

- [ ] **Step 4: 最终提交（如有遗漏修复）**

```bash
git add -A
git commit -m "fix(web): SSH Keys page integration fixes"
```
