package alerting

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type groupState struct {
	firstSeenAt  time.Time
	firstAlertID uint
	alertCount   int
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

// ShouldSend reports whether the persistent alert owns the first-send slot
// for this key in the current window. Replaying the same alert after a
// durable routing failure is idempotent: it may reclaim its own first slot
// without incrementing the group count. A distinct alert in the window is
// counted and suppressed.
func (g *Grouping) ShouldSend(key string, alertID uint) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if st, ok := g.active[key]; ok && now.Sub(st.firstSeenAt) < g.window {
		if alertID != 0 && st.firstAlertID == alertID {
			return true
		}
		st.alertCount++
		return false
	}
	g.active[key] = &groupState{firstSeenAt: now, firstAlertID: alertID, alertCount: 1}
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

var sharedGrouping atomic.Pointer[Grouping]

func init() {
	sharedGrouping.Store(NewGrouping(5 * time.Minute))
}

// GetSharedGrouping returns the current package-level Grouping instance.
// Safe to call concurrently; tests may replace the instance atomically.
func GetSharedGrouping() *Grouping {
	return sharedGrouping.Load()
}

// SetSharedGroupingForTest atomically replaces SharedGrouping. Intended for tests.
// Keep exported-ness minimal — this should live in a _test.go if possible, but
// dispatcher_test.go and handler tests need cross-package access so export.
func SetSharedGroupingForTest(g *Grouping) {
	sharedGrouping.Store(g)
}
