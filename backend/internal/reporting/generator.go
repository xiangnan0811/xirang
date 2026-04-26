package reporting

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// FailureEntry 失败热点条目（Top N）
type FailureEntry struct {
	NodeName string `json:"node_name"`
	TaskName string `json:"task_name"`
	Count    int    `json:"count"`
	LastErr  string `json:"last_err"`
}

// DiskTrendEntry 磁盘趋势采样点
type DiskTrendEntry struct {
	Date    string  `json:"date"`
	AvgFree float64 `json:"avg_free_pct"`
}

// Generate 生成一份 SLA 报告并持久化。
func Generate(db *gorm.DB, cfg model.ReportConfig, start, end time.Time) (*model.Report, error) {
	// 1. 确定作用域节点 ID 列表
	nodeIDs, err := resolveScopeNodeIDs(db, cfg)
	if err != nil {
		return nil, fmt.Errorf("解析作用域失败: %w", err)
	}

	// 2. 聚合 TaskRun 数据
	stats, err := aggregateRuns(db, nodeIDs, start, end)
	if err != nil {
		return nil, err
	}

	// 3. 失败热点 Top 5
	topFailures, err := buildTopFailures(db, nodeIDs, start, end)
	if err != nil {
		return nil, err
	}
	topFailuresJSON, _ := json.Marshal(topFailures)

	// 4. 磁盘趋势
	diskTrend, err := buildDiskTrend(db, nodeIDs, start, end)
	if err != nil {
		return nil, err
	}
	diskTrendJSON, _ := json.Marshal(diskTrend)

	successRate := 0.0
	if stats.Total > 0 {
		successRate = float64(stats.Success) / float64(stats.Total) * 100
	}

	report := &model.Report{
		ConfigID:      cfg.ID,
		PeriodStart:   start,
		PeriodEnd:     end,
		TotalRuns:     stats.Total,
		SuccessRuns:   stats.Success,
		FailedRuns:    stats.Failed,
		SuccessRate:   successRate,
		AvgDurationMs: stats.AvgMs,
		TopFailures:   string(topFailuresJSON),
		DiskTrend:     string(diskTrendJSON),
		GeneratedAt:   time.Now(),
	}

	if err := db.Create(report).Error; err != nil {
		return nil, fmt.Errorf("保存报告失败: %w", err)
	}

	// 5. 发送到通知渠道
	go sendReport(db, cfg, report)

	return report, nil
}

func resolveScopeNodeIDs(db *gorm.DB, cfg model.ReportConfig) ([]uint, error) {
	switch cfg.ScopeType {
	case "all":
		var ids []uint
		if err := db.Model(&model.Node{}).Pluck("id", &ids).Error; err != nil {
			return nil, err
		}
		return ids, nil
	case "tag":
		tag := strings.TrimSpace(cfg.ScopeValue)
		var nodes []model.Node
		if err := db.Where("tags LIKE ?", "%"+tag+"%").Find(&nodes).Error; err != nil {
			return nil, err
		}
		ids := make([]uint, 0, len(nodes))
		for _, n := range nodes {
			ids = append(ids, n.ID)
		}
		return ids, nil
	case "node_ids":
		var ids []uint
		if err := json.Unmarshal([]byte(cfg.ScopeValue), &ids); err != nil {
			return nil, fmt.Errorf("node_ids JSON 解析失败: %w", err)
		}
		return ids, nil
	default:
		return nil, fmt.Errorf("未知作用域类型: %s", cfg.ScopeType)
	}
}

type runAgg struct {
	Total   int
	Success int
	Failed  int
	AvgMs   int64
}

func aggregateRuns(db *gorm.DB, nodeIDs []uint, start, end time.Time) (runAgg, error) {
	if len(nodeIDs) == 0 {
		return runAgg{}, nil
	}
	type row struct {
		Total   int     `gorm:"column:total"`
		Success int     `gorm:"column:success_count"`
		Failed  int     `gorm:"column:failed_count"`
		AvgMs   float64 `gorm:"column:avg_ms"`
	}
	var r row
	err := db.Table("task_runs").
		Select("COUNT(*) as total, SUM(CASE WHEN task_runs.status='success' THEN 1 ELSE 0 END) as success_count, SUM(CASE WHEN task_runs.status='failed' THEN 1 ELSE 0 END) as failed_count, AVG(task_runs.duration_ms) as avg_ms").
		Joins("JOIN tasks ON tasks.id = task_runs.task_id").
		Where("tasks.node_id IN ? AND task_runs.started_at >= ? AND task_runs.started_at < ?", nodeIDs, start, end).
		Scan(&r).Error
	if err != nil {
		return runAgg{}, fmt.Errorf("聚合 TaskRun 失败: %w", err)
	}
	return runAgg{
		Total:   r.Total,
		Success: r.Success,
		Failed:  r.Failed,
		AvgMs:   int64(r.AvgMs),
	}, nil
}

func buildTopFailures(db *gorm.DB, nodeIDs []uint, start, end time.Time) ([]FailureEntry, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	type row struct {
		NodeName string `gorm:"column:node_name"`
		TaskName string `gorm:"column:task_name"`
		Count    int    `gorm:"column:cnt"`
		LastErr  string `gorm:"column:last_err"`
	}
	var rows []row
	err := db.Table("task_runs").
		Select("nodes.name as node_name, tasks.name as task_name, COUNT(*) as cnt, MAX(task_runs.last_error) as last_err").
		Joins("JOIN tasks ON tasks.id = task_runs.task_id").
		Joins("JOIN nodes ON nodes.id = tasks.node_id").
		Where("tasks.node_id IN ? AND task_runs.status='failed' AND task_runs.started_at >= ? AND task_runs.started_at < ?", nodeIDs, start, end).
		Group("tasks.node_id, task_runs.task_id").
		Order("cnt desc").
		Limit(5).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询失败热点失败: %w", err)
	}
	entries := make([]FailureEntry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, FailureEntry(r))
	}
	return entries, nil
}

