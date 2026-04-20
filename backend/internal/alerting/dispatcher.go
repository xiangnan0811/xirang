package alerting

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/slo"
	"xirang/backend/internal/util"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/net/proxy"
	"gorm.io/gorm"
)

var alertsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "xirang_alerts_total",
	Help: "Total alerts raised by severity",
}, []string{"severity"})

// settingsSvc 模块级设置服务引用，由 InitSettings 注入（sync.Once 保证并发安全）
var (
	settingsSvc    *settings.Service
	settingsInitMu sync.Mutex
)

// InitSettings 注入设置服务（在 main 中调用，sync.Mutex 保证写入可见性）
func InitSettings(svc *settings.Service) {
	settingsInitMu.Lock()
	settingsSvc = svc
	settingsInitMu.Unlock()
}

// getSettingsSvc 安全读取设置服务引用
func getSettingsSvc() *settings.Service {
	settingsInitMu.Lock()
	svc := settingsSvc
	settingsInitMu.Unlock()
	return svc
}

var defaultHTTPClient = &http.Client{Timeout: 15 * time.Second}

type payload struct {
	Title      string    `json:"title"`
	Severity   string    `json:"severity"`
	Status     string    `json:"status"`
	NodeName   string    `json:"node_name"`
	TaskID     *uint     `json:"task_id,omitempty"`
	PolicyName string    `json:"policy_name,omitempty"`
	ErrorCode  string    `json:"error_code"`
	Message    string    `json:"message"`
	Triggered  time.Time `json:"triggered_at"`
}

func RaiseTaskFailure(db *gorm.DB, task model.Task, taskRunID *uint, message string) error {
	errorCode := fmt.Sprintf("XR-EXEC-%d", task.ID)
	policyName := ""
	if task.Policy != nil {
		policyName = task.Policy.Name
	}
	alert := model.Alert{
		NodeID:      task.NodeID,
		NodeName:    task.Node.Name,
		TaskID:      &task.ID,
		TaskRunID:   taskRunID,
		PolicyName:  policyName,
		Severity:    "critical",
		Status:      "open",
		ErrorCode:   errorCode,
		Message:     message,
		Retryable:   true,
		TriggeredAt: time.Now(),
	}
	return raiseAndDispatch(db, &alert)
}

func RaiseVerificationFailure(db *gorm.DB, task model.Task, taskRunID *uint, message string) error {
	errorCode := fmt.Sprintf("XR-VRFY-%d", task.ID)
	policyName := ""
	if task.Policy != nil {
		policyName = task.Policy.Name
	}
	alert := model.Alert{
		NodeID:      task.NodeID,
		NodeName:    task.Node.Name,
		TaskID:      &task.ID,
		TaskRunID:   taskRunID,
		PolicyName:  policyName,
		Severity:    "warning",
		Status:      "open",
		ErrorCode:   errorCode,
		Message:     message,
		Retryable:   false,
		TriggeredAt: time.Now(),
	}
	return raiseAndDispatch(db, &alert)
}

func ResolveTaskAlerts(db *gorm.DB, taskID uint, note string) error {
	updates := map[string]interface{}{
		"status":           "resolved",
		"retryable":        false,
		"last_notified_at": time.Now(),
	}
	if note != "" {
		updates["message"] = note
	}
	return db.Model(&model.Alert{}).
		Where("task_id = ? AND status IN ?", taskID, []string{"open", "acked"}).
		Updates(updates).Error
}

func RaiseNodeProbeFailure(db *gorm.DB, node model.Node, message string) error {
	errorCode := fmt.Sprintf("XR-NODE-%d", node.ID)
	alert := model.Alert{
		NodeID:      node.ID,
		NodeName:    node.Name,
		TaskID:      nil,
		PolicyName:  "",
		Severity:    "warning",
		Status:      "open",
		ErrorCode:   errorCode,
		Message:     message,
		Retryable:   false,
		TriggeredAt: time.Now(),
	}
	return raiseAndDispatch(db, &alert)
}

