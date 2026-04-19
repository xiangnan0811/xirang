package alerting

import (
	"testing"
	"time"
)

func TestGrouping_FirstAlertRegistersAndReturnsShouldSend(t *testing.T) {
	g := NewGrouping(5 * time.Minute)
	if !g.ShouldSend("k1") {
		t.Fatal("first alert must be sent")
	}
}

func TestGrouping_SecondAlertWithinWindowIsSuppressed(t *testing.T) {
	g := NewGrouping(5 * time.Minute)
	_ = g.ShouldSend("k1")
	if g.ShouldSend("k1") {
		t.Fatal("second alert within window must be suppressed")
	}
	if got := g.Count("k1"); got != 2 {
		t.Fatalf("count=%d, want 2", got)
	}
}

func TestGrouping_DifferentKeysDoNotCollide(t *testing.T) {
	g := NewGrouping(5 * time.Minute)
	if !g.ShouldSend("a") {
		t.Fatal("a: first → send")
	}
	if !g.ShouldSend("b") {
		t.Fatal("b: first → send")
	}
}

func TestGrouping_WindowExpiryResetsKey(t *testing.T) {
	g := NewGrouping(10 * time.Millisecond)
	_ = g.ShouldSend("x")
	time.Sleep(25 * time.Millisecond)
	if !g.ShouldSend("x") {
		t.Fatal("after window expired, next alert must send as first again")
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
