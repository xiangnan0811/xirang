package auth

import (
	"context"
	"strings"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type LoginFailureLocker struct {
	db           *gorm.DB
	threshold    int
	lockDuration time.Duration
}

func NewLoginFailureLocker(db *gorm.DB, threshold int, lockDuration time.Duration) *LoginFailureLocker {
	if threshold <= 0 {
		threshold = 5
	}
	if lockDuration <= 0 {
		lockDuration = 15 * time.Minute
	}
	return &LoginFailureLocker{
		db:           db,
		threshold:    threshold,
		lockDuration: lockDuration,
	}
}

func (l *LoginFailureLocker) IsLocked(username, ip string, now time.Time) (time.Time, bool) {
	u, i := normalize(username, ip)
	var rec model.LoginFailure
	err := l.db.Where("username = ? AND client_ip = ?", u, i).First(&rec).Error
	if err != nil {
		return time.Time{}, false
	}
	if rec.LockedUntil != nil && rec.LockedUntil.After(now) {
		return *rec.LockedUntil, true
	}
	// 锁定已过期——清除状态
	if rec.LockedUntil != nil {
		l.db.Delete(&rec)
	}
	return time.Time{}, false
}

func (l *LoginFailureLocker) RegisterFailure(username, ip string, now time.Time) {
	u, i := normalize(username, ip)

	var rec model.LoginFailure
	err := l.db.Where("username = ? AND client_ip = ?", u, i).First(&rec).Error
	if err != nil {
		// 首次失败，创建记录
		rec = model.LoginFailure{
			Username:  u,
			ClientIP:  i,
			FailCount: 1,
			UpdatedAt: now,
		}
		if rec.FailCount >= l.threshold {
			locked := now.Add(l.lockDuration)
			rec.LockedUntil = &locked
			rec.FailCount = 0
		}
		l.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&rec)
		return
	}

	// 已锁定且未过期——忽略
	if rec.LockedUntil != nil && rec.LockedUntil.After(now) {
		return
	}
	// 锁定已过期——重置
	if rec.LockedUntil != nil {
		rec.FailCount = 0
		rec.LockedUntil = nil
	}

	rec.FailCount++
	rec.UpdatedAt = now
	if rec.FailCount >= l.threshold {
		locked := now.Add(l.lockDuration)
		rec.LockedUntil = &locked
		rec.FailCount = 0
	}
	l.db.Save(&rec)
}

func (l *LoginFailureLocker) RegisterSuccess(username, ip string) {
	u, i := normalize(username, ip)
	l.db.Where("username = ? AND client_ip = ?", u, i).Delete(&model.LoginFailure{})
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