func RaiseDiskUsageAlert(db *gorm.DB, node model.Node, diskPct float64) error {
	alert := model.Alert{
		NodeID:      node.ID,
		NodeName:    node.Name,
		TaskID:      nil,
		PolicyName:  "",
		Severity:    "warning",
		Status:      "open",
		ErrorCode:   "XR-NODE-DISK-FULL",
		Message:     fmt.Sprintf("节点磁盘使用率 %.1f%% 超过 90%%", diskPct),
		Retryable:   false,
		TriggeredAt: time.Now(),
	}
	return raiseAndDispatch(db, &alert)
}

func RaiseNodeExpiryWarning(db *gorm.DB, node model.Node, message string) error {
	severity := "warning"
	errorCode := fmt.Sprintf("XR-NODE-EXPIRY-%d", node.ID)
	alert := model.Alert{
		NodeID:      node.ID,
		NodeName:    node.Name,
		Severity:    severity,
		Status:      "open",
		ErrorCode:   errorCode,
		Message:     message,
		Retryable:   false,
		TriggeredAt: time.Now(),
	}
	return raiseAndDispatch(db, &alert)
}

func RaiseRetentionFailure(db *gorm.DB, policyID uint, policyName string, nodeName string, nodeID uint, message string) error {
	errorCode := fmt.Sprintf("XR-RETN-%d", policyID)
	alert := model.Alert{
		NodeID:      nodeID,
		NodeName:    nodeName,
		PolicyName:  policyName,
		Severity:    "warning",
		Status:      "open",
		ErrorCode:   errorCode,
		Message:     message,
		Retryable:   false,
		TriggeredAt: time.Now(),
	}
	return raiseAndDispatch(db, &alert)
}

func RaiseIntegrityCheckFailure(db *gorm.DB, policyID uint, policyName string, nodeName string, nodeID uint, message string) error {
	errorCode := fmt.Sprintf("XR-INTG-%d", policyID)
	alert := model.Alert{
		NodeID:      nodeID,
		NodeName:    nodeName,
		PolicyName:  policyName,
		Severity:    "warning",
		Status:      "open",
		ErrorCode:   errorCode,
		Message:     message,
		Retryable:   false,
		TriggeredAt: time.Now(),
	}
	return raiseAndDispatch(db, &alert)
}

func ResolveAlertsByErrorCode(db *gorm.DB, errorCode string, note string) error {
	updates := map[string]interface{}{
		"status":           "resolved",
		"retryable":        false,
		"last_notified_at": time.Now(),
	}
	if note != "" {
		updates["message"] = note
	}
	return db.Model(&model.Alert{}).
		Where("error_code = ? AND status IN ?", errorCode, []string{"open", "acked"}).
		Updates(updates).Error
}

func RaiseStorageSpaceAlert(db *gorm.DB, targetPath string, freeGB float64, totalGB float64, usagePct float64) error {
	severity := "warning"
	if usagePct >= 95 {
		severity = "critical"
	}
	alert := model.Alert{
		NodeID:      0,
		NodeName:    "localhost",
		PolicyName:  "",
		Severity:    severity,
		Status:      "open",
		ErrorCode:   "XR-STORAGE-LOW:" + targetPath,
		Message:     fmt.Sprintf("本地备份存储空间不足: %s (剩余 %.1fGB / 共 %.1fGB, 使用率 %.1f%%)", targetPath, freeGB, totalGB, usagePct),
		Retryable:   false,
		TriggeredAt: time.Now(),
	}
	return raiseAndDispatch(db, &alert)
}

func ResolveNodeAlerts(db *gorm.DB, nodeID uint, note string) error {
	updates := map[string]interface{}{
		"status":           "resolved",
		"retryable":        false,
		"last_notified_at": time.Now(),
	}
	if note != "" {
		updates["message"] = note
	}
	return db.Model(&model.Alert{}).
		Where("node_id = ? AND task_id IS NULL AND status IN ?", nodeID, []string{"open", "acked"}).
		Updates(updates).Error
}

