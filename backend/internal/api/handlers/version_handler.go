package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/version"

	"github.com/gin-gonic/gin"
)

// VersionHandler 提供版本信息与可选的更新检查
type VersionHandler struct{}

func NewVersionHandler() *VersionHandler {
	return &VersionHandler{}
}

// Info 返回当前版本信息（无需认证）
func (h *VersionHandler) Info(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"version":    version.Version,
		"build_time": version.BuildTime,
		"git_commit": version.GitCommit,
	})
}

// Check 检查是否有新版本可用（需 admin 权限）
func (h *VersionHandler) Check(c *gin.Context) {
	checkURL := os.Getenv("VERSION_CHECK_URL")
	if checkURL == "" {
		c.JSON(http.StatusOK, gin.H{
			"update_available": false,
			"message":          "未配置版本检查地址",
		})
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(checkURL)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("请求版本检查地址失败: %v", err)})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("版本检查地址返回状态码 %d", resp.StatusCode)})
		return
	}

	var release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "解析版本检查响应失败"})
		return
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	currentVersion := strings.TrimPrefix(version.Version, "v")
	updateAvailable := compareSemver(currentVersion, latestVersion) < 0

	c.JSON(http.StatusOK, gin.H{
		"update_available": updateAvailable,
		"current_version":  version.Version,
		"latest_version":   latestVersion,
		"release_url":      release.HTMLURL,
	})
}

// compareSemver 简单的语义版本比较，返回 -1/0/1。
// 非合法 semver 时按字符串比较回退。
func compareSemver(a, b string) int {
	partsA := strings.SplitN(a, ".", 3)
	partsB := strings.SplitN(b, ".", 3)

	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}

	for i := 0; i < maxLen; i++ {
		var va, vb int
		if i < len(partsA) {
			va, _ = strconv.Atoi(partsA[i])
		}
		if i < len(partsB) {
			vb, _ = strconv.Atoi(partsB[i])
		}
		if va < vb {
			return -1
		}
		if va > vb {
			return 1
		}
	}
	return 0
}
