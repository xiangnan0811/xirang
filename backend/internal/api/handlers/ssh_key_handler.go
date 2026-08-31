package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
)

type SSHKeyHandler struct {
	db *gorm.DB
}

func NewSSHKeyHandler(db *gorm.DB) *SSHKeyHandler {
	return &SSHKeyHandler{db: db}
}

// isSSHKeyDuplicateError classifies driver-level unique violations without
// exposing their SQL, table, or constraint details to API clients.
func isSSHKeyDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code == sqlite3.ErrConstraint && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func logSSHKeyPersistenceError(operation, name string, err error) {
	logger.Module("api").Error().
		Err(err).
		Str("operation", operation).
		Str("ssh_key_name", name).
		Msg("SSH key persistence failed")
}

const (
	sshKeyDuplicateMessage    = "名称已存在"
	sshKeyPersistenceMessage  = "服务器内部错误"
	sshKeyValidationErrorCode = "validation_error"
	sshKeyDuplicateErrorCode  = "duplicate_name"
	sshKeyPersistenceCode     = "persistence_error"
)

type sshKeyScopeRequest struct {
	Disabled        bool       `json:"disabled"`
	ExpiresAt       *time.Time `json:"expires_at"`
	AllowedPurposes string     `json:"allowed_purposes"`
	AllowedNodeIDs  string     `json:"allowed_node_ids"`
	AllowedNodeTags string     `json:"allowed_node_tags"`
}

type sshKeyScopePatchRequest struct {
	Disabled        *bool      `json:"disabled"`
	ExpiresAt       *time.Time `json:"expires_at"`
	ExpiresAtSet    bool       `json:"-"`
	AllowedPurposes *string    `json:"allowed_purposes"`
	AllowedNodeIDs  *string    `json:"allowed_node_ids"`
	AllowedNodeTags *string    `json:"allowed_node_tags"`
}

type sshKeyCreateRequest struct {
	Name       string `json:"name" binding:"required"`
	Username   string `json:"username" binding:"required"`
	KeyType    string `json:"key_type"`
	PrivateKey string `json:"private_key" binding:"required"`
	sshKeyScopeRequest
}

type sshKeyUpdateRequest struct {
	Name       string `json:"name" binding:"required"`
	Username   string `json:"username" binding:"required"`
	KeyType    string `json:"key_type"`
	PrivateKey string `json:"private_key"`
	sshKeyScopePatchRequest
}

func (r *sshKeyUpdateRequest) UnmarshalJSON(data []byte) error {
	type alias sshKeyUpdateRequest
	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, ok := fields["expires_at"]; ok {
		raw.ExpiresAtSet = true
	}
	*r = sshKeyUpdateRequest(raw)
	return nil
}