func raiseAndDispatch(db *gorm.DB, alert *model.Alert) error {
	if deduped, err := inDedupWindow(db, *alert, time.Now()); err != nil {
		return err
	} else if deduped {
		return nil
	}

	if err := db.Create(alert).Error; err != nil {
		return err
	}
	alertsTotal.WithLabelValues(alert.Severity).Inc()

	var integrations []model.Integration
	if err := db.Where("enabled = ?", true).Find(&integrations).Error; err != nil {
		return err
	}
	if len(integrations) == 0 {
		return nil
	}

	var openCount int64
	if err := db.Model(&model.Alert{}).
		Where("node_id = ? AND status = ?", alert.NodeID, "open").
		Count(&openCount).Error; err != nil {
		return err
	}

	now := time.Now()

	// 静默检查：若告警命中活跃静默规则，跳过所有通道投递
	silences, _ := ActiveSilences(db, now)
	var node model.Node
	nodeLoadAttempted := false
	if len(silences) > 0 {
		if res := db.First(&node, alert.NodeID); res.Error != nil {
			logger.Module("alerting").Warn().
				Uint("alert_id", alert.ID).
				Uint("node_id", alert.NodeID).
				Err(res.Error).
				Msg("静默检查：节点加载失败，使用空 tags 计算 group_key")
		}
		nodeLoadAttempted = true
		if matched := MatchSilence(*alert, node, silences, now); matched != nil {
			logger.Module("alerting").Info().
				Uint("alert_id", alert.ID).
				Uint("silence_id", matched.ID).
				Msg("告警已静默，跳过投递")
			return nil
		}
	}

	// 分组检查：同一 (category, nodeID, tags) 组合在窗口内只投递首次告警
	if !nodeLoadAttempted {
		if res := db.First(&node, alert.NodeID); res.Error != nil {
			logger.Module("alerting").Warn().
				Uint("alert_id", alert.ID).
				Uint("node_id", alert.NodeID).
				Err(res.Error).
				Msg("分组检查：节点加载失败，使用空 tags 计算 group_key")
		}
	}
	key := GroupKey(alert.ErrorCode, alert.NodeID, splitNodeTags(node.Tags))
	if !GetSharedGrouping().ShouldSend(key) {
		logger.Module("alerting").Info().
			Uint("alert_id", alert.ID).
			Int("group_count", GetSharedGrouping().Count(key)).
			Msg("告警已被分组，跳过投递")
		return nil
	}

	var wg sync.WaitGroup
	for _, channel := range integrations {
		if int(openCount) < channel.FailThreshold {
			continue
		}
		if inCooldown(db, channel.ID, channel.CooldownMinutes, now) {
			continue
		}

		wg.Add(1)
		go func(ch model.Integration) {
			defer wg.Done()
			err := send(ch, *alert)
			d := model.AlertDelivery{
				AlertID:       alert.ID,
				IntegrationID: ch.ID,
				AttemptCount:  1,
			}
			if err == nil {
				d.Status = "sent"
			} else {
				next := time.Now().Add(backoffDuration(1))
				d.Status = "retrying"
				d.NextRetryAt = &next
				d.LastError = util.SanitizeDeliveryError(ch.Type, err)
				d.Error = d.LastError // legacy compat
			}
			if saveErr := db.Create(&d).Error; saveErr != nil {
				logger.Module("alerting").Warn().Uint("alert_id", alert.ID).Uint("integration_id", ch.ID).Err(saveErr).Msg("保存告警投递记录失败")
			}
		}(channel)
	}
	wg.Wait()

	// 更新 last_notified_at（在所有发送完成后）
	var sentCount int64
	db.Model(&model.AlertDelivery{}).Where("alert_id = ? AND status = ?", alert.ID, "sent").Count(&sentCount)
	if sentCount > 0 {
		notifiedAt := time.Now()
		alert.LastNotifiedAt = &notifiedAt
		if err := db.Model(alert).Update("last_notified_at", &notifiedAt).Error; err != nil {
			logger.Module("alerting").Warn().Uint("alert_id", alert.ID).Err(err).Msg("更新告警最后通知时间失败")
		}
	}

	return nil
}

