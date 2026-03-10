package handlers

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/model"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type IntegrationHandler struct {
	db *gorm.DB
}

func NewIntegrationHandler(db *gorm.DB) *IntegrationHandler {
	return &IntegrationHandler{db: db}
}

type integrationRequest struct {
	Type            string `json:"type" binding:"required"`
	Name            string `json:"name" binding:"required"`
	Endpoint        string `json:"endpoint" binding:"required"`
	Enabled         *bool  `json:"enabled"`
	FailThreshold   int    `json:"fail_threshold"`
	CooldownMinutes int    `json:"cooldown_minutes"`
}

type integrationTestResponse struct {
	OK        bool   `json:"ok"`
	Message   string `json:"message"`
	LatencyMS int64  `json:"latency_ms"`
}

func validateIntegrationEndpoint(channelType, endpoint string) error {
	normalizedType := strings.ToLower(strings.TrimSpace(channelType))
	if normalizedType != "webhook" && normalizedType != "slack" && normalizedType != "telegram" {
		return nil
	}

	parsedURL, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsedURL == nil {
		return fmt.Errorf("%s 通道 endpoint 必须是合法 URL", normalizedType)
	}

	scheme := strings.ToLower(strings.TrimSpace(parsedURL.Scheme))
	if (scheme != "http" && scheme != "https") || parsedURL.Host == "" {
		return fmt.Errorf("%s 通道仅允许 http/https URL", normalizedType)
	}
	if normalizedType == "telegram" {
		if err := validateTelegramEndpoint(parsedURL); err != nil {
			return err
		}
	}

	blockPrivate, err := util.ReadBoolEnv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", true)
	if err != nil {
		return err
	}
	if !blockPrivate {
		return nil
	}

	hostName := strings.TrimSpace(parsedURL.Hostname())
	if hostName == "" {
		return fmt.Errorf("%s 通道 endpoint 缺少主机地址", normalizedType)
	}
	if err := validatePublicEndpointHost(hostName); err != nil {
		return fmt.Errorf("%s 通道 endpoint 不安全: %w", normalizedType, err)
	}
	return nil
}

func validateTelegramEndpoint(parsedURL *url.URL) error {
	_, err := util.ValidateTelegramEndpoint(parsedURL)
	return err
}

func validatePublicEndpointHost(host string) error {
	normalizedHost := strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if normalizedHost == "" {
		return fmt.Errorf("主机地址不能为空")
	}
	if normalizedHost == "localhost" || strings.HasSuffix(normalizedHost, ".localhost") {
		return fmt.Errorf("禁止使用本地回环地址")
	}

	if ip, err := netip.ParseAddr(normalizedHost); err == nil {
		if isPrivateOrLoopback(ip.Unmap()) {
			return fmt.Errorf("禁止使用内网或回环地址")
		}
		return nil
	}

	resolved, err := resolveHostAddrs(normalizedHost)
	if err != nil {
		return fmt.Errorf("无法解析主机地址，请检查域名是否正确")
	}
	if len(resolved) == 0 {
		return fmt.Errorf("主机地址无法解析，请检查域名是否正确")
	}
	for _, ip := range resolved {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		if isPrivateOrLoopback(addr.Unmap()) {
			return fmt.Errorf("该地址指向内网或回环地址，不允许使用")
		}
	}
	return nil
}

func resolveHostAddrs(host string) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ipAddrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	addresses := make([]net.IP, 0, len(ipAddrs))
	for _, item := range ipAddrs {
		if item.IP != nil {
			addresses = append(addresses, item.IP)
		}
	}
	return addresses, nil
}

func isPrivateOrLoopback(addr netip.Addr) bool {
	return addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsUnspecified()
}

func (h *IntegrationHandler) List(c *gin.Context) {
	var items []model.Integration
	if err := h.db.Order("id asc").Find(&items).Error; err != nil {
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	for i := range items {
		maskIntegrationEndpoint(&items[i])
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (h *IntegrationHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var item model.Integration
	if err := h.db.First(&item, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "通知通道不存在"})
		return
	}
	maskIntegrationEndpoint(&item)
	c.JSON(http.StatusOK, gin.H{"data": item})
}

func (h *IntegrationHandler) Create(c *gin.Context) {
	var req integrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.Name = strings.TrimSpace(req.Name)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	if req.Type == "" || req.Name == "" || req.Endpoint == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}
	if err := validateIntegrationEndpoint(req.Type, req.Endpoint); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if req.FailThreshold <= 0 {
		req.FailThreshold = 1
	}
	if req.CooldownMinutes <= 0 {
		req.CooldownMinutes = 5
	}

	item := model.Integration{
		Type:            req.Type,
		Name:            req.Name,
		Endpoint:        req.Endpoint,
		Enabled:         enabled,
		FailThreshold:   req.FailThreshold,
		CooldownMinutes: req.CooldownMinutes,
	}
	if err := h.db.Create(&item).Error; err != nil {
		log.Printf("创建通知通道失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	maskIntegrationEndpoint(&item)
	c.JSON(http.StatusCreated, gin.H{"data": item})
}

func (h *IntegrationHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req integrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.Name = strings.TrimSpace(req.Name)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	if req.Type == "" || req.Name == "" || req.Endpoint == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}
	if err := validateIntegrationEndpoint(req.Type, req.Endpoint); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var item model.Integration
	if err := h.db.First(&item, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "通知通道不存在"})
		return
	}
	if req.FailThreshold <= 0 {
		req.FailThreshold = item.FailThreshold
	}
	if req.CooldownMinutes <= 0 {
		req.CooldownMinutes = item.CooldownMinutes
	}

	item.Type = req.Type
	item.Name = req.Name
	item.Endpoint = req.Endpoint
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	item.FailThreshold = req.FailThreshold
	item.CooldownMinutes = req.CooldownMinutes

	if err := h.db.Save(&item).Error; err != nil {
		log.Printf("更新通知通道失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	maskIntegrationEndpoint(&item)
	c.JSON(http.StatusOK, gin.H{"data": item})
}

func (h *IntegrationHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.db.Delete(&model.Integration{}, id).Error; err != nil {
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

func (h *IntegrationHandler) Test(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var item model.Integration
	if err := h.db.First(&item, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "通知通道不存在"})
		return
	}

	startedAt := time.Now()
	err := alerting.SendProbe(item)
	latency := time.Since(startedAt).Milliseconds()
	if err != nil {
		errMsg := err.Error()
		if strings.ToLower(strings.TrimSpace(item.Type)) == "telegram" {
			errMsg = util.SanitizeTelegramError(err)
		}
		c.JSON(http.StatusOK, gin.H{"data": integrationTestResponse{
			OK:        false,
			Message:   "测试发送失败: " + errMsg,
			LatencyMS: latency,
		}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": integrationTestResponse{
		OK:        true,
		Message:   "测试发送成功",
		LatencyMS: latency,
	}})
}

// maskIntegrationEndpoint 对 Telegram 类型通道的 endpoint 中 bot token 进行脱敏
func maskIntegrationEndpoint(item *model.Integration) {
	if strings.ToLower(strings.TrimSpace(item.Type)) == "telegram" {
		item.Endpoint = util.MaskBotToken(item.Endpoint)
	}
}
