package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func (s *Service) ListUsers() ([]model.User, error) {
	var users []model.User
	if err := s.db.Select("id", "username", "role", "created_at", "updated_at").Order("id asc").Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

func (s *Service) CreateUser(username, password, role string) (*model.User, error) {
	normalizedUsername := strings.TrimSpace(username)
	if normalizedUsername == "" {
		return nil, fmt.Errorf("用户名不能为空")
	}
	normalizedRole, err := normalizeRole(role)
	if err != nil {
		return nil, err
	}
	if err := ValidatePasswordStrength(password); err != nil {
		return nil, err
	}

	var existing model.User
	if err := s.db.Where("username = ?", normalizedUsername).First(&existing).Error; err == nil {
		return nil, fmt.Errorf("用户名 %s 已存在", normalizedUsername)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("操作失败，请稍候重试")
	}

	hash, err := HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("操作失败，请稍候重试")
	}

	user := &model.User{
		Username:     normalizedUsername,
		PasswordHash: hash,
		Role:         normalizedRole,
	}
	if err := s.db.Create(user).Error; err != nil {
		return nil, err
	}
	return user, nil
}

func (s *Service) UpdateUser(userID uint, role *string, password *string) (*model.User, error) {
	var user model.User
	if err := s.db.First(&user, userID).Error; err != nil {
		return nil, err
	}

	updates := map[string]any{}
	if role != nil {
		normalizedRole, err := normalizeRole(*role)
		if err != nil {
			return nil, err
		}
		updates["role"] = normalizedRole
	}
	if password != nil && strings.TrimSpace(*password) != "" {
		if err := ValidatePasswordStrength(*password); err != nil {
			return nil, err
		}
		hash, err := HashPassword(*password)
		if err != nil {
			return nil, fmt.Errorf("操作失败，请稍候重试")
		}
		updates["password_hash"] = hash
	}
	if len(updates) == 0 {
		return &user, nil
	}

	if err := s.db.Model(&user).Updates(updates).Error; err != nil {
		return nil, err
	}
	if err := s.db.First(&user, userID).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (s *Service) DeleteUser(userID uint, actorID uint) error {
	if userID == actorID {
		return fmt.Errorf("不允许删除当前登录用户")
	}
	result := s.db.Delete(&model.User{}, userID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("用户不存在")
	}
	return nil
}

func (s *Service) ChangePassword(userID uint, currentPassword string, newPassword string) error {
	var user model.User
	if err := s.db.First(&user, userID).Error; err != nil {
		return err
	}

	if err := CheckPassword(user.PasswordHash, currentPassword); err != nil {
		return fmt.Errorf("当前密码错误")
	}
	if err := ValidatePasswordStrength(newPassword); err != nil {
		return err
	}

	hash, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("操作失败，请稍候重试")
	}
	return s.db.Model(&user).Update("password_hash", hash).Error
}

func normalizeRole(role string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(role))
	switch normalized {
	case "admin", "operator", "viewer":
		return normalized, nil
	default:
		return "", fmt.Errorf("不支持的角色类型")
	}
}
