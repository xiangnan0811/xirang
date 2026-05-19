package probe

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/metrics"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"gorm.io/gorm"
)

// Prober periodically probes all nodes via SSH to check health and collect metrics.
type Prober struct {
	db                  *gorm.DB
	sink                *metrics.FanSink
	interval            time.Duration
	failThreshold       int
	concurrency         int
	metricRetentionDays int
	metricAuditMu       sync.Mutex
	metricAuditFailures map[string]int
	cancel              context.CancelFunc
	done                chan struct{}
}

// NewProber creates a new Prober instance. Metric samples are written through
// sink (typically a FanSink containing at least a DBSink); do not pass nil.
func NewProber(db *gorm.DB, interval time.Duration, failThreshold, concurrency int, sink *metrics.FanSink) *Prober {
	return &Prober{
		db:                  db,
		sink:                sink,
		interval:            interval,
		failThreshold:       failThreshold,
		concurrency:         concurrency,
		metricRetentionDays: 7,
		metricAuditFailures: make(map[string]int),
		done:                make(chan struct{}),
	}
}

// Start begins the periodic probe loop in a background goroutine.
func (p *Prober) Start(ctx context.Context) {
	probeCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	go p.run(probeCtx)
}

// Run starts the periodic probe loop and blocks until ctx is done.
// Implements lifecycle.Worker.
func (p *Prober) Run(ctx context.Context) {
	p.Start(ctx)
	<-ctx.Done()
}