// sshKeyResponseItem 是 SSH Key API 响应结构，包含派生的公钥，不暴露私钥。
type sshKeyResponseItem struct {
	ID              uint       `json:"id"`
	Name            string     `json:"name"`
	Username        string     `json:"username"`
	KeyType         string     `json:"key_type"`
	Fingerprint     string     `json:"fingerprint"`
	PublicKey       string     `json:"public_key,omitempty"`
	Disabled        bool       `json:"disabled"`
	ExpiresAt       *time.Time `json:"expires_at"`
	AllowedPurposes string     `json:"allowed_purposes"`
	AllowedNodeIDs  string     `json:"allowed_node_ids"`
	AllowedNodeTags string     `json:"allowed_node_tags"`
	BroadScope      bool       `json:"broad_scope"`
	LastUsedAt      *time.Time `json:"last_used_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// toSSHKeyResponse 将 model.SSHKey 转换为安全的响应结构（含派生公钥，不含私钥）。
func toSSHKeyResponse(item model.SSHKey) sshKeyResponseItem {
	publicKey, _ := sshutil.DerivePublicKey(item.PrivateKey)
	keyType := item.KeyType
	if strings.TrimSpace(keyType) == "" {
		keyType = sshutil.SSHKeyTypeAuto
	}
	return sshKeyResponseItem{
		ID:              item.ID,
		Name:            item.Name,
		Username:        item.Username,
		KeyType:         keyType,
		Fingerprint:     item.Fingerprint,
		PublicKey:       publicKey,
		Disabled:        item.Disabled,
		ExpiresAt:       item.ExpiresAt,
		AllowedPurposes: item.AllowedPurposes,
		AllowedNodeIDs:  item.AllowedNodeIDs,
		AllowedNodeTags: item.AllowedNodeTags,
		BroadScope:      sshutil.IsBroadScope(item),
		LastUsedAt:      item.LastUsedAt,
		CreatedAt:       item.CreatedAt,
		UpdatedAt:       item.UpdatedAt,
	}
}

func generateFingerprint(privateKey string) string {
	sum := sha256.Sum256([]byte(privateKey))
	encoded := base64.StdEncoding.EncodeToString(sum[:])
	return fmt.Sprintf("SHA256:%s", encoded)
}

func (h *SSHKeyHandler) visibleSSHKeysQuery(c *gin.Context) (*gorm.DB, error) {
	query := h.db.Model(&model.SSHKey{})
	switch middleware.CurrentRole(c) {
	case "admin":
		return query, nil
	case "viewer":
		return query.Where("id IN (?)", h.db.Model(&model.Node{}).
			Select("DISTINCT ssh_key_id").
			Where("ssh_key_id IS NOT NULL")), nil
	case "operator":
		ownedNodeIDs, err := middleware.OwnedNodeIDs(h.db, middleware.CurrentUserID(c))
		if err != nil {
			return nil, err
		}
		if len(ownedNodeIDs) == 0 {
			return query.Where("1 = 0"), nil
		}
		return query.Where("id IN (?)", h.db.Model(&model.Node{}).
			Select("DISTINCT ssh_key_id").
			Where("id IN ? AND ssh_key_id IS NOT NULL", ownedNodeIDs)), nil
	default:
		return nil, errUnknownRole
	}
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

func applySSHKeyScopeInput(item *model.SSHKey, scope sshKeyScopeRequest) error {
	allowedPurposes, err := sshutil.NormalizePurposeList(scope.AllowedPurposes)
	if err != nil {
		return err
	}
	allowedNodeIDs, err := sshutil.NormalizeNodeIDList(scope.AllowedNodeIDs)
	if err != nil {
		return err
	}
	item.Disabled = scope.Disabled
	item.ExpiresAt = scope.ExpiresAt
	item.AllowedPurposes = allowedPurposes
	item.AllowedNodeIDs = allowedNodeIDs
	item.AllowedNodeTags = sshutil.NormalizeTagList(scope.AllowedNodeTags)
	return nil
}

func applySSHKeyScopePatch(item *model.SSHKey, scope sshKeyScopePatchRequest) error {
	if scope.Disabled != nil {
		item.Disabled = *scope.Disabled
	}
	if scope.ExpiresAtSet {
		item.ExpiresAt = scope.ExpiresAt
	}
	if scope.AllowedPurposes != nil {
		allowedPurposes, err := sshutil.NormalizePurposeList(*scope.AllowedPurposes)
		if err != nil {
			return err
		}
		item.AllowedPurposes = allowedPurposes
	}
	if scope.AllowedNodeIDs != nil {
		allowedNodeIDs, err := sshutil.NormalizeNodeIDList(*scope.AllowedNodeIDs)
		if err != nil {
			return err
		}
		item.AllowedNodeIDs = allowedNodeIDs
	}
	if scope.AllowedNodeTags != nil {
		item.AllowedNodeTags = sshutil.NormalizeTagList(*scope.AllowedNodeTags)
	}
	return nil
}

// List godoc
// @Summary      列出 SSH Key
// @Description  返回所有 SSH Key 列表（不含私钥，含派生公钥）
// @Tags         ssh-keys
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response{data=[]handlers.sshKeyResponseItem}
// @Failure      401  {object}  handlers.Response
// @Router       /ssh-keys [get]
func (h *SSHKeyHandler) List(c *gin.Context) {
	query, err := h.visibleSSHKeysQuery(c)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	var items []model.SSHKey
	if err := query.Order("id asc").Find(&items).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	result := make([]sshKeyResponseItem, 0, len(items))
	for _, one := range items {
		result = append(result, toSSHKeyResponse(one))
	}
	respondOK(c, result)
}

// Get godoc
// @Summary      获取 SSH Key 详情
// @Description  返回单个 SSH Key（不含私钥，含派生公钥）
// @Tags         ssh-keys
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "SSH Key ID"
// @Success      200  {object}  handlers.Response{data=handlers.sshKeyResponseItem}
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /ssh-keys/{id} [get]
func (h *SSHKeyHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	query, err := h.visibleSSHKeysQuery(c)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	var item model.SSHKey
	if err := query.Where("id = ?", id).First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondNotFound(c, "ssh key 不存在")
			return
		}
		respondInternalError(c, err)
		return
	}
	respondOK(c, toSSHKeyResponse(item))
}

// Create godoc
// @Summary      创建 SSH Key
// @Description  创建新的 SSH Key
// @Tags         ssh-keys
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      handlers.sshKeyCreateRequest  true  "SSH Key 信息"
// @Success      201  {object}  handlers.Response{data=handlers.sshKeyResponseItem}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /ssh-keys [post]
func (h *SSHKeyHandler) Create(c *gin.Context) {
	var req sshKeyCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	normalizedName, normalizedUsername, storedKeyType, preparedKey, err := normalizeSSHKeyInput(
		req.Name,
		req.Username,
		req.KeyType,
		req.PrivateKey,
	)
	if err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	item := model.SSHKey{
		Name:        normalizedName,
		Username:    normalizedUsername,
		KeyType:     storedKeyType,
		PrivateKey:  preparedKey,
		Fingerprint: generateFingerprint(preparedKey),
	}
	if err := applySSHKeyScopeInput(&item, req.sshKeyScopeRequest); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if err := h.db.Create(&item).Error; err != nil {
		if isSSHKeyDuplicateError(err) {
			respondConflict(c, sshKeyDuplicateMessage)
			return
		}
		logSSHKeyPersistenceError("create", normalizedName, err)
		respondInternalError(c, err)
		return
	}
	respondCreated(c, toSSHKeyResponse(item))
}

// Update godoc
// @Summary      更新 SSH Key
// @Description  更新指定 SSH Key 的名称、用户名或私钥
// @Tags         ssh-keys
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int                           true  "SSH Key ID"
// @Param        body  body      handlers.sshKeyUpdateRequest  true  "更新信息"
// @Success      200  {object}  handlers.Response{data=handlers.sshKeyResponseItem}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /ssh-keys/{id} [put]
func (h *SSHKeyHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req sshKeyUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	var item model.SSHKey
	if err := h.db.First(&item, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondNotFound(c, "ssh key 不存在")
			return
		}
		logSSHKeyPersistenceError("load", strconv.FormatUint(uint64(id), 10), err)
		respondInternalError(c, err)
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
			respondBadRequest(c, err.Error())
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
				respondBadRequest(c, err.Error())
				return
			}
			item.KeyType = storedType
			item.PrivateKey = preparedKey
			item.Fingerprint = generateFingerprint(preparedKey)
		}
	}
	if err := applySSHKeyScopePatch(&item, req.sshKeyScopePatchRequest); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if err := h.db.Save(&item).Error; err != nil {
		if isSSHKeyDuplicateError(err) {
			respondConflict(c, sshKeyDuplicateMessage)
			return
		}
		logSSHKeyPersistenceError("update", normalizedName, err)
		respondInternalError(c, err)
		return
	}
	respondOK(c, toSSHKeyResponse(item))
}

// Delete godoc
// @Summary      删除 SSH Key
// @Description  删除指定 SSH Key（正在被节点使用时拒绝）
// @Tags         ssh-keys
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "SSH Key ID"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /ssh-keys/{id} [delete]
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
		respondBadRequest(c, "该 ssh key 正在被节点使用，无法删除")
		return
	}

	if err := h.db.Delete(&model.SSHKey{}, id).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, gin.H{"message": "deleted", "deleted_at": time.Now()})
}

// TestConnection godoc
// @Summary      测试 SSH Key 连通性
// @Description  使用指定 SSH Key 对一组节点进行连通性测试
// @Tags         ssh-keys
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int   true  "SSH Key ID"
// @Param        body  body      object  true  "节点 ID 列表"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /ssh-keys/{id}/test-connection [post]
func (h *SSHKeyHandler) TestConnection(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req struct {
		NodeIDs []uint `json:"node_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if len(req.NodeIDs) == 0 {
		respondBadRequest(c, "node_ids 不能为空")
		return
	}

	var sshKey model.SSHKey
	if err := h.db.First(&sshKey, id).Error; err != nil {
		respondNotFound(c, "ssh key 不存在")
		return
	}

	var nodes []model.Node
	if err := h.db.Where("id IN ?", req.NodeIDs).Find(&nodes).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	hostKeyCallback, hkErr := sshutil.ResolveSSHHostKeyCallback()
	if hkErr != nil {
		respondInternalError(c, hkErr)
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
	successCount := 0
	failureCount := 0
	blockedCount := 0
	for _, node := range nodes {
		// 构造临时节点，将 SSHKey 指向待测试的密钥
		testNode := node
		testNode.AuthType = "key"
		testNode.SSHKey = &sshKey
		testNode.SSHKeyID = &sshKey.ID

		authMethods, _, err := sshutil.BuildSSHAuthForPurpose(testNode, h.db, sshutil.PurposeSSHKeyTest)
		if err != nil {
			blockedCount++
			results = append(results, testResult{
				NodeID:  node.ID,
				Name:    node.Name,
				Host:    node.Host,
				Port:    node.Port,
				Success: false,
				Error:   sanitizedClientError(err),
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
			failureCount++
			results = append(results, testResult{
				NodeID:    node.ID,
				Name:      node.Name,
				Host:      node.Host,
				Port:      node.Port,
				Success:   false,
				LatencyMs: latency,
				Error:     sanitizedClientError(dialErr),
			})
			continue
		}
		_ = client.Close()
		successCount++

		results = append(results, testResult{
			NodeID:    node.ID,
			Name:      node.Name,
			Host:      node.Host,
			Port:      node.Port,
			Success:   true,
			LatencyMs: latency,
		})
	}

	writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
		Action:           "ssh_key.test_connection",
		Purpose:          sshutil.PurposeSSHKeyTest,
		CredentialKind:   "ssh_key",
		CredentialSource: fmt.Sprintf("ssh_key_id=%d", sshKey.ID),
		SSHKeyID:         &sshKey.ID,
		Outcome:          credentialAuditOutcome(successCount, failureCount, blockedCount),
		Metadata: map[string]any{
			"node_count":    len(nodes),
			"success_count": successCount,
			"failure_count": failureCount,
			"blocked_count": blockedCount,
		},
	})

	respondOK(c, results)
}