func buildDiskTrend(db *gorm.DB, nodeIDs []uint, start, end time.Time) ([]DiskTrendEntry, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	type row struct {
		Date    string  `gorm:"column:date_label"`
		AvgFree float64 `gorm:"column:avg_free"`
	}
	// SQLite: date(sampled_at), PostgreSQL: DATE(sampled_at) — 均兼容
	var rows []row
	err := db.Table("node_metric_samples").
		Select("DATE(sampled_at) as date_label, AVG(100 - disk_pct) as avg_free").
		Where("node_id IN ? AND sampled_at >= ? AND sampled_at < ?", nodeIDs, start, end).
		Group("date_label").
		Order("date_label asc").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询磁盘趋势失败: %w", err)
	}
	entries := make([]DiskTrendEntry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, DiskTrendEntry(r))
	}
	return entries, nil
}

func sendReport(db *gorm.DB, cfg model.ReportConfig, report *model.Report) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("报告发送 panic（config=%d, report=%d）: %v", cfg.ID, report.ID, r)
		}
	}()
	var integrationIDs []uint
	if err := json.Unmarshal([]byte(cfg.IntegrationIDs), &integrationIDs); err != nil || len(integrationIDs) == 0 {
		return
	}

	body := buildReportMessage(cfg, report)
	alertMsg := model.Alert{
		NodeName:    "XiRang SLA Report",
		Severity:    "info",
		Status:      "open",
		ErrorCode:   fmt.Sprintf("XR-REPORT-%d", report.ID),
		Message:     body,
		TriggeredAt: time.Now(),
	}

	for _, intID := range integrationIDs {
		var integration model.Integration
		if err := db.First(&integration, intID).Error; err != nil {
			log.Printf("报告发送：通知渠道 %d 不存在", intID)
			continue
		}
		if !integration.Enabled {
			continue
		}
		if err := alerting.SendAlert(integration, alertMsg); err != nil {
			log.Printf("报告发送失败（渠道 %d）: %v", intID, err)
		}
	}
}

func buildReportMessage(cfg model.ReportConfig, report *model.Report) string {
	return fmt.Sprintf(
		"【SLA 报告】%s\n期间：%s ~ %s\n成功率：%.1f%%（%d/%d）\n平均耗时：%dms",
		cfg.Name,
		report.PeriodStart.Format("2006-01-02"),
		report.PeriodEnd.Format("2006-01-02"),
		report.SuccessRate,
		report.SuccessRuns,
		report.TotalRuns,
		report.AvgDurationMs,
	)
}

// Scheduler 定时报告调度器，在应用启动时启动。
type Scheduler struct {
	db   *gorm.DB
	ctx  context.Context
	done chan struct{}
}

func NewScheduler(ctx context.Context, db *gorm.DB) *Scheduler {
	return &Scheduler{db: db, ctx: ctx, done: make(chan struct{})}
}

// Start launches the scheduler using the ctx supplied at construction.
// Deprecated: prefer go s.Run(ctx) directly for the lifecycle.Worker pattern.
// TODO(task-12): remove once main.go is migrated to lifecycle.Worker slice.
func (s *Scheduler) Start() {
	go s.Run(s.ctx)
}

// Run drives the scheduler until ctx is done. Implements lifecycle.Worker.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer close(s.done) // fires last
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			s.checkAndGenerate(t)
		}
	}
}

// Shutdown blocks until Run returns or stopCtx expires. Implements
// lifecycle.Worker.
func (s *Scheduler) Shutdown(ctx context.Context) error {
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Scheduler) checkAndGenerate(now time.Time) {
	var configs []model.ReportConfig
	if err := s.db.Where("enabled = ?", true).Find(&configs).Error; err != nil {
		return
	}
	for _, cfg := range configs {
		if shouldGenerate(cfg, now) {
			var start time.Time
			if cfg.Period == "monthly" {
				start = now.AddDate(0, -1, 0)
			} else {
				start = now.AddDate(0, 0, -7)
			}
			if _, err := Generate(s.db, cfg, start, now); err != nil {
				log.Printf("定时报告生成失败（config=%d）: %v", cfg.ID, err)
			}
		}
	}
}

// shouldGenerate 简单 cron 匹配：解析 "分 时 * * 星期几" 格式。
// 标准 cron 解析可用 robfig/cron，此处用简单检查避免引入依赖。
func shouldGenerate(cfg model.ReportConfig, now time.Time) bool {
	parts := strings.Fields(cfg.Cron)
	if len(parts) != 5 {
		return false
	}
	minute := now.Minute()
	hour := now.Hour()
	weekday := int(now.Weekday()) // 0=Sunday
	dayOfMonth := now.Day()

	if !matchField(parts[0], minute) {
		return false
	}
	if !matchField(parts[1], hour) {
		return false
	}
	// parts[2] = day of month, parts[3] = month, parts[4] = day of week
	if parts[2] != "*" && !matchField(parts[2], dayOfMonth) {
		return false
	}
	if parts[3] != "*" {
		return false
	}
	if parts[4] != "*" && !matchField(parts[4], weekday) {
		return false
	}
	return true
}

func matchField(expr string, value int) bool {
	if expr == "*" {
		return true
	}
	var n int
	_, err := fmt.Sscanf(expr, "%d", &n)
	return err == nil && n == value
}
