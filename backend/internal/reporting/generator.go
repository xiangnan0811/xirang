package reporting

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/model"
	"xirang/backend/internal/util"

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

	// 5. 发送到通知渠道。
	//
	// Wave 2 (PR-C C1) 之前：裸 `go sendReport(...)`，Scheduler.Stop() 时
	// in-flight goroutine 被丢弃，进程退出时强制中断 SMTP/HTTP，可能让接收端
	// 看到半途投递。
	//
	// 现在：使用包级 dispatcher（默认 globalDispatcher）跟踪 in-flight 计数
	// 并在 Shutdown 时阻塞等其完成；测试可注入 nilDispatcher 跳过。
	getDispatcher().Dispatch(context.Background(), db, cfg, report)

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

// sendReport 真正完成一份 SLA 报告的对外通知。
//
// ctx 控制循环的提前退出（dispatcher.Shutdown 时取消）。每个 channel send()
// 之前先检查 ctx.Err()，一旦 dispatcher 在收尾就停止剩余通道，避免进程退出
// 阶段还在拨 SMTP / 推 webhook（接收端会看到截断的请求）。
func sendReport(ctx context.Context, db *gorm.DB, cfg model.ReportConfig, report *model.Report) {
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
		// Wave 2 (PR-C C1): 在每个外发动作前检查 ctx，让 Shutdown 能立即停止
		// 剩余的 SMTP/HTTP 投递，避免进程退出时投递半截。
		if ctx.Err() != nil {
			log.Printf("报告发送中断（config=%d, report=%d）: %v", cfg.ID, report.ID, ctx.Err())
			return
		}
		var integration model.Integration
		if err := db.First(&integration, intID).Error; err != nil {
			log.Printf("报告发送：通知渠道 %d 不存在", intID)
			continue
		}
		if !integration.Enabled {
			continue
		}
		if err := alerting.SendAlert(integration, alertMsg); err != nil {
			// Wave 2 (PR-C C2): err.Error() 可能含 webhook URL（带 access_token）
			// 或 SMTP 异常文本中的内部凭证。SLA 报告会广播给配置的所有渠道，
			// 走 alerting 同一个 sanitize 函数避免泄露给外部接收端的日志或下次
			// 报警去重 key。
			log.Printf("报告发送失败（渠道 %d）: %s", intID, util.SanitizeError(err))
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
//
// Wave 2 (PR-C C1): Scheduler 拥有一个 reportDispatcher，所有由 cron 触发以及
// 由 API GenerateNow 路径产生的"发通知"goroutine 都通过它启动并被 wg 跟踪。
// Shutdown 会先取消 dispatcher 内部 ctx 让在飞 sendReport 提前退出，再等所有
// goroutine 完成才返回，确保进程退出时不会丢失/截断对外通知。
type Scheduler struct {
	db         *gorm.DB
	done       chan struct{}
	dispatcher *reportDispatcher
}

func NewScheduler(db *gorm.DB) *Scheduler {
	return &Scheduler{
		db:         db,
		done:       make(chan struct{}),
		dispatcher: newReportDispatcher(),
	}
}

// Run drives the scheduler until ctx is done. Implements lifecycle.Worker.
func (s *Scheduler) Run(ctx context.Context) {
	// 为本 Scheduler 实例期间安装其专属 dispatcher，让 Generate() 调用走这条
	// 路径完成跟踪；Shutdown 之后恢复全局 default。
	prev := swapDispatcher(s.dispatcher)
	defer swapDispatcher(prev)

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
//
// 在等 Run 退出之后，会再 Shutdown 内嵌 dispatcher：取消其 ctx 让 in-flight
// sendReport 看到 ctx.Err() 并尽早返回，再等所有通知 goroutine 完成。这避免
// "进程退出时 SMTP STARTTLS 协商一半被截断"的体验。
func (s *Scheduler) Shutdown(ctx context.Context) error {
	select {
	case <-s.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.dispatcher.Shutdown(ctx)
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

// reportDispatcher 跟踪所有 sendReport goroutine 的生命周期。
//
// Wave 2 (PR-C C1) 引入。原来 Generate() 直接 `go sendReport(...)` 导致：
//   - Scheduler.Shutdown 时 in-flight 通知被丢弃
//   - 进程退出时 SMTP/HTTP 半途中断
//
// 现在所有外发都通过 dispatcher.Dispatch：内部 wg.Add 并启动一个监听 ctx 的
// goroutine。Shutdown 取消 ctx 让循环看到 ctx.Err() 提前退出，然后 wg.Wait
// 等 goroutine 完成。
type reportDispatcher struct {
	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

func newReportDispatcher() *reportDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &reportDispatcher{ctx: ctx, cancel: cancel}
}

// Dispatch 启动一个跟踪计数的 sendReport goroutine。parentCtx 仅用于在它
// 比 dispatcher.ctx 更早结束时直接放弃；通常调用方传 context.Background()。
func (d *reportDispatcher) Dispatch(parentCtx context.Context, db *gorm.DB, cfg model.ReportConfig, report *model.Report) {
	if d == nil {
		return
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		// 合并 parentCtx 与 dispatcher.ctx：任一取消都让 sendReport 提前退出。
		ctx, cancel := context.WithCancel(d.ctx)
		defer cancel()
		if parentCtx != nil && parentCtx != context.Background() {
			go func() {
				select {
				case <-parentCtx.Done():
					cancel()
				case <-ctx.Done():
				}
			}()
		}
		sendReport(ctx, db, cfg, report)
	}()
}

// Shutdown 触发取消并等所有 in-flight 完成或 stopCtx 过期。
func (d *reportDispatcher) Shutdown(stopCtx context.Context) error {
	if d == nil {
		return nil
	}
	d.cancel()
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-stopCtx.Done():
		return stopCtx.Err()
	}
}

// 全局 dispatcher 是 fallback，用于未被 Scheduler 接管的调用路径（例如 API
// GenerateNow 在 Scheduler 启动前发起）。同时也是测试默认值。
var (
	dispatcherMu      sync.RWMutex
	currentDispatcher = newReportDispatcher()
)

// getDispatcher 返回当前活跃的 dispatcher。Scheduler.Run 期间会把它换为
// Scheduler 的本地实例，让 Shutdown 能精确等到本次任务的所有通知完成。
func getDispatcher() *reportDispatcher {
	dispatcherMu.RLock()
	defer dispatcherMu.RUnlock()
	return currentDispatcher
}

// swapDispatcher 替换当前 dispatcher，返回旧的。Scheduler.Run 在启动时调用以
// 接管全局；Run 退出（Shutdown 等返回）后用 defer 恢复 prev。
func swapDispatcher(d *reportDispatcher) *reportDispatcher {
	dispatcherMu.Lock()
	defer dispatcherMu.Unlock()
	prev := currentDispatcher
	if d != nil {
		currentDispatcher = d
	}
	return prev
}
