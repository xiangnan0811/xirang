package probe

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"gorm.io/gorm"
)

// Prober periodically probes all nodes via SSH to check health and collect metrics.
type Prober struct {
	db            *gorm.DB
	interval      time.Duration
	failThreshold int
	concurrency   int
	cancel        context.CancelFunc
	done          chan struct{}
}

// NewProber creates a new Prober instance.
func NewProber(db *gorm.DB, interval time.Duration, failThreshold, concurrency int) *Prober {
	return &Prober{
		db:            db,
		interval:      interval,
		failThreshold: failThreshold,
		concurrency:   concurrency,
		done:          make(chan struct{}),
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

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.probeAll()
		}
	}
}

func (p *Prober) probeAll() {
	var nodes []model.Node
	if err := p.db.Preload("SSHKey").Find(&nodes).Error; err != nil {
		log.Printf("warn: 节点探测查询失败: %v", err)
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

func (p *Prober) probeNode(node model.Node) {
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
			log.Printf("warn: 更新节点探测状态失败(node_id=%d): %v", node.ID, dbErr)
		}

		if newFailures >= p.failThreshold {
			if alertErr := alerting.RaiseNodeProbeFailure(p.db, node, fmt.Sprintf("节点连续探测失败 %d 次: %v", newFailures, err)); alertErr != nil {
				log.Printf("warn: 创建节点探测告警失败(node_id=%d): %v", node.ID, alertErr)
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
		log.Printf("warn: 更新节点探测状态失败(node_id=%d): %v", node.ID, dbErr)
	}

	if resolveErr := alerting.ResolveNodeAlerts(p.db, node.ID, "节点探测恢复正常"); resolveErr != nil {
		log.Printf("warn: 恢复节点探测告警失败(node_id=%d): %v", node.ID, resolveErr)
	}
}