func inDedupWindow(db *gorm.DB, alert model.Alert, now time.Time) (bool, error) {
	window := readAlertDedupWindow()
	if window <= 0 {
		return false, nil
	}

	query := db.Model(&model.Alert{}).
		Where("node_id = ? AND error_code = ? AND created_at >= ?", alert.NodeID, alert.ErrorCode, now.Add(-window)).
		Where("status IN ?", []string{"open", "acked"})
	if alert.TaskID == nil {
		query = query.Where("task_id IS NULL")
	} else {
		query = query.Where("task_id = ?", *alert.TaskID)
	}

	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func readAlertDedupWindow() time.Duration {
	if svc := getSettingsSvc(); svc != nil {
		raw := svc.GetEffective("alert.dedup_window")
		if raw != "" {
			value, err := time.ParseDuration(raw)
			if err == nil && value > 0 {
				return value
			}
		}
	}
	raw := strings.TrimSpace(os.Getenv("ALERT_DEDUP_WINDOW"))
	if raw == "" {
		return 10 * time.Minute
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < 0 {
		return 10 * time.Minute
	}
	return value
}

func inCooldown(db *gorm.DB, integrationID uint, cooldownMinutes int, now time.Time) bool {
	if cooldownMinutes <= 0 {
		return false
	}
	var latest model.AlertDelivery
	err := db.Where("integration_id = ? AND status = ?", integrationID, "sent").
		Order("created_at desc").
		First(&latest).Error
	if err != nil {
		return false
	}
	return now.Sub(latest.CreatedAt) < time.Duration(cooldownMinutes)*time.Minute
}

func send(channel model.Integration, alert model.Alert) error {
	body := payload{
		Title:      "XiRang 告警通知",
		Severity:   alert.Severity,
		Status:     alert.Status,
		NodeName:   alert.NodeName,
		TaskID:     alert.TaskID,
		PolicyName: alert.PolicyName,
		ErrorCode:  alert.ErrorCode,
		Message:    alert.Message,
		Triggered:  alert.TriggeredAt,
	}

	s, ok := senderRegistry[strings.ToLower(strings.TrimSpace(channel.Type))]
	if !ok {
		return fmt.Errorf("不支持的通知通道类型: %s", channel.Type)
	}
	client := getHTTPClient(channel.ProxyURL)
	return s.Send(client, channel.Endpoint, channel.Secret, body)
}

// proxyClients 缓存按代理 URL 创建的 HTTP 客户端，避免每次调用创建新 Transport
var proxyClients sync.Map // proxyURL -> *http.Client

// getHTTPClient 根据代理配置返回 HTTP 客户端（带缓存）
func getHTTPClient(proxyURL string) *http.Client {
	if proxyURL == "" {
		return defaultHTTPClient
	}
	if cached, ok := proxyClients.Load(proxyURL); ok {
		return cached.(*http.Client)
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return defaultHTTPClient
	}
	timeout := 30 * time.Second // 代理场景给更长超时

	// blockLinkLocal 拦截链路本地地址（169.254.x.x），防止云环境中通过代理访问实例元数据
	blockLinkLocal := func(innerDial func(ctx context.Context, network, addr string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(addr)
			if ip := net.ParseIP(host); ip != nil && ip.IsLinkLocalUnicast() {
				return nil, fmt.Errorf("blocked: link-local address not allowed")
			}
			return innerDial(ctx, network, addr)
		}
	}

	defaultDial := (&net.Dialer{Timeout: 10 * time.Second}).DialContext

	var client *http.Client
	switch parsed.Scheme {
	case "socks5", "socks5h":
		dialer, err := proxy.FromURL(parsed, proxy.Direct)
		if err != nil {
			return defaultHTTPClient
		}
		transport := &http.Transport{}
		if cd, ok := dialer.(proxy.ContextDialer); ok {
			transport.DialContext = blockLinkLocal(cd.DialContext)
		} else {
			transport.DialContext = blockLinkLocal(func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			})
		}
		client = &http.Client{Timeout: timeout, Transport: transport}
	default: // http, https
		client = &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				Proxy:       http.ProxyURL(parsed),
				DialContext: blockLinkLocal(defaultDial),
			},
		}
	}
	proxyClients.Store(proxyURL, client)
	return client
}

