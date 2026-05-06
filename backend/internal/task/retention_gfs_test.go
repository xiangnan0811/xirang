package task

import (
	"testing"

	"xirang/backend/internal/model"
)

func TestBuildGFSKeepArgs_DailyOnly(t *testing.T) {
	p := model.Policy{
		RetentionMode: "gfs",
		KeepDaily:     7,
	}
	got := buildGFSKeepArgs(p)
	expected := "--keep-daily 7"
	if got != expected {
		t.Fatalf("期望 %q，实际 %q", expected, got)
	}
}

func TestBuildGFSKeepArgs_AllLevels(t *testing.T) {
	p := model.Policy{
		RetentionMode: "gfs",
		KeepDaily:     7,
		KeepWeekly:    4,
		KeepMonthly:   12,
		KeepYearly:    2,
	}
	got := buildGFSKeepArgs(p)
	expected := "--keep-daily 7 --keep-weekly 4 --keep-monthly 12 --keep-yearly 2"
	if got != expected {
		t.Fatalf("期望 %q，实际 %q", expected, got)
	}
}

func TestBuildGFSKeepArgs_AllZerosFallback(t *testing.T) {
	p := model.Policy{
		RetentionMode: "gfs",
		// 所有 keep 字段均为 0
	}
	got := buildGFSKeepArgs(p)
	expected := "--keep-within 7d"
	if got != expected {
		t.Fatalf("期望全部为 0 时回退到 %q，实际 %q", expected, got)
	}
}

func TestBuildGFSKeepArgs_Mixed(t *testing.T) {
	p := model.Policy{
		RetentionMode: "gfs",
		KeepDaily:     0,
		KeepWeekly:    4,
		KeepMonthly:   0,
		KeepYearly:    1,
	}
	got := buildGFSKeepArgs(p)
	expected := "--keep-weekly 4 --keep-yearly 1"
	if got != expected {
		t.Fatalf("期望 %q，实际 %q", expected, got)
	}
}

func TestBuildGFSKeepArgs_SimpleModeStillWorks(t *testing.T) {
	// simple 模式不应使用 GFS args — 此函数仅在模式为 gfs 时调用，
	// 但确保输入为 simple 模式时仍返回合理回退。
	p := model.Policy{
		RetentionMode: "simple",
		RetentionDays: 30,
	}
	got := buildGFSKeepArgs(p)
	// simple 模式下 GFS 字段为 0 → 回退
	expected := "--keep-within 7d"
	if got != expected {
		t.Fatalf("期望 %q，实际 %q", expected, got)
	}
}
