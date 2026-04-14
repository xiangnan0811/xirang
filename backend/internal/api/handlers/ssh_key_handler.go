package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type SSHKeyHandler struct {
	db *gorm.DB
}

func NewSSHKeyHandler(db *gorm.DB) *SSHKeyHandler {
	return &SSHKeyHandler{db: db}
}

type sshKeyCreateRequest struct {
	Name       string `json:"name" binding:"required"`
	Username   string `json:"username" binding:"required"`
	KeyType    string `json:"key_type"`
	PrivateKey string `json:"private_key" binding:"required"`
}

type sshKeyUpdateRequest struct {
	Name       string `json:"name" binding:"required"`
	Username   string `json:"username" binding:"required"`
	KeyType    string `json:"key_type"`
	PrivateKey string `json:"private_key"`
}

// sshKeyResponseItem 是 SSH Key API 响应结构，包含派生的公钥，不暴露私钥。
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

// toSSHKeyResponse 将 model.SSHKey 转换为安全的响应结构（含派生公钥，不含私钥）。
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

func generateFingerprint(privateKey string) string {
	sum := sha256.Sum256([]byte(privateKey))
	encoded := base64.StdEncoding.EncodeToString(sum[:])
	return fmt.Sprintf("SHA256:%s", encoded)
}

func normalizeSSHKeyInput(name, username, keyType, privateKey string) (string, string, string, string, error) {
	normalizedName := strings.TrimSpace(name)
	normalizedUsername := strings.TrimSpace(username)
	normalizedType := sshutil.NormalizeKeyType(keyType)
	preparedKey, detectedType, err := sshutil.ValidateAndPreparePrivateKey(privateKey, normalizedType)
	if err != nil {
		return "", "", "", "", err
	}
	storedType := detectedType
	if normalizedType != sshutil.SSHKeyTypeAuto {
		storedType = normalizedType
	}
	return normalizedName, normalizedUsername, storedType, preparedKey, nil
}

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

func (h *SSHKeyHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var item model.SSHKey
	if err := h.db.First(&item, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ssh key 不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": toSSHKeyResponse(item)})
}

func (h *SSHKeyHandler) Create(c *gin.Context) {
	var req sshKeyCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	normalizedName, normalizedUsername, storedKeyType, preparedKey, err := normalizeSSHKeyInput(
		req.Name,
		req.Username,
		req.KeyType,
		req.PrivateKey,
	)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	item := model.SSHKey{
		Name:        normalizedName,
		Username:    normalizedUsername,
		KeyType:     storedKeyType,
		PrivateKey:  preparedKey,
		Fingerprint: generateFingerprint(preparedKey),
	}
	if err := h.db.Create(&item).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": toSSHKeyResponse(item)})
}

func (h *SSHKeyHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req sshKeyUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	var item model.SSHKey
	if err := h.db.First(&item, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ssh key 不存在"})
		return
	}

	normalizedName := strings.TrimSpace(req.Name)
	normalizedUsername := strings.TrimSpace(req.Username)
	normalizedType := sshutil.NormalizeKeyType(req.KeyType)
	if normalizedType == sshutil.SSHKeyTypeAuto {
		normalizedType = sshutil.NormalizeKeyType(item.KeyType)
	}

	item.Name = normalizedName
	item.Username = normalizedUsername

	if req.PrivateKey != "" {
		_, _, storedType, preparedKey, err := normalizeSSHKeyInput(
			normalizedName,
			normalizedUsername,
			normalizedType,
			req.PrivateKey,
		)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		item.KeyType = storedType
		item.PrivateKey = preparedKey
		item.Fingerprint = generateFingerprint(preparedKey)
	} else {
		if item.KeyType == "" {
			item.KeyType = sshutil.SSHKeyTypeAuto
		}
		if normalizedType != sshutil.SSHKeyTypeAuto {
			preparedKey, storedType, err := sshutil.ValidateAndPreparePrivateKey(item.PrivateKey, normalizedType)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			item.KeyType = storedType
			item.PrivateKey = preparedKey
			item.Fingerprint = generateFingerprint(preparedKey)
		}
	}
	if err := h.db.Save(&item).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": toSSHKeyResponse(item)})
}

func (h *SSHKeyHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var count int64
	if err := h.db.Model(&model.Node{}).Where("ssh_key_id = ?", id).Count(&count).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	if count > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该 ssh key 正在被节点使用，无法删除"})
		return
	}

	if err := h.db.Delete(&model.SSHKey{}, id).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted", "deleted_at": time.Now()})
}

