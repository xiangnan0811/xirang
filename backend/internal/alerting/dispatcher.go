package alerting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

var httpClient = &http.Client{Timeout: 8 * time.Second}

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

func RaiseTaskFailure(db *gorm.DB, task model.Task, message string) error {
	errorCode := fmt.Sprintf("XR-EXEC-%04d", task.ID)
	policyName := ""
	if task.Policy != nil {
		policyName = task.Policy.Name
	}
	alert := model.Alert{
		NodeID:      task.NodeID,
		NodeName:    task.Node.Name,
		TaskID:      &task.ID,
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
	errorCode := fmt.Sprintf("XR-NODE-%04d", node.ID)
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
			delivery := model.AlertDelivery{
				AlertID:       alert.ID,
				IntegrationID: ch.ID,
			}
			if err != nil {
				delivery.Status = "failed"
				delivery.Error = err.Error()
			} else {
				delivery.Status = "sent"
			}
			_ = db.Create(&delivery).Error
		}(channel)
	}
	wg.Wait()

	// 更新 last_notified_at（在所有发送完成后）
	var sentCount int64
	db.Model(&model.AlertDelivery{}).Where("alert_id = ? AND status = ?", alert.ID, "sent").Count(&sentCount)
	if sentCount > 0 {
		notifiedAt := time.Now()
		alert.LastNotifiedAt = &notifiedAt
		_ = db.Model(alert).Update("last_notified_at", &notifiedAt).Error
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

	switch strings.ToLower(strings.TrimSpace(channel.Type)) {
	case "webhook":
		return postJSON(channel.Endpoint, body)
	case "slack":
		return postJSON(channel.Endpoint, map[string]string{
			"text": fmt.Sprintf("[XiRang][%s] %s (%s)", strings.ToUpper(alert.Severity), alert.Message, alert.ErrorCode),
		})
	case "telegram":
		return postJSON(channel.Endpoint, map[string]string{
			"text": fmt.Sprintf("[XiRang][%s]\n节点: %s\n错误: %s\n说明: %s", strings.ToUpper(alert.Severity), alert.NodeName, alert.ErrorCode, alert.Message),
		})
	case "email":
		subject := fmt.Sprintf("[XiRang][%s] %s", strings.ToUpper(alert.Severity), alert.ErrorCode)
		content := fmt.Sprintf("节点: %s\n策略: %s\n错误码: %s\n详情: %s\n时间: %s\n", alert.NodeName, alert.PolicyName, alert.ErrorCode, alert.Message, alert.TriggeredAt.Format(time.RFC3339))
		return sendEmail(channel.Endpoint, subject, content)
	default:
		return fmt.Errorf("不支持的通知通道类型: %s", channel.Type)
	}
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

func postJSON(url string, body interface{}) error {
	payloadBytes, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("通知发送失败: http %d", resp.StatusCode)
	}
	return nil
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
	return smtp.SendMail(addr, auth, from, to, message)
}
