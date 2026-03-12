package probe

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"gorm.io/gorm"
)

// Prober periodically probes all nodes via SSH to check health and collect metrics.
type Prober struct {
	db                   *gorm.DB
	interval             time.Duration
	failThreshold        int
	concurrency          int
	metricRetentionDays  int
	cancel               context.CancelFunc
	done                 chan struct{}
}

// NewProber creates a new Prober instance.
func NewProber(db *gorm.DB, interval time.Duration, failThreshold, concurrency int) *Prober {
	return &Prober{
		db:                  db,
		interval:            interval,
		failThreshold:       failThreshold,
		concurrency:         concurrency,
		metricRetentionDays: 7,
		done:                make(chan struct{}),
	}
}

// Start begins the periodic probe loop in a background goroutine.
func (p *Prober) Start(ctx context.Context) {
	probeCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	go p.run(probeCtx)
}

// Stop signals the prober to stop and waits for completion.
func (p *Prober) Stop(ctx context.Context) error {
	if p.cancel != nil {
		p.cancel()
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Prober) run(ctx context.Context) {
	defer close(p.done)

	// Run immediately on start
	p.probeAll()

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	cleanupTicker := time.NewTicker(24 * time.Hour)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.probeAll()
		case <-cleanupTicker.C:
			p.cleanupOldMetrics()
		}
	}
}

func (p *Prober) probeAll() {
	var nodes []model.Node
	if err := p.db.Preload("SSHKey").Find(&nodes).Error; err != nil {
		logger.Module("probe").Warn().Err(err).Msg("节点探测查询失败")
		return
	}

	if len(nodes) == 0 {
		return
	}

	work := make(chan model.Node, len(nodes))
	for _, node := range nodes {
		work <- node
	}
	close(work)

	var wg sync.WaitGroup
	for i := 0; i < p.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range work {
				p.probeNode(n)
			}
		}()
	}

	wg.Wait()
}

func isInMaintenanceWindow(node model.Node) bool {
	if node.MaintenanceStart == nil || node.MaintenanceEnd == nil {
		return false
	}
	now := time.Now().UTC()
	return now.After(*node.MaintenanceStart) && now.Before(*node.MaintenanceEnd)
}

func (p *Prober) probeNode(node model.Node) {
	// 维护窗口内跳过探测和告警
	if isInMaintenanceWindow(node) {
		return
	}

	now := time.Now()
	result, err := sshutil.ProbeNode(node, p.db)

	if err != nil {
		// Failed
		newFailures := node.ConsecutiveFailures + 1
		updates := map[string]interface{}{
			"status":               "offline",
			"connection_latency":   0,
			"last_probe_at":        now,
			"consecutive_failures": newFailures,
		}
		if dbErr := p.db.Model(&node).Updates(updates).Error; dbErr != nil {
			logger.Module("probe").Warn().Uint("node_id", node.ID).Err(dbErr).Msg("更新节点探测状态失败")
		}

		if newFailures >= p.failThreshold {
			if alertErr := alerting.RaiseNodeProbeFailure(p.db, node, fmt.Sprintf("节点连续探测失败 %d 次: %v", newFailures, err)); alertErr != nil {
				logger.Module("probe").Warn().Uint("node_id", node.ID).Err(alertErr).Msg("创建节点探测告警失败")
			}
		}
		return
	}

	// Success
	diskUsed := result.DiskUsed
	diskTotal := result.DiskTotal
	if diskTotal > 0 {
		if diskUsed < 0 {
			diskUsed = 0
		}
		if diskUsed > diskTotal {
			diskUsed = diskTotal
		}
	} else {
		diskUsed = 0
	}

	updates := map[string]interface{}{
		"status":               "online",
		"connection_latency":   result.Latency,
		"disk_used_gb":         diskUsed,
		"disk_total_gb":        diskTotal,
		"last_probe_at":        now,
		"last_seen_at":         now,
		"consecutive_failures": 0,
	}
	if dbErr := p.db.Model(&node).Updates(updates).Error; dbErr != nil {
		logger.Module("probe").Warn().Uint("node_id", node.ID).Err(dbErr).Msg("更新节点探测状态失败")
	}

	if resolveErr := alerting.ResolveNodeAlerts(p.db, node.ID, "节点探测恢复正常"); resolveErr != nil {
		logger.Module("probe").Warn().Uint("node_id", node.ID).Err(resolveErr).Msg("恢复节点探测告警失败")
	}

	go p.collectAndSaveMetrics(node)
}

