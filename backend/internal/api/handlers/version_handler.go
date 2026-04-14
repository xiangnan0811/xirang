package handlers

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
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

	if err := validateCheckURL(checkURL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "版本检查地址配置不合法"})
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(checkURL)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "请求版本检查地址失败"})
		return
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": "版本检查地址返回异常"})
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

	// 校验 release URL 必须以 https:// 开头
	releaseURL := ""
	if strings.HasPrefix(release.HTMLURL, "https://") {
		releaseURL = release.HTMLURL
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	currentVersion := strings.TrimPrefix(version.Version, "v")
	updateAvailable := compareSemver(currentVersion, latestVersion) < 0

	c.JSON(http.StatusOK, gin.H{
		"update_available": updateAvailable,
		"current_version":  version.Version,
		"latest_version":   latestVersion,
		"release_url":      releaseURL,
	})
}

// validateCheckURL 校验版本检查 URL 的安全性（禁止私有地址和非 HTTPS）。
func validateCheckURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("无效的 URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("仅允许 HTTPS 协议")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("不允许访问内部网络地址")
		}
	} else {
		// DNS 解析域名，检查所有解析结果防止 DNS rebinding
		addrs, err := net.LookupHost(host)
		if err != nil {
			return fmt.Errorf("无法解析主机名")
		}
		for _, addr := range addrs {
			resolved := net.ParseIP(addr)
			if resolved != nil && (resolved.IsLoopback() || resolved.IsPrivate() || resolved.IsLinkLocalUnicast() || resolved.IsLinkLocalMulticast()) {
				return fmt.Errorf("不允许访问内部网络地址")
			}
		}
	}
	return nil
}

// compareSemver 简单的语义版本比较，返回 -1/0/1。
// 仅处理 major.minor.patch 数字格式，不支持 pre-release/metadata 后缀。
// 非合法 semver 时按字符串比较回退（Atoi 失败默认为 0）。
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