// TestConnection 使用指定 SSH Key 对一组节点进行连通性测试。
func (h *SSHKeyHandler) TestConnection(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req struct {
		NodeIDs []uint `json:"node_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}
	if len(req.NodeIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "node_ids 不能为空"})
		return
	}

	var sshKey model.SSHKey
	if err := h.db.First(&sshKey, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ssh key 不存在"})
		return
	}

	var nodes []model.Node
	if err := h.db.Where("id IN ?", req.NodeIDs).Find(&nodes).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	hostKeyCallback, hkErr := sshutil.ResolveSSHHostKeyCallback()
	if hkErr != nil {
		log.Printf("解析 host key callback 失败: %v", hkErr)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法初始化 SSH 主机密钥校验"})
		return
	}

	type testResult struct {
		NodeID    uint   `json:"node_id"`
		Name      string `json:"name"`
		Host      string `json:"host"`
		Port      int    `json:"port"`
		Success   bool   `json:"success"`
		LatencyMs int64  `json:"latency_ms"`
		Error     string `json:"error,omitempty"`
	}

	results := make([]testResult, 0, len(nodes))
	for _, node := range nodes {
		// 构造临时节点，将 SSHKey 指向待测试的密钥
		testNode := node
		testNode.AuthType = "key"
		testNode.SSHKey = &sshKey

		authMethods, err := sshutil.BuildSSHAuth(testNode, h.db)
		if err != nil {
			results = append(results, testResult{
				NodeID:  node.ID,
				Name:    node.Name,
				Host:    node.Host,
				Port:    node.Port,
				Success: false,
				Error:   err.Error(),
			})
			continue
		}

		addr := fmt.Sprintf("%s:%d", node.Host, node.Port)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)

		start := time.Now()
		client, dialErr := sshutil.DialSSH(ctx, addr, node.Username, authMethods, hostKeyCallback)
		latency := time.Since(start).Milliseconds()
		cancel()

		if dialErr != nil {
			results = append(results, testResult{
				NodeID:    node.ID,
				Name:      node.Name,
				Host:      node.Host,
				Port:      node.Port,
				Success:   false,
				LatencyMs: latency,
				Error:     dialErr.Error(),
			})
			continue
		}
		_ = client.Close()

		results = append(results, testResult{
			NodeID:    node.ID,
			Name:      node.Name,
			Host:      node.Host,
			Port:      node.Port,
			Success:   true,
			LatencyMs: latency,
		})
	}

	c.JSON(http.StatusOK, gin.H{"data": results})
}

// BatchCreate 批量创建 SSH Key（单次最多 50 条）。
func (h *SSHKeyHandler) BatchCreate(c *gin.Context) {
	var req struct {
		Keys []sshKeyCreateRequest `json:"keys" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}
	if len(req.Keys) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "keys 不能为空"})
		return
	}
	if len(req.Keys) > 50 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "单次批量创建不能超过 50 条"})
		return
	}

	type batchResult struct {
		Name   string `json:"name"`
		Status string `json:"status"` // created | skipped | error
		Error  string `json:"error,omitempty"`
	}

	results := make([]batchResult, 0, len(req.Keys))
	for _, k := range req.Keys {
		normalizedName, normalizedUsername, storedKeyType, preparedKey, err := normalizeSSHKeyInput(
			k.Name, k.Username, k.KeyType, k.PrivateKey,
		)
		if err != nil {
			results = append(results, batchResult{Name: strings.TrimSpace(k.Name), Status: "error", Error: err.Error()})
			continue
		}

		// 检查名称唯一性
		var exists int64
		h.db.Model(&model.SSHKey{}).Where("name = ?", normalizedName).Count(&exists)
		if exists > 0 {
			results = append(results, batchResult{Name: normalizedName, Status: "skipped", Error: "名称已存在"})
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
			results = append(results, batchResult{Name: normalizedName, Status: "error", Error: err.Error()})
			continue
		}
		results = append(results, batchResult{Name: normalizedName, Status: "created"})
	}

	c.JSON(http.StatusOK, gin.H{"data": results})
}

