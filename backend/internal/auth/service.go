package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

var errInvalidCredentials = fmt.Errorf("用户名或密码错误")

// dummyPasswordHash 用于用户不存在时执行等价 bcrypt 比对，消除时序差异。
var dummyPasswordHash = func() string {
	h, _ := HashPassword("xirang-dummy-timing-pad")
	return h
}()

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
	FailLockThreshold       int
	FailLockDuration        time.Duration
	GlobalFailLockThreshold int
	GlobalFailLockDuration  time.Duration
	// Now is the clock function used during Login(). If nil, time.Now is used.
	// Tests may inject a controlled clock to avoid time.Sleep.
	Now func() time.Time
}

type Service struct {
	db            *gorm.DB
	jwt           *JWTManager
	failureLocker *LoginFailureLocker
	nowFunc       func() time.Time
}

func NewService(db *gorm.DB, jwt *JWTManager, settingsSvc *settings.Service, cfg LoginSecurityConfig) *Service {
	locker := NewLoginFailureLocker(db, settingsSvc, cfg.FailLockThreshold, cfg.FailLockDuration, cfg.GlobalFailLockThreshold, cfg.GlobalFailLockDuration)
	locker.StartCleanup(context.Background(), 5*time.Minute)
	nowFunc := cfg.Now
	if nowFunc == nil {
		nowFunc = time.Now
	}
	if jwt != nil && db != nil {
		// Pending 2FA JTIs are durably registered by the shared manager. This
		// also keeps direct manager-generated challenges on the same path.
		jwt.SetDB(db)
	}
	return &Service{
		db:            db,
		jwt:           jwt,
		failureLocker: locker,
		nowFunc:       nowFunc,
	}
}

// LoginResult 封装登录结果，区分完整登录和需要 2FA 的中间状态。
type LoginResult struct {
	Token       string
	User        *model.User
	Requires2FA bool
	LoginToken  string // 仅在 Requires2FA=true 时有效
}

func (s *Service) Login(username, password, clientIP string) (*LoginResult, error) {
	now := s.nowFunc()
	if lockedUntil, locked := s.failureLocker.IsLocked(username, clientIP, now); locked {
		return nil, &LoginLockedError{Until: lockedUntil}
	}

	var user model.User
	result := s.db.Where("username = ?", username).Limit(1).Find(&user)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		// 执行一次等价的 bcrypt 比对以消除时序差异，防止通过响应时间枚举有效用户名
		_ = CheckPassword(dummyPasswordHash, password)
		s.failureLocker.RegisterFailure(username, clientIP, now)
		return nil, errInvalidCredentials
	}
	if err := CheckPassword(user.PasswordHash, password); err != nil {
		s.failureLocker.RegisterFailure(username, clientIP, now)
		return nil, errInvalidCredentials
	}
	s.failureLocker.RegisterSuccess(username, clientIP)

	if user.TOTPEnabled {
		loginToken, err := s.jwt.Generate2FAPendingToken(user)
		if err != nil {
			return nil, err
		}
		return &LoginResult{Requires2FA: true, LoginToken: loginToken, User: &user}, nil
	}

	token, err := s.jwt.GenerateToken(user)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Token: token, User: &user}, nil
}

func (s *Service) ListUsers() ([]model.User, error) {
	var users []model.User
	if err := s.db.Select("id", "username", "role", "totp_enabled", "created_at", "updated_at").Order("id asc").Find(&users).Error; err != nil {
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
	result := s.db.Where("username = ?", normalizedUsername).Limit(1).Find(&existing)
	if result.Error != nil {
		return nil, fmt.Errorf("操作失败，请稍候重试")
	}
	if result.RowsAffected > 0 {
		return nil, fmt.Errorf("用户名 %s 已存在", normalizedUsername)
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
	if s == nil || s.db == nil {
		return nil, errors.New("身份数据库未初始化")
	}

	var normalizedRole string
	if role != nil {
		var err error
		normalizedRole, err = normalizeRole(*role)
		if err != nil {
			return nil, err
		}
	}
	passwordHash := ""
	if password != nil && strings.TrimSpace(*password) != "" {
		if err := ValidatePasswordStrength(*password); err != nil {
			return nil, err
		}
		var err error
		passwordHash, err = HashPassword(*password)
		if err != nil {
			return nil, fmt.Errorf("操作失败，请稍候重试")
		}
	}
	if role == nil && passwordHash == "" {
		var user model.User
		if err := s.db.First(&user, userID).Error; err != nil {
			return nil, err
		}
		return &user, nil
	}

	var updated model.User
	err := withIdentityTransaction(context.Background(), s.db, func(tx *gorm.DB) error {
		if err := SerializeIdentityMutationLock(tx); err != nil {
			return err
		}
		row, err := loadStoredIdentityUser(tx, userID, true)
		if err != nil {
			return err
		}
		if role != nil && row.Role == "admin" && normalizedRole != "admin" {
			count, err := countAdmins(tx)
			if err != nil {
				return err
			}
			if count <= 1 {
				return ErrLastAdmin
			}
		}
		updates := map[string]any{}
		securityChanged := false
		if role != nil && normalizedRole != row.Role {
			updates["role"] = normalizedRole
			securityChanged = true
		}
		if passwordHash != "" {
			updates["password_hash"] = passwordHash
			securityChanged = true
		}
		if !securityChanged {
			return tx.First(&updated, userID).Error
		}
		updates["token_version"] = gorm.Expr("token_version + 1")
		result := tx.Table("users").Where(
			"id = ? AND token_version = ? AND role = ?",
			userID, row.TokenVersion, row.Role,
		).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrSecurityConflict
		}
		if err := deletePendingTokens(tx, userID); err != nil {
			return err
		}
		return tx.First(&updated, userID).Error
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func (s *Service) DeleteUser(userID uint, actorID uint) error {
	if userID == actorID {
		return fmt.Errorf("不允许删除当前登录用户")
	}
	if s == nil || s.db == nil {
		return errors.New("身份数据库未初始化")
	}
	return withIdentityTransaction(context.Background(), s.db, func(tx *gorm.DB) error {
		if err := SerializeIdentityMutationLock(tx); err != nil {
			return err
		}
		row, err := loadStoredIdentityUser(tx, userID, true)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("用户不存在")
			}
			return err
		}
		if row.Role == "admin" {
			count, err := countAdmins(tx)
			if err != nil {
				return err
			}
			if count <= 1 {
				return ErrLastAdmin
			}
		}
		result := tx.Where("id = ?", userID).Delete(&model.User{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("用户不存在")
		}
		return nil
	})
}

func (s *Service) ChangePassword(userID uint, currentPassword string, newPassword string) error {
	if s == nil || s.db == nil {
		return errors.New("身份数据库未初始化")
	}
	if err := ValidatePasswordStrength(newPassword); err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("操作失败，请稍候重试")
	}
	return withIdentityTransaction(context.Background(), s.db, func(tx *gorm.DB) error {
		row, err := loadStoredIdentityUser(tx, userID, true)
		if err != nil {
			return err
		}
		if err := CheckPassword(row.PasswordHash, currentPassword); err != nil {
			return fmt.Errorf("当前密码错误")
		}
		result := tx.Table("users").Where(
			"id = ? AND token_version = ? AND password_hash = ?",
			userID, row.TokenVersion, row.PasswordHash,
		).Updates(map[string]any{
			"password_hash": hash,
			"token_version": gorm.Expr("token_version + 1"),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrSecurityConflict
		}
		return deletePendingTokens(tx, userID)
	})
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
