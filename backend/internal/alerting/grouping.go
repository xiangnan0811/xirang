package alerting

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type groupState struct {
	firstSeenAt time.Time
	alertCount  int
}

// Grouping 持有内存中的渐进式分组状态，线程安全。
// 进程重启会清除所有状态；最坏情况是重启后某条被抑制的告警被重新投递，可接受。
type Grouping struct {
	window time.Duration
	mu     sync.Mutex
	active map[string]*groupState
}

func NewGrouping(window time.Duration) *Grouping {
	return &Grouping{window: window, active: map[string]*groupState{}}
}

// ShouldSend 报告本次告警是否为窗口内的首次出现。
// 首次出现时注册 key 并调度清理；窗口内重复出现时递增计数并返回 false。
func (g *Grouping) ShouldSend(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if st, ok := g.active[key]; ok && now.Sub(st.firstSeenAt) < g.window {
		st.alertCount++
		return false
	}
	g.active[key] = &groupState{firstSeenAt: now, alertCount: 1}
	time.AfterFunc(g.window, func() {
		g.mu.Lock()
		if st, ok := g.active[key]; ok && time.Since(st.firstSeenAt) >= g.window {
			delete(g.active, key)
		}
		g.mu.Unlock()
	})
	return true
}

// Count 返回当前窗口内该 key 累计的告警次数（含首次）。
func (g *Grouping) Count(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st, ok := g.active[key]; ok {
		return st.alertCount
	}
	return 0
}

// GroupKey 构建用于分组的规范化 key：category + nodeID + sorted(tags)。
func GroupKey(category string, nodeID uint, nodeTags []string) string {
	tags := append([]string(nil), nodeTags...)
	sort.Strings(tags)
	raw := category + "|" + strconv.FormatUint(uint64(nodeID), 10) + "|" + strings.Join(tags, ",")
	sum := sha1.Sum([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// SharedGrouping 是分发器使用的包级分组实例。导出以便 alert handler 读取分组计数。
var SharedGrouping = NewGrouping(5 * time.Minute)