func SendProbe(channel model.Integration) error {
	probe := model.Alert{
		NodeName:    "XiRang Probe",
		Severity:    "info",
		Status:      "open",
		ErrorCode:   "XR-PROBE-0001",
		Message:     "XiRang 通道连通性测试消息",
		TriggeredAt: time.Now(),
	}
	return send(channel, probe)
}

func SendAlert(channel model.Integration, alert model.Alert) error {
	return send(channel, alert)
}

func postJSON(client *http.Client, targetURL string, body interface{}) error {
	payloadBytes, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := client.Post(targetURL, "application/json", bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return buildNotificationHTTPError(resp.StatusCode, resp.Body)
	}
	return nil
}

func postTelegram(client *http.Client, endpoint, text string) error {
	telegramURL, params, err := buildTelegramSendMessageEndpoint(endpoint)
	if err != nil {
		return err
	}

	form := url.Values{}
	form.Set("chat_id", params.Get("chat_id"))
	form.Set("text", text)
	if parseMode := strings.TrimSpace(params.Get("parse_mode")); parseMode != "" {
		form.Set("parse_mode", parseMode)
	}
	if disabledPreview := strings.TrimSpace(params.Get("disable_web_page_preview")); disabledPreview != "" {
		form.Set("disable_web_page_preview", disabledPreview)
	}

	resp, err := client.Post(telegramURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("telegram 请求失败: %s", util.SanitizeTelegramError(err))
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return buildNotificationHTTPError(resp.StatusCode, resp.Body)
	}
	return nil
}

func buildTelegramSendMessageEndpoint(rawEndpoint string) (string, url.Values, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawEndpoint))
	if err != nil || parsed == nil {
		return "", nil, fmt.Errorf("telegram 通道 endpoint 必须是合法 URL")
	}
	if parsed.Host == "" {
		return "", nil, fmt.Errorf("telegram 通道 endpoint 缺少主机地址")
	}

	info, err := util.ValidateTelegramEndpoint(parsed)
	if err != nil {
		return "", nil, err
	}

	parsed.Path = "/" + info.BotSegment + "/sendMessage"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), info.Params, nil
}

func buildNotificationHTTPError(statusCode int, body io.Reader) error {
	raw, _ := io.ReadAll(io.LimitReader(body, 2048))
	desc := strings.TrimSpace(extractNotificationErrorDescription(raw))
	if desc == "" {
		return fmt.Errorf("通知发送失败: http %d", statusCode)
	}
	return fmt.Errorf("通知发送失败: http %d (%s)", statusCode, desc)
}

func extractNotificationErrorDescription(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}

	var respPayload map[string]interface{}
	if err := json.Unmarshal(raw, &respPayload); err == nil {
		if desc, ok := respPayload["description"].(string); ok && strings.TrimSpace(desc) != "" {
			return desc
		}
		if msg, ok := respPayload["message"].(string); ok && strings.TrimSpace(msg) != "" {
			return msg
		}
	}

	text := strings.TrimSpace(string(raw))
	runes := []rune(text)
	if len(runes) > 180 {
		return string(runes[:180]) + "..."
	}
	return text
}

