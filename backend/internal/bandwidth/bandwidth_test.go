package bandwidth

import (
	"testing"
	"time"
)

func makeTime(hour, minute int) time.Time {
	return time.Date(2025, 1, 15, hour, minute, 0, 0, time.UTC)
}

func TestResolveLimitEmpty(t *testing.T) {
	if got := ResolveLimit("", makeTime(12, 0)); got != 0 {
		t.Fatalf("空调度应返回 0，实际: %d", got)
	}
}

func TestResolveLimitInvalidJSON(t *testing.T) {
	if got := ResolveLimit("not json", makeTime(12, 0)); got != 0 {
		t.Fatalf("无效 JSON 应返回 0，实际: %d", got)
	}
}

func TestResolveLimitMatchesRule(t *testing.T) {
	schedule := `[
		{"start":"00:00","end":"08:00","limit_mbps":100},
		{"start":"08:00","end":"18:00","limit_mbps":20},
		{"start":"18:00","end":"00:00","limit_mbps":50}
	]`

	tests := []struct {
		name     string
		hour     int
		minute   int
		expected int
	}{
		{"凌晨 2 点匹配夜间规则", 2, 0, 100},
		{"上午 10 点匹配白天规则", 10, 0, 20},
		{"晚上 20 点匹配晚间规则", 20, 0, 50},
		{"08:00 边界匹配白天规则", 8, 0, 20},
		{"17:59 仍匹配白天规则", 17, 59, 20},
		{"18:00 边界匹配晚间规则", 18, 0, 50},
		{"00:00 边界匹配夜间规则", 0, 0, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveLimit(schedule, makeTime(tt.hour, tt.minute))
			if got != tt.expected {
				t.Fatalf("期望 %d Mbps，实际: %d", tt.expected, got)
			}
		})
	}
}

func TestResolveLimitOvernightRange(t *testing.T) {
	schedule := `[{"start":"22:00","end":"06:00","limit_mbps":100}]`

	tests := []struct {
		name     string
		hour     int
		minute   int
		expected int
	}{
		{"23:00 匹配跨午夜规则", 23, 0, 100},
		{"03:00 匹配跨午夜规则", 3, 0, 100},
		{"22:00 边界匹配", 22, 0, 100},
		{"05:59 仍匹配", 5, 59, 100},
		{"06:00 不匹配", 6, 0, 0},
		{"12:00 不匹配", 12, 0, 0},
		{"21:59 不匹配", 21, 59, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveLimit(schedule, makeTime(tt.hour, tt.minute))
			if got != tt.expected {
				t.Fatalf("期望 %d Mbps，实际: %d", tt.expected, got)
			}
		})
	}
}

func TestResolveLimitNoMatch(t *testing.T) {
	schedule := `[{"start":"08:00","end":"18:00","limit_mbps":20}]`
	if got := ResolveLimit(schedule, makeTime(20, 0)); got != 0 {
		t.Fatalf("无匹配规则应返回 0，实际: %d", got)
	}
}

func TestResolveLimitInvalidTimeFormat(t *testing.T) {
	schedule := `[{"start":"abc","end":"18:00","limit_mbps":20}]`
	if got := ResolveLimit(schedule, makeTime(10, 0)); got != 0 {
		t.Fatalf("无效时间格式应跳过规则返回 0，实际: %d", got)
	}
}

func TestParseHHMM(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"00:00", 0},
		{"08:30", 510},
		{"23:59", 1439},
		{"12:00", 720},
		{"", -1},
		{"abc", -1},
		{"25:00", -1},
		{"12:60", -1},
		{"-1:00", -1},
		{"12:-1", -1},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseHHMM(tt.input)
			if got != tt.expected {
				t.Fatalf("ParseHHMM(%q) = %d，期望 %d", tt.input, got, tt.expected)
			}
		})
	}
}
