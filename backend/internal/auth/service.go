package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

var errInvalidCredentials = fmt.Errorf("用户名或密码错误")

type LoginLockedError struct {
	Until time.Time
}

func (e *LoginLockedError) Error() string {
	return "登录失败次数过多，请稍后再试"
}

func (e *LoginLockedError) RetryAfterSeconds(now time.Time) int {
	if !e.Until.After(now) {
		return 0
	}
	seconds := int(time.Until(e.Until).Seconds())
	if seconds < 1 {
		return 1
	}
	return seconds
}

func IsLoginLocked(err error) (*LoginLockedError, bool) {
	var lockedErr *LoginLockedError
	if !errors.As(err, &lockedErr) {
		return nil, false
	}
	return lockedErr, true
}

type LoginSecurityConfig struct {
	FailLockThreshold int
	FailLockDuration  time.Duration
}

type Service struct {
	db            *gorm.DB
	jwt           *JWTManager
	failureLocker *LoginFailureLocker
}

func NewService(db *gorm.DB, jwt *JWTManager, cfg LoginSecurityConfig) *Service {
	locker := NewLoginFailureLocker(cfg.FailLockThreshold, cfg.FailLockDuration)
	locker.StartCleanup(context.Background(), 5*time.Minute)
	return &Service{
		db:            db,
		jwt:           jwt,
		failureLocker: locker,
	}
}

func (s *Service) Login(username, password, clientIP string) (string, *model.User, error) {
	now := time.Now()
	if lockedUntil, locked := s.failureLocker.IsLocked(username, clientIP, now); locked {
		return "", nil, &LoginLockedError{Until: lockedUntil}
	}

	var user model.User
	if err := s.db.Where("username = ?", username).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			s.failureLocker.RegisterFailure(username, clientIP, now)
			return "", nil, errInvalidCredentials
		}
		return "", nil, err
	}
	if err := CheckPassword(user.PasswordHash, password); err != nil {
		s.failureLocker.RegisterFailure(username, clientIP, now)
		return "", nil, errInvalidCredentials
	}
	s.failureLocker.RegisterSuccess(username, clientIP)

	token, err := s.jwt.GenerateToken(user)
	if err != nil {
		return "", nil, err
	}
	return token, &user, nil
}