func sendEmail(toRaw, subject, content string) error {
	host := strings.TrimSpace(os.Getenv("SMTP_HOST"))
	if host == "" {
		return fmt.Errorf("SMTP_HOST 未配置")
	}
	port := strings.TrimSpace(os.Getenv("SMTP_PORT"))
	if port == "" {
		port = "587"
	}
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("SMTP_PORT 配置错误")
	}
	user := strings.TrimSpace(os.Getenv("SMTP_USER"))
	password := strings.TrimSpace(os.Getenv("SMTP_PASS"))
	from := strings.TrimSpace(os.Getenv("SMTP_FROM"))
	if from == "" {
		from = user
	}
	if from == "" {
		return fmt.Errorf("SMTP_FROM 或 SMTP_USER 不能为空")
	}

	to := make([]string, 0)
	for _, one := range strings.Split(toRaw, ",") {
		item := strings.TrimSpace(one)
		if item != "" {
			to = append(to, item)
		}
	}
	if len(to) == 0 {
		return fmt.Errorf("邮件接收人为空")
	}

	header := []string{
		fmt.Sprintf("From: %s", from),
		fmt.Sprintf("To: %s", strings.Join(to, ",")),
		fmt.Sprintf("Subject: %s", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		content,
	}
	message := []byte(strings.Join(header, "\r\n"))

	addr := fmt.Sprintf("%s:%s", host, port)
	var auth smtp.Auth
	if user != "" {
		auth = smtp.PlainAuth("", user, password, host)
	}

	// SMTP_REQUIRE_TLS=true（默认）强制使用 TLS 连接
	requireTLS := strings.ToLower(strings.TrimSpace(os.Getenv("SMTP_REQUIRE_TLS"))) != "false"
	if requireTLS {
		return sendEmailWithTLS(addr, host, port, auth, from, to, message)
	}
	return smtp.SendMail(addr, auth, from, to, message)
}

// sendEmailWithTLS 强制使用 TLS 发送邮件
func sendEmailWithTLS(addr, host, port string, auth smtp.Auth, from string, to []string, msg []byte) error {
	tlsConfig := &tls.Config{ServerName: host}

	if port == "465" {
		// 隐式 TLS（SMTPS）
		conn, err := tls.Dial("tcp", addr, tlsConfig)
		if err != nil {
			return fmt.Errorf("TLS 连接失败: %w", err)
		}
		defer conn.Close() //nolint:errcheck
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			return fmt.Errorf("创建 SMTP 客户端失败: %w", err)
		}
		defer c.Close() //nolint:errcheck
		return smtpSend(c, auth, from, to, msg)
	}

	// 显式 TLS（STARTTLS，端口 587 等）
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("SMTP 连接失败: %w", err)
	}
	defer c.Close() //nolint:errcheck
	if ok, _ := c.Extension("STARTTLS"); !ok {
		return fmt.Errorf("SMTP 服务器不支持 STARTTLS，拒绝发送（设置 SMTP_REQUIRE_TLS=false 可关闭此检查）")
	}
	if err := c.StartTLS(tlsConfig); err != nil {
		return fmt.Errorf("STARTTLS 握手失败: %w", err)
	}
	return smtpSend(c, auth, from, to, msg)
}

// RaiseSLOBreach emits a platform-level alert for an SLO burn-rate breach.
// The alert flows through the standard silence/grouping/retry pipeline with
// ErrorCode = "XR-SLO-<id>" and NodeID=0 sentinel for "platform" scope.
func RaiseSLOBreach(db *gorm.DB, def *model.SLODefinition, c *slo.Compliance) error {
	severity := "warning"
	if c.ErrorBudgetRemainingPct <= 0 {
		severity = "critical"
	}
	alert := &model.Alert{
		NodeID:    0,
		NodeName:  "platform",
		ErrorCode: fmt.Sprintf("XR-SLO-%d", def.ID),
		Severity:  severity,
		Status:    "open",
		Message: fmt.Sprintf(
			"SLO %q: observed %.2f%% < threshold %.2f%%, 1h burn rate %.2f",
			def.Name, c.Observed*100, def.Threshold*100, c.BurnRate1h,
		),
		TriggeredAt: time.Now(),
	}
	return raiseAndDispatch(db, alert)
}

func smtpSend(c *smtp.Client, auth smtp.Auth, from string, to []string, msg []byte) error {
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, addr := range to {
		if err := c.Rcpt(addr); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