// BatchCreate godoc
// @Summary      批量创建 SSH Key
// @Description  批量创建 SSH Key，单次最多 50 条
// @Tags         ssh-keys
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      object  true  "SSH Key 列表"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /ssh-keys/batch [post]
func (h *SSHKeyHandler) BatchCreate(c *gin.Context) {
	var req struct {
		Keys []sshKeyCreateRequest `json:"keys" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if len(req.Keys) == 0 {
		respondBadRequest(c, "keys 不能为空")
		return
	}
	if len(req.Keys) > 50 {
		respondBadRequest(c, "单次批量创建不能超过 50 条")
		return
	}

	type batchResult struct {
		Name      string `json:"name"`
		Status    string `json:"status"` // created | skipped | error
		ErrorCode string `json:"error_code,omitempty"`
		Error     string `json:"error,omitempty"`
	}

	results := make([]batchResult, 0, len(req.Keys))
	for _, k := range req.Keys {
		normalizedName, normalizedUsername, storedKeyType, preparedKey, err := normalizeSSHKeyInput(
			k.Name, k.Username, k.KeyType, k.PrivateKey,
		)
		if err != nil {
			results = append(results, batchResult{
				Name:      strings.TrimSpace(k.Name),
				Status:    "error",
				ErrorCode: sshKeyValidationErrorCode,
				Error:     err.Error(),
			})
			continue
		}

		// 检查名称唯一性
		var exists int64
		if err := h.db.Model(&model.SSHKey{}).Where("name = ?", normalizedName).Count(&exists).Error; err != nil {
			logSSHKeyPersistenceError("check-duplicate", normalizedName, err)
			results = append(results, batchResult{
				Name:      normalizedName,
				Status:    "error",
				ErrorCode: sshKeyPersistenceCode,
				Error:     sshKeyPersistenceMessage,
			})
			continue
		}
		if exists > 0 {
			results = append(results, batchResult{
				Name:      normalizedName,
				Status:    "skipped",
				ErrorCode: sshKeyDuplicateErrorCode,
				Error:     sshKeyDuplicateMessage,
			})
			continue
		}

		item := model.SSHKey{
			Name:        normalizedName,
			Username:    normalizedUsername,
			KeyType:     storedKeyType,
			PrivateKey:  preparedKey,
			Fingerprint: generateFingerprint(preparedKey),
		}
		if err := applySSHKeyScopeInput(&item, k.sshKeyScopeRequest); err != nil {
			results = append(results, batchResult{
				Name:      normalizedName,
				Status:    "error",
				ErrorCode: sshKeyValidationErrorCode,
				Error:     err.Error(),
			})
			continue
		}
		if err := h.db.Create(&item).Error; err != nil {
			if isSSHKeyDuplicateError(err) {
				results = append(results, batchResult{
					Name:      normalizedName,
					Status:    "skipped",
					ErrorCode: sshKeyDuplicateErrorCode,
					Error:     sshKeyDuplicateMessage,
				})
				continue
			}
			logSSHKeyPersistenceError("batch-create", normalizedName, err)
			results = append(results, batchResult{
				Name:      normalizedName,
				Status:    "error",
				ErrorCode: sshKeyPersistenceCode,
				Error:     sshKeyPersistenceMessage,
			})
			continue
		}
		results = append(results, batchResult{Name: normalizedName, Status: "created"})
	}

	respondOK(c, results)

}

// Export godoc
// @Summary      导出 SSH Key
// @Description  导出 SSH Key 列表，支持 authorized_keys / json / csv 格式
// @Tags         ssh-keys
// @Security     Bearer
// @Produce      json
// @Param        format  query     string  false  "导出格式（authorized_keys/json/csv）"
// @Param        scope   query     string  false  "范围（all/in_use）"
// @Param        ids     query     string  false  "逗号分隔的 ID 列表"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /ssh-keys/export [get]
func (h *SSHKeyHandler) Export(c *gin.Context) {
	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "authorized_keys")))
	scope := strings.ToLower(strings.TrimSpace(c.DefaultQuery("scope", "all")))
	idsParam := strings.TrimSpace(c.Query("ids"))

	query, err := h.visibleSSHKeysQuery(c)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	query = query.Order("id asc")

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

	exportable := make([]model.SSHKey, 0, len(items))
	blockedCount := 0
	for _, item := range items {
		if err := sshutil.ValidateSSHKeyPurpose(item, sshutil.PurposeSSHKeyExport); err != nil {
			blockedCount++
			continue
		}
		exportable = append(exportable, item)
	}
	writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
		Action:           "ssh_key.export",
		Purpose:          sshutil.PurposeSSHKeyExport,
		CredentialKind:   "ssh_key",
		CredentialSource: "ssh_key_export",
		Outcome:          credentialAuditOutcome(len(exportable), 0, blockedCount),
		Metadata: map[string]any{
			"format":          format,
			"scope":           scope,
			"requested_count": len(items),
			"exported_count":  len(exportable),
			"blocked_count":   blockedCount,
		},
	})

	switch format {
	case "authorized_keys":
		var lines []string
		for _, item := range exportable {
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
		result := make([]sshKeyResponseItem, 0, len(exportable))
		for _, item := range exportable {
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
		for _, item := range exportable {
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
		respondBadRequest(c, "不支持的导出格式，可选：authorized_keys / json / csv")
	}
}

// BatchDelete godoc
// @Summary      批量删除 SSH Key
// @Description  批量删除 SSH Key，正在被节点使用的密钥会被跳过
// @Tags         ssh-keys
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      object  true  "要删除的 ID 列表"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /ssh-keys/batch-delete [post]
func (h *SSHKeyHandler) BatchDelete(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if len(req.IDs) == 0 {
		respondBadRequest(c, "ids 不能为空")
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

	respondOK(c, gin.H{
		"deleted":        deleted,
		"skipped_in_use": skippedNames,
	})
}