type nodeMetrics struct {
	cpuPct  float64
	memPct  float64
	diskPct float64
	load1m  float64
}

func (p *Prober) collectAndSaveMetrics(node model.Node) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	metrics, err := p.collectMetrics(ctx, node)
	if err != nil {
		logger.Module("probe").Warn().Uint("node_id", node.ID).Err(err).Msg("采集节点资源指标失败")
		return
	}

	sample := model.NodeMetricSample{
		NodeID:    node.ID,
		CpuPct:    metrics.cpuPct,
		MemPct:    metrics.memPct,
		DiskPct:   metrics.diskPct,
		Load1m:    metrics.load1m,
		SampledAt: time.Now().UTC(),
	}
	if err := p.db.Create(&sample).Error; err != nil {
		logger.Module("probe").Warn().Uint("node_id", node.ID).Err(err).Msg("保存节点资源指标失败")
		return
	}

	if metrics.diskPct > 90 {
		if alertErr := alerting.RaiseDiskUsageAlert(p.db, node, metrics.diskPct); alertErr != nil {
			logger.Module("probe").Warn().Uint("node_id", node.ID).Err(alertErr).Msg("创建磁盘告警失败")
		}
	}
}

func (p *Prober) collectMetrics(ctx context.Context, node model.Node) (*nodeMetrics, error) {
	authMethods, err := sshutil.BuildSSHAuth(node, p.db)
	if err != nil {
		return nil, fmt.Errorf("构建 SSH 认证失败: %w", err)
	}

	hostKeyCallback, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		return nil, fmt.Errorf("解析主机密钥回调失败: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", node.Host, node.Port)
	client, err := sshutil.DialSSH(ctx, addr, node.Username, authMethods, hostKeyCallback)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	defer session.Close()

	cmd := `cpu=$(top -bn1 2>/dev/null | grep -i "cpu" | head -1 | awk '{for(i=1;i<=NF;i++){if($i~/id,?/){gsub(/[^0-9.]/,"",$i);print 100-$i;exit}}}' 2>/dev/null || echo 0); mem=$(free 2>/dev/null | awk '/^Mem:/{printf "%.1f", $3/$2*100}' || echo 0); disk=$(df / 2>/dev/null | awk 'NR==2{gsub(/%/,"",$5); print $5}' || echo 0); load=$(cat /proc/loadavg 2>/dev/null | awk '{print $1}' || echo 0); echo "$cpu $mem $disk $load"`
	out, err := session.Output(cmd)
	if err != nil {
		return nil, fmt.Errorf("执行指标采集命令失败: %w", err)
	}

	return parseMetricsOutput(strings.TrimSpace(string(out)))
}

func parseMetricsOutput(output string) (*nodeMetrics, error) {
	fields := strings.Fields(output)
	if len(fields) < 4 {
		return nil, fmt.Errorf("指标输出格式不符: %q", output)
	}

	parseFloat := func(s string) float64 {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || v < 0 {
			return 0
		}
		return v
	}

	return &nodeMetrics{
		cpuPct:  parseFloat(fields[0]),
		memPct:  parseFloat(fields[1]),
		diskPct: parseFloat(fields[2]),
		load1m:  parseFloat(fields[3]),
	}, nil
}

func (p *Prober) cleanupOldMetrics() {
	cutoff := time.Now().UTC().AddDate(0, 0, -p.metricRetentionDays)
	if err := p.db.Where("sampled_at < ?", cutoff).Delete(&model.NodeMetricSample{}).Error; err != nil {
		logger.Module("probe").Warn().Err(err).Msg("清理过期节点指标失败")
	}
}
