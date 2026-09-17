package alerting

import (
	"testing"
	"time"
)

func TestGrouping_FirstAlertRegistersAndReturnsShouldSend(t *testing.T) {
	g := NewGrouping(5 * time.Minute)
	if !g.ShouldSend("k1", 1) {
		t.Fatal("first alert must be sent")
	}
}

func TestGrouping_SecondAlertWithinWindowIsSuppressed(t *testing.T) {
	g := NewGrouping(5 * time.Minute)
	_ = g.ShouldSend("k1", 1)
	if g.ShouldSend("k1", 2) {
		t.Fatal("second alert within window must be suppressed")
	}
	if got := g.Count("k1"); got != 2 {
		t.Fatalf("count=%d, want 2", got)
	}
}

func TestGrouping_DifferentKeysDoNotCollide(t *testing.T) {
	g := NewGrouping(5 * time.Minute)
	if !g.ShouldSend("a", 1) {
		t.Fatal("a: first → send")
	}
	if !g.ShouldSend("b", 2) {
		t.Fatal("b: first → send")
	}
}

func TestGrouping_WindowExpiryResetsKey(t *testing.T) {
	g := NewGrouping(10 * time.Millisecond)
	_ = g.ShouldSend("x", 1)
	// time.Sleep 等待 10ms 窗口过期；Grouping.ShouldSend 内部直接调用
	// time.Now()，无可注入时钟接口。25ms > 窗口 + 调度抖动，充足裕量。
	time.Sleep(25 * time.Millisecond)
	if !g.ShouldSend("x", 2) {
		t.Fatal("after window expired, next alert must send as first again")
	}
}

func TestGrouping_ReplayOfFirstAlertIsIdempotent(t *testing.T) {
	g := NewGrouping(5 * time.Minute)
	if !g.ShouldSend("k1", 42) {
		t.Fatal("first alert must own the grouping slot")
	}
	if !g.ShouldSend("k1", 42) {
		t.Fatal("same alert replay must retain ownership")
	}
	if got := g.Count("k1"); got != 1 {
		t.Fatalf("same alert replay count=%d, want 1", got)
	}
	if g.ShouldSend("k1", 43) {
		t.Fatal("distinct alert must be suppressed by first alert")
	}
	if got := g.Count("k1"); got != 2 {
		t.Fatalf("distinct alert count=%d, want 2", got)
	}
}

func TestGroupKey_NodeAndCategoryAndTags(t *testing.T) {
	k1 := GroupKey("probe_down", 1, []string{"web", "prod"})
	k2 := GroupKey("probe_down", 1, []string{"prod", "web"})
	if k1 != k2 {
		t.Fatalf("tag order must be canonicalized: %q vs %q", k1, k2)
	}
	k3 := GroupKey("probe_down", 2, []string{"prod", "web"})
	if k1 == k3 {
		t.Fatal("different node ids must produce different keys")
	}
	k4 := GroupKey("backup_failed", 1, []string{"prod", "web"})
	if k1 == k4 {
		t.Fatal("different category must produce different keys")
	}
}
