package handlers

import (
	"encoding/json"
	"errors"
	"strings"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type NodeLogConfigHandler struct{ db *gorm.DB }

func NewNodeLogConfigHandler(db *gorm.DB) *NodeLogConfigHandler {
	return &NodeLogConfigHandler{db: db}
}

type nodeLogConfigRequest struct {
	LogPaths             []string `json:"log_paths"`
	LogJournalctlEnabled bool     `json:"log_journalctl_enabled"`
	LogRetentionDays     int      `json:"log_retention_days"`
}

type nodeLogConfigResponse struct {
	LogPaths             []string `json:"log_paths"`
	LogJournalctlEnabled bool     `json:"log_journalctl_enabled"`
	LogRetentionDays     int      `json:"log_retention_days"`
}

var logPathDenyPrefixes = []string{"/etc/", "/proc/", "/sys/", "/dev/", "/boot/", "/root/"}

// shell metacharacters that enable command substitution / quote-break inside
// the remote SSH script (buildScript uses double-quoted path args).
const logPathShellMetaChars = "$`\\\n\r\"'"

func validateLogPaths(paths []string) error {
	if len(paths) > 20 {
		return errors.New("log_paths: 最多 20 条")
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, "/") {
			return errors.New("log_paths: 必须绝对路径 (" + p + ")")
		}
		if strings.ContainsAny(p, "*?[]") {
			return errors.New("log_paths: 不支持通配符 (" + p + ")")
		}
		if strings.ContainsAny(p, logPathShellMetaChars) {
			return errors.New("log_paths: 路径包含非法字符 (" + p + ")")
		}
		for _, deny := range logPathDenyPrefixes {
			if strings.HasPrefix(p, deny) {
				return errors.New("log_paths: 路径在黑名单中 (" + p + ")")
			}
		}
	}
	return nil
}

func (h *NodeLogConfigHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var n model.Node
	if err := h.db.First(&n, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondNotFound(c, "节点不存在")
			return
		}
		respondInternalError(c, err)
		return
	}
	respondOK(c, nodeLogConfigResponse{
		LogPaths:             n.DecodedLogPaths(),
		LogJournalctlEnabled: n.LogJournalctlEnabled,
		LogRetentionDays:     n.LogRetentionDays,
	})
}

func (h *NodeLogConfigHandler) Patch(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req nodeLogConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if err := validateLogPaths(req.LogPaths); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if req.LogRetentionDays < 0 || req.LogRetentionDays > 365 {
		respondBadRequest(c, "log_retention_days 必须 0-365")
		return
	}
	var n model.Node
	if err := h.db.First(&n, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondNotFound(c, "节点不存在")
			return
		}
		respondInternalError(c, err)
		return
	}
	encoded, _ := json.Marshal(req.LogPaths)
	updates := map[string]any{
		"log_paths":              string(encoded),
		"log_journalctl_enabled": req.LogJournalctlEnabled,
		"log_retention_days":     req.LogRetentionDays,
	}
	if err := h.db.Model(&model.Node{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, nodeLogConfigResponse{
		LogPaths:             req.LogPaths,
		LogJournalctlEnabled: req.LogJournalctlEnabled,
		LogRetentionDays:     req.LogRetentionDays,
	})
}