// Export 导出 SSH Key 列表，支持 authorized_keys / json / csv 格式。
func (h *SSHKeyHandler) Export(c *gin.Context) {
	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "authorized_keys")))
	scope := strings.ToLower(strings.TrimSpace(c.DefaultQuery("scope", "all")))
	idsParam := strings.TrimSpace(c.Query("ids"))

	query := h.db.Model(&model.SSHKey{}).Order("id asc")

	// 按 scope 过滤
	if scope == "in_use" {
		query = query.Where("id IN (?)", h.db.Model(&model.Node{}).Select("DISTINCT ssh_key_id").Where("ssh_key_id IS NOT NULL"))
	}

	// 按 ids 过滤
	if idsParam != "" {
		idStrs := strings.Split(idsParam, ",")
		ids := make([]uint, 0, len(idStrs))
		for _, s := range idStrs {
			if v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64); err == nil {
				ids = append(ids, uint(v))
			}
		}
		if len(ids) > 0 {
			query = query.Where("id IN ?", ids)
		}
	}

	var items []model.SSHKey
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
			// 格式：公钥 + 注释（key 名称）
			lines = append(lines, fmt.Sprintf("%s %s", pub, item.Name))
		}
		c.Header("Content-Disposition", "attachment; filename=authorized_keys")
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(strings.Join(lines, "\n")+"\n"))

	case "json":
		result := make([]sshKeyResponseItem, 0, len(items))
		for _, item := range items {
			result = append(result, toSSHKeyResponse(item))
		}
		c.Header("Content-Disposition", "attachment; filename=ssh_keys.json")
		c.JSON(http.StatusOK, result)

	case "csv":
		c.Header("Content-Disposition", "attachment; filename=ssh_keys.csv")
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Writer.WriteHeader(http.StatusOK)

		w := csv.NewWriter(c.Writer)
		// 写入 BOM 以支持 Excel 正确识别 UTF-8
		_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
		_ = w.Write([]string{"id", "name", "username", "key_type", "fingerprint", "public_key", "created_at", "updated_at"})
		for _, item := range items {
			pub, _ := sshutil.DerivePublicKey(item.PrivateKey)
			_ = w.Write([]string{
				strconv.FormatUint(uint64(item.ID), 10),
				item.Name,
				item.Username,
				item.KeyType,
				item.Fingerprint,
				pub,
				item.CreatedAt.Format(time.RFC3339),
				item.UpdatedAt.Format(time.RFC3339),
			})
		}
		w.Flush()

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的导出格式，可选：authorized_keys / json / csv"})
	}
}

// BatchDelete 批量删除 SSH Key，正在被节点使用的密钥会被跳过。
func (h *SSHKeyHandler) BatchDelete(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids 不能为空"})
		return
	}

	// 查询哪些 key 正在被节点使用
	var usedKeyIDs []uint
	if err := h.db.Model(&model.Node{}).
		Where("ssh_key_id IN ?", req.IDs).
		Distinct("ssh_key_id").
		Pluck("ssh_key_id", &usedKeyIDs).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	usedSet := make(map[uint]bool, len(usedKeyIDs))
	for _, id := range usedKeyIDs {
		usedSet[id] = true
	}

	// 查询被使用 key 的名称（用于返回提示）
	var skippedNames []string
	if len(usedKeyIDs) > 0 {
		var usedKeys []model.SSHKey
		h.db.Where("id IN ?", usedKeyIDs).Select("id", "name").Find(&usedKeys)
		for _, k := range usedKeys {
			skippedNames = append(skippedNames, k.Name)
		}
	}

	// 筛选可删除的 ID
	toDelete := make([]uint, 0)
	for _, id := range req.IDs {
		if !usedSet[id] {
			toDelete = append(toDelete, id)
		}
	}

	deleted := 0
	if len(toDelete) > 0 {
		result := h.db.Where("id IN ?", toDelete).Delete(&model.SSHKey{})
		if result.Error != nil {
			respondInternalError(c, result.Error)
			return
		}
		deleted = int(result.RowsAffected)
	}

	c.JSON(http.StatusOK, gin.H{
		"deleted":        deleted,
		"skipped_in_use": skippedNames,
	})
}
