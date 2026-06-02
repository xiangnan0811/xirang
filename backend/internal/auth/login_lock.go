package auth

import (
	"context"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// globalIPMarker 是全局计数器的 IP 标记，不与任何真实 IP 冲突。
const globalIPMarker = "__global__"

type LoginFailureLocker struct {
	db                *gorm.DB
	threshold         int
	lockDuration      time.Duration
	globalThreshold   int
	globalLockDuration time.Duration
	settingsSvc       *settings.Service
}

func NewLoginFailureLocker(db *gorm.DB, settingsSvc *settings.Service, threshold int, lockDuration time.Duration, globalThreshold int, globalLockDuration time.Duration) *LoginFailureLocker {
	if threshold <= 0 {
		threshold = 5
	}
	if lockDuration <= 0 {
		lockDuration = 15 * time.Minute
	}
	if globalThreshold <= 0 {
		globalThreshold = 50
	}
	if globalLockDuration <= 0 {
		globalLockDuration = 15 * time.Minute
	}
	return &LoginFailureLocker{
		db:                 db,
		threshold:          threshold,
		lockDuration:       lockDuration,
		globalThreshold:    globalThreshold,
		globalLockDuration: globalLockDuration,
		settingsSvc:        settingsSvc,
	}
}

// getThreshold 动态读取登录失败锁定阈值（per-IP）
func (l *LoginFailureLocker) getThreshold() int {
	if l.settingsSvc != nil {
		if v, err := strconv.Atoi(l.settingsSvc.GetEffective("login.fail_lock_threshold")); err == nil && v > 0 {
			return v
		}
	}
	return l.threshold
}

// getLockDuration 动态读取登录锁定持续时间（per-IP）
func (l *LoginFailureLocker) getLockDuration() time.Duration {
	if l.settingsSvc != nil {
		if d, err := time.ParseDuration(l.settingsSvc.GetEffective("login.fail_lock_duration")); err == nil && d > 0 {
			return d
		}
	}
	return l.lockDuration
}

// getGlobalThreshold 动态读取全局登录失败锁定阈值（per-username，跨 IP）
func (l *LoginFailureLocker) getGlobalThreshold() int {
	if l.settingsSvc != nil {
		if v, err := strconv.Atoi(l.settingsSvc.GetEffective("login.global_fail_lock_threshold")); err == nil && v > 0 {
			return v
		}
	}
	return l.globalThreshold
}

// getGlobalLockDuration 动态读取全局锁定持续时间
func (l *LoginFailureLocker) getGlobalLockDuration() time.Duration {
	if l.settingsSvc != nil {
		if d, err := time.ParseDuration(l.settingsSvc.GetEffective("login.global_fail_lock_duration")); err == nil && d > 0 {
			return d
		}
	}
	return l.globalLockDuration
}

// IsLocked 检查用户名是否存在锁定（per-IP 或全局）。
// 优先返回 per-IP 的锁定期限；若无则检查全局锁。
func (l *LoginFailureLocker) IsLocked(username, ip string, now time.Time) (time.Time, bool) {
	u, i := normalize(username, ip)

	// 1. 检查 per-IP 锁定
	var rec model.LoginFailure
	if l.db.Where("username = ? AND client_ip = ?", u, i).Limit(1).Find(&rec).RowsAffected > 0 {
		if rec.LockedUntil != nil && rec.LockedUntil.After(now) {
			return *rec.LockedUntil, true
		}
		// 已过期——清除
		if rec.LockedUntil != nil {
			l.db.Delete(&rec)
		}
	}

	// 2. 检查全局 per-username 锁定（client_ip = "__global__"）
	var globalRec model.LoginFailure
	if l.db.Where("username = ? AND client_ip = ?", u, globalIPMarker).Limit(1).Find(&globalRec).RowsAffected > 0 {
		if globalRec.LockedUntil != nil && globalRec.LockedUntil.After(now) {
			return *globalRec.LockedUntil, true
		}
		// 已过期——清除
		if globalRec.LockedUntil != nil {
			l.db.Delete(&globalRec)
		}
	}

	return time.Time{}, false
}

// RegisterFailure 记录一次登录失败，同时维护 per-IP 和全局计数器。
func (l *LoginFailureLocker) RegisterFailure(username, ip string, now time.Time) {
	u, i := normalize(username, ip)
	threshold := l.getThreshold()
	lockDuration := l.getLockDuration()

	// --- per-IP 计数器 ---
	var rec model.LoginFailure
	if l.db.Where("username = ? AND client_ip = ?", u, i).Limit(1).Find(&rec).RowsAffected == 0 {
		rec = model.LoginFailure{
			Username:  u,
			ClientIP:  i,
			FailCount: 1,
			UpdatedAt: now,
		}
		if rec.FailCount >= threshold {
			locked := now.Add(lockDuration)
			rec.LockedUntil = &locked
			rec.FailCount = 0
		}
		l.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&rec)
	} else {
		if rec.LockedUntil != nil && rec.LockedUntil.After(now) {
			// 已锁定——不重复计数
		} else {
			if rec.LockedUntil != nil {
				rec.FailCount = 0
				rec.LockedUntil = nil
			}
			rec.FailCount++
			rec.UpdatedAt = now
			if rec.FailCount >= threshold {
				locked := now.Add(lockDuration)
				rec.LockedUntil = &locked
				rec.FailCount = 0
			}
			l.db.Save(&rec)
		}
	}

	// --- 全局 per-username 计数器（client_ip = "__global__"） ---
	globalThreshold := l.getGlobalThreshold()
	globalLockDuration := l.getGlobalLockDuration()

	var grec model.LoginFailure
	if l.db.Where("username = ? AND client_ip = ?", u, globalIPMarker).Limit(1).Find(&grec).RowsAffected == 0 {
		grec = model.LoginFailure{
			Username:  u,
			ClientIP:  globalIPMarker,
			FailCount: 1,
			UpdatedAt: now,
		}
		if grec.FailCount >= globalThreshold {
			locked := now.Add(globalLockDuration)
			grec.LockedUntil = &locked
			grec.FailCount = 0
			log.Warn().
				Str("username", u).
				Int("global_threshold", globalThreshold).
				Dur("lock_duration", globalLockDuration).
				Msg("全局登录锁定触发——疑似分布式暴力破解攻击")
		}
		l.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&grec)
		return
	}

	// 已全局锁定——不重复计数
	if grec.LockedUntil != nil && grec.LockedUntil.After(now) {
		return
	}

	// 锁定已过期——重置
	if grec.LockedUntil != nil {
		grec.FailCount = 0
		grec.LockedUntil = nil
	}

	grec.FailCount++
	grec.UpdatedAt = now
	if grec.FailCount >= globalThreshold {
		locked := now.Add(globalLockDuration)
		grec.LockedUntil = &locked
		grec.FailCount = 0
		log.Warn().
			Str("username", u).
			Int("global_threshold", globalThreshold).
			Dur("lock_duration", globalLockDuration).
			Msg("全局登录锁定触发——疑似分布式暴力破解攻击")
	}
	l.db.Save(&grec)
}

// RegisterSuccess 登录成功后清除 per-IP 和全局失败计数。
func (l *LoginFailureLocker) RegisterSuccess(username, ip string) {
	u, i := normalize(username, ip)
	l.db.Where("username = ? AND client_ip = ?", u, i).Delete(&model.LoginFailure{})
	l.db.Where("username = ? AND client_ip = ?", u, globalIPMarker).Delete(&model.LoginFailure{})
}

// StartCleanup 定期清理过期的锁定条目，防止表膨胀。
func (l *LoginFailureLocker) StartCleanup(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now()
				// 清理已过期的锁定记录
				l.db.Where("locked_until IS NOT NULL AND locked_until < ?", now).Delete(&model.LoginFailure{})
				// 清理长期无活动的失败记录（未锁定但超过 24 小时未更新）
				stale := now.Add(-24 * time.Hour)
				l.db.Where("locked_until IS NULL AND updated_at < ?", stale).Delete(&model.LoginFailure{})
			}
		}
	}()
}

func normalize(username, ip string) (string, string) {
	u := strings.ToLower(strings.TrimSpace(username))
	i := strings.TrimSpace(ip)
	if i == "" {
		i = "unknown"
	}
	return u, i
}