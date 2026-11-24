package task

import (
	"strconv"
	"strings"
	"syscall"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/util"
)

func (m *Manager) checkLocalStorageSpace() {
	log := logger.Module("task")

	minFreeGB := 10
	if m.settingsSvc != nil {
		if v, err := strconv.Atoi(m.settingsSvc.GetEffective("storage.min_free_gb")); err == nil && v >= 0 {
			minFreeGB = v
		}
	} else {
		if raw := util.GetEnvOrDefault("BACKUP_STORAGE_MIN_FREE_GB", "10"); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
				minFreeGB = v
			}
		}
	}

	maxUsagePct := 90
	if m.settingsSvc != nil {
		if v, err := strconv.Atoi(m.settingsSvc.GetEffective("storage.max_usage_pct")); err == nil && v >= 0 && v <= 100 {
			maxUsagePct = v
		}
	} else {
		if raw := util.GetEnvOrDefault("BACKUP_STORAGE_MAX_USAGE_PCT", "90"); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil && v >= 0 && v <= 100 {
				maxUsagePct = v
			}
		}
	}

	// 收集所有启用且非模板策略的本地目标路径
	var policies []model.Policy
	if err := m.db.Where("enabled = ? AND is_template = ?", true, false).Find(&policies).Error; err != nil {
		log.Error().Err(err).Msg("查询策略失败（存储空间检查）")
		return
	}

	seen := make(map[string]bool)
	var localPaths []string
	for _, p := range policies {
		path := strings.TrimSpace(p.TargetPath)
		if path == "" || seen[path] {
			continue
		}
		// 仅检查本地路径（不含 : 的路径视为本地）
		if strings.Contains(path, ":") {
			continue
		}
		seen[path] = true
		localPaths = append(localPaths, path)
	}

	overThresholdCodes := make(map[string]bool)
	healthyCodes := make(map[string]bool)
	for _, path := range localPaths {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(path, &stat); err != nil {
			log.Warn().Str("path", sanitizeTaskLogMessage(path)).Str("error", sanitizeTaskRuntimeError(err)).Msg("获取磁盘空间信息失败")
			continue
		}

		totalBytes := stat.Blocks * uint64(stat.Bsize)
		freeBytes := stat.Bavail * uint64(stat.Bsize)
		totalGB := float64(totalBytes) / (1024 * 1024 * 1024)
		freeGB := float64(freeBytes) / (1024 * 1024 * 1024)
		usagePct := 0.0
		if totalBytes > 0 {
			usagePct = float64(totalBytes-freeBytes) / float64(totalBytes) * 100
		}

		overThreshold := false
		if minFreeGB > 0 && freeGB < float64(minFreeGB) {
			overThreshold = true
		}
		if maxUsagePct > 0 && usagePct > float64(maxUsagePct) {
			overThreshold = true
		}

		sanitizedPath := sanitizeTaskLogMessage(path)
		errorCode := "XR-STORAGE-LOW:" + sanitizedPath
		if overThreshold {
			overThresholdCodes[errorCode] = true
			log.Warn().Str("path", sanitizedPath).Float64("free_gb", freeGB).Float64("usage_pct", usagePct).Msg("备份存储空间不足")
			_ = m.alertDispatcher.RaiseStorageSpaceAlert(sanitizedPath, freeGB, totalGB, usagePct)
		} else {
			healthyCodes[errorCode] = true
		}
	}
	for errorCode := range healthyCodes {
		if overThresholdCodes[errorCode] {
			continue
		}
		// 如果之前有告警，现在同标识路径均恢复正常，按脱敏后的路径标识解除告警
		_ = m.alertDispatcher.ResolveAlertsByErrorCode(errorCode, "存储空间恢复正常")
	}
}