// Shutdown signals the prober to stop and waits for completion.
func (p *Prober) Shutdown(ctx context.Context) error {
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
	if err := p.db.Preload("SSHKey").Where("archived = ?", false).Find(&nodes).Error; err != nil {
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
	result, credential, err := sshutil.ProbeNode(node, p.db)

	if err != nil {
		// Failed
		newFailures := node.ConsecutiveFailures + 1
		outcome := probeAuditOutcome(err)
		if shouldAuditProbeCredentialFailure(node.ConsecutiveFailures, newFailures, p.failThreshold) {
			p.writeSystemCredentialAudit(node, credential, sshutil.PurposeProbe, "probe.ssh", outcome, "probe", err, map[string]any{
				"failure_count": newFailures,
			})
		}
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

	go p.collectAndSaveMetrics(node, result.Latency, float64(diskUsed), float64(diskTotal))
}

type nodeMetrics struct {
	cpuPct  float64
	memPct  float64
	diskPct float64
	load1m  float64
}

func (p *Prober) collectAndSaveMetrics(node model.Node, probeLatencyMs int, diskGBUsed, diskGBTotal float64) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	nm, err := p.collectMetrics(ctx, node)
	if err != nil {
		logger.Module("probe").Warn().Uint("node_id", node.ID).Err(err).Msg("采集节点资源指标失败")
		return
	}

	cpuPct, memPct, diskPct, load1 := nm.cpuPct, nm.memPct, nm.diskPct, nm.load1m
	latency := float64(probeLatencyMs)
	ms := metrics.Sample{
		NodeID:    node.ID,
		NodeName:  node.Name,
		SampledAt: time.Now().UTC(),
		CPUPct:    &cpuPct,
		MemPct:    &memPct,
		DiskPct:   &diskPct,
		Load1:     &load1,
		LatencyMs: &latency,
		ProbeOK:   true,
	}
	if diskGBTotal > 0 {
		used, total := diskGBUsed, diskGBTotal
		ms.DiskGBUsed = &used
		ms.DiskGBTotal = &total
	}
	if err := p.sink.Write(ctx, ms); err != nil {
		// FanSink.Write returns nil today, but honour the contract in case the
		// implementation is swapped for one that returns errors.
		logger.Module("probe").Warn().Uint("node_id", node.ID).Err(err).Msg("保存节点资源指标失败")
		return
	}

	if nm.diskPct > 90 {
		if alertErr := alerting.RaiseDiskUsageAlert(p.db, node, nm.diskPct); alertErr != nil {
			logger.Module("probe").Warn().Uint("node_id", node.ID).Err(alertErr).Msg("创建磁盘告警失败")
		}
	}
}

func (p *Prober) collectMetrics(ctx context.Context, node model.Node) (*nodeMetrics, error) {
	authMethods, credential, err := sshutil.BuildSSHAuthForPurpose(node, p.db, sshutil.PurposeProbe)
	if err != nil {
		p.writeMetricCredentialAudit(node, credential, credentialaudit.OutcomeBlocked, "auth_build", err)
		return nil, fmt.Errorf("构建 SSH 认证失败: %w", err)
	}

	hostKeyCallback, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		p.writeMetricCredentialAudit(node, credential, credentialaudit.OutcomeFailure, "host_key", err)
		return nil, fmt.Errorf("解析主机密钥回调失败: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", node.Host, node.Port)
	client, err := sshutil.DialSSH(ctx, addr, node.Username, authMethods, hostKeyCallback)
	if err != nil {
		p.writeMetricCredentialAudit(node, credential, credentialaudit.OutcomeFailure, "dial", err)
		return nil, err
	}
	defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	defer session.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	// CPU: 用 /proc/stat 两次采样（间隔 0.5s）计算实时使用率，兼容所有 Linux 发行版
	// mem/disk/load 保持原有方式
	cmd := `s1=$(awk '/^cpu /{for(i=2;i<=NF;i++)t+=$i;print $5,t;exit}' /proc/stat);` +
		`sleep 0.5;` +
		`s2=$(awk '/^cpu /{for(i=2;i<=NF;i++)t+=$i;print $5,t;exit}' /proc/stat);` +
		`cpu=$(echo "$s1 $s2"|awk '{di=$3-$1;dt=$4-$2;if(dt>0)printf "%.1f",100*(1-di/dt);else print 0}');` +
		`mem=$(free 2>/dev/null|awk '/^Mem:/{printf "%.1f",$3/$2*100}'||echo 0);` +
		`disk=$(df / 2>/dev/null|awk 'NR==2{gsub(/%/,"",$5);print $5}'||echo 0);` +
		`load=$(awk '{print $1}' /proc/loadavg 2>/dev/null||echo 0);` +
		`echo "${cpu:-0} ${mem:-0} ${disk:-0} ${load:-0}"`
	out, err := session.Output(cmd)
	if err != nil {
		return nil, fmt.Errorf("执行指标采集命令失败: %w", err)
	}

	return parseMetricsOutput(strings.TrimSpace(string(out)))
}

func (p *Prober) writeMetricCredentialAudit(node model.Node, credential sshutil.ResolvedCredential, outcome, stage string, err error) {
	failureCount := p.recordMetricAuditFailure(node.ID, stage)
	if !shouldAuditProbeCredentialFailure(failureCount-1, failureCount, p.failThreshold) {
		return
	}
	p.writeSystemCredentialAudit(node, credential, sshutil.PurposeProbe, "probe.metrics", outcome, stage, err, map[string]any{
		"failure_count": failureCount,
	})
}

func (p *Prober) recordMetricAuditFailure(nodeID uint, stage string) int {
	key := fmt.Sprintf("%d:%s", nodeID, strings.TrimSpace(stage))
	p.metricAuditMu.Lock()
	defer p.metricAuditMu.Unlock()
	if p.metricAuditFailures == nil {
		p.metricAuditFailures = make(map[string]int)
	}
	p.metricAuditFailures[key]++
	return p.metricAuditFailures[key]
}

func (p *Prober) writeSystemCredentialAudit(node model.Node, credential sshutil.ResolvedCredential, purpose, action, outcome, stage string, err error, metadata map[string]any) {
	kind := strings.TrimSpace(credential.Kind)
	if kind == "" {
		switch strings.ToLower(strings.TrimSpace(node.AuthType)) {
		case "password":
			kind = "password"
		case "key":
			if node.SSHKeyID != nil && *node.SSHKeyID != 0 {
				kind = "ssh_key"
			} else if strings.TrimSpace(node.PrivateKey) != "" {
				kind = "node_private_key"
			}
		}
	}
	if kind == "" {
		kind = "unknown"
	}
	source := strings.TrimSpace(credential.Source)
	if source == "" {
		switch kind {
		case "password":
			source = "node.password"
		case "ssh_key":
			if node.SSHKeyID != nil && *node.SSHKeyID != 0 {
				source = fmt.Sprintf("ssh_key_id=%d", *node.SSHKeyID)
			}
		case "node_private_key":
			source = "node.private_key"
		}
	}
	if source == "" {
		source = "unknown"
	}
	keyID := credential.KeyID
	if keyID == nil && kind == "ssh_key" && node.SSHKeyID != nil {
		keyID = node.SSHKeyID
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["stage"] = stage
	if err != nil {
		metadata["has_error"] = true
	}
	event := credentialaudit.Event{
		Username:         "system",
		Role:             "system",
		Action:           action,
		Purpose:          purpose,
		CredentialKind:   kind,
		CredentialSource: source,
		SSHKeyID:         keyID,
		NodeID:           credentialaudit.PtrUint(node.ID),
		Outcome:          outcome,
		Metadata:         metadata,
	}
	if err != nil {
		event.ErrorMessage = credentialAuditSafeProbeError(stage)
	}
	if writeErr := credentialaudit.Write(p.db, event); writeErr != nil {
		logger.Module("credential_audit").Warn().Err(writeErr).
			Str("action", event.Action).
			Str("purpose", event.Purpose).
			Msg("系统凭据审计事件写入失败")
	}
}

func probeAuditOutcome(err error) string {
	if err == nil {
		return credentialaudit.OutcomeSuccess
	}
	if isProbeCredentialBlockedError(err) {
		return credentialaudit.OutcomeBlocked
	}
	return credentialaudit.OutcomeFailure
}

func shouldAuditProbeCredentialFailure(previousFailures, newFailures, threshold int) bool {
	if newFailures <= 0 {
		return false
	}
	if previousFailures < 0 {
		previousFailures = 0
	}
	if newFailures == 1 {
		return true
	}
	if threshold > 0 && previousFailures < threshold && newFailures >= threshold {
		return true
	}
	if threshold <= 0 {
		threshold = 1
	}
	return newFailures > threshold && isPowerOfTwo(newFailures)
}

func isPowerOfTwo(value int) bool {
	return value > 0 && value&(value-1) == 0
}

func isProbeCredentialBlockedError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"构建 ssh 认证失败",
		"ssh key 已禁用",
		"ssh key 已过期",
		"ssh key 不允许",
		"密钥认证",
		"密码认证",
		"不支持的认证方式",
	} {
		if strings.Contains(msg, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func credentialAuditSafeProbeError(stage string) string {
	stage = strings.TrimSpace(stage)
	if stage == "" {
		stage = "probe"
	}
	return fmt.Sprintf("%s failed", stage)
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
