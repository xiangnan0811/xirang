package handlers

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
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

func generateFingerprint(privateKey string) string {
	sum := sha256.Sum256([]byte(privateKey))
	encoded := base64.StdEncoding.EncodeToString(sum[:])
	return fmt.Sprintf("SHA256:%s", encoded)
}

func sanitizeSSHKey(item model.SSHKey) model.SSHKey {
	copyItem := item
	if strings.TrimSpace(copyItem.KeyType) == "" {
		copyItem.KeyType = sshutil.SSHKeyTypeAuto
	}
	copyItem.PrivateKey = ""
	return copyItem
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	result := make([]model.SSHKey, 0, len(items))
	for _, one := range items {
		result = append(result, sanitizeSSHKey(one))
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
	c.JSON(http.StatusOK, gin.H{"data": sanitizeSSHKey(item)})
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
	c.JSON(http.StatusCreated, gin.H{"data": sanitizeSSHKey(item)})
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
	c.JSON(http.StatusOK, gin.H{"data": sanitizeSSHKey(item)})
}

func (h *SSHKeyHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var count int64
	if err := h.db.Model(&model.Node{}).Where("ssh_key_id = ?", id).Count(&count).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if count > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该 ssh key 正在被节点使用，无法删除"})
		return
	}

	if err := h.db.Delete(&model.SSHKey{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted", "deleted_at": time.Now()})
}
