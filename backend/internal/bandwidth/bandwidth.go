package bandwidth

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// Rule 定义一个时间段内的带宽限制规则。
type Rule struct {
	Start     string `json:"start"`     // "HH:MM"
	End       string `json:"end"`       // "HH:MM"
	LimitMbps int    `json:"limit_mbps"`
}

// ResolveLimit 根据带宽调度规则和当前时间返回当前应使用的带宽限制（Mbps）。
// 返回 0 表示无匹配规则（即不限速）。
func ResolveLimit(scheduleJSON string, now time.Time) int {
	if scheduleJSON == "" {
		return 0
	}
	var rules []Rule
	if err := json.Unmarshal([]byte(scheduleJSON), &rules); err != nil {
		return 0
	}
	currentMinutes := now.Hour()*60 + now.Minute()
	for _, r := range rules {
		startMin := ParseHHMM(r.Start)
		endMin := ParseHHMM(r.End)
		if startMin < 0 || endMin < 0 {
			continue
		}
		if r.LimitMbps < 0 {
			continue
		}
		// 处理跨午夜的时间段（如 22:00-06:00）
		if startMin <= endMin {
			if currentMinutes >= startMin && currentMinutes < endMin {
				return r.LimitMbps
			}
		} else {
			// 跨午夜：当前时间 >= start 或 < end 均匹配
			if currentMinutes >= startMin || currentMinutes < endMin {
				return r.LimitMbps
			}
		}
	}
	return 0
}

// ParseHHMM 将 "HH:MM" 格式的时间字符串解析为从 00:00 起的分钟数。
// 返回 -1 表示解析失败。
func ParseHHMM(s string) int {
	s = strings.TrimSpace(s)
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return -1
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || h < 0 || h > 23 {
		return -1
	}
	m, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || m < 0 || m > 59 {
		return -1
	}
	return h*60 + m
}
