package task

import (
	"strings"
	"syscall"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/util"

	"strconv"
)

func (m *Manager) checkLocalStorageSpace() {
	log := logger.Module("task")

	minFreeRaw := util.GetEnvOrDefault("BACKUP_STORAGE_MIN_FREE_GB", "10")
	minFreeGB, err := strconv.Atoi(minFreeRaw)
	if err != nil || minFreeGB < 0 {
		minFreeGB = 10
	}

	maxUsageRaw := util.GetEnvOrDefault("BACKUP_STORAGE_MAX_USAGE_PCT", "90")
	maxUsagePct, err := strconv.Atoi(maxUsageRaw)
	if err != nil || maxUsagePct < 0 || maxUsagePct > 100 {
		maxUsagePct = 90
	}

	// 收集所有 rsync 类型策略的本地目标路径
	var policies []model.Policy
	if err := m.db.Where("enabled = ?", true).Find(&policies).Error; err != nil {
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

	for _, path := range localPaths {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(path, &stat); err != nil {
			log.Warn().Str("path", path).Err(err).Msg("获取磁盘空间信息失败")
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

		if overThreshold {
			log.Warn().Str("path", path).Float64("free_gb", freeGB).Float64("usage_pct", usagePct).Msg("备份存储空间不足")
			_ = alerting.RaiseStorageSpaceAlert(m.db, path, freeGB, totalGB, usagePct)
		} else {
			// 如果之前有告警，现在恢复了，按路径解除告警
			_ = alerting.ResolveAlertsByErrorCode(m.db, "XR-STORAGE-LOW:"+path, "存储空间恢复正常")
		}
	}
}
