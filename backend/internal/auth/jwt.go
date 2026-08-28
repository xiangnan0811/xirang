package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	Purpose2FAPending = "2fa_pending"
	PurposeStepUp     = "step_up"
	StepUpProofTTL    = 5 * time.Minute
)

type Claims struct {
	UserID       uint         `json:"uid"`
	Username     string       `json:"username"`
	Role         string       `json:"role"`
	Purpose      string       `json:"purpose,omitempty"`
	StepUpAction StepUpAction `json:"step_up_action,omitempty"`
	SessionID    string       `json:"sid,omitempty"`
	TokenVersion uint         `json:"ver"`
	jwt.RegisteredClaims
}

type JWTManager struct {
	secret      []byte
	ttl         time.Duration
	mu          sync.Mutex
	revoked     map[string]time.Time
	lastPruneAt time.Time
	db          *gorm.DB
}

func NewJWTManager(secret string, ttl time.Duration) *JWTManager {
	return &JWTManager{
		secret:  []byte(secret),
		ttl:     ttl,
		revoked: make(map[string]time.Time),
	}
}

// SetDB 设置数据库连接，启用撤销持久化
func (m *JWTManager) SetDB(db *gorm.DB) {
	m.db = db
	m.loadRevokedFromDB()
}

// loadRevokedFromDB 从数据库加载未过期的撤销记录到内存
func (m *JWTManager) loadRevokedFromDB() {
	if m.db == nil {
		return
	}
	var revocations []model.TokenRevocation
	if err := m.db.Where("expires_at > ?", time.Now().UTC()).Find(&revocations).Error; err != nil {
		logger.Module("auth").Warn().Err(err).Msg("加载 JWT 撤销记录失败")
		return
	}
	m.mu.Lock()
	for _, r := range revocations {
		m.revoked[r.TokenHash] = r.ExpiresAt
	}
	m.mu.Unlock()
}

// Generate2FAPendingToken 生成用于 2FA 验证步骤的短期令牌（5 分钟有效）。
func (m *JWTManager) Generate2FAPendingToken(user model.User) (string, error) {
	now := time.Now()
	tokenID, err := generateTokenID()
	if err != nil {
		return "", err
	}
	claims := Claims{
		UserID:       user.ID,
		Username:     user.Username,
		Role:         user.Role,
		Purpose:      Purpose2FAPending,
		TokenVersion: user.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        tokenID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
			Subject:   fmt.Sprintf("%d", user.ID),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

func (m *JWTManager) GenerateStepUpToken(user model.User, action StepUpAction, sessionIDs ...string) (string, time.Time, error) {
	if !IsValidStepUpAction(action) {
		return "", time.Time{}, fmt.Errorf("step-up action 无效")
	}
	if len(sessionIDs) > 1 {
		return "", time.Time{}, fmt.Errorf("step-up session binding 无效")
	}
	sessionID := ""
	if len(sessionIDs) == 1 {
		sessionID = sessionIDs[0]
	}
	if action == StepUpActionAssetSecretReveal && !lowerHexID(sessionID) || sessionID != "" && !lowerHexID(sessionID) {
		return "", time.Time{}, fmt.Errorf("step-up session binding 无效")
	}
	// NumericDate is serialized at whole-second precision. Return the same exact
	// expiry carried by the signed claim so API/storage facts cannot outlive it.
	now := time.Now().UTC().Truncate(time.Second)
	expiresAt := now.Add(StepUpProofTTLForAction(action))
	tokenID, err := generateTokenID()
	if err != nil {
		return "", time.Time{}, err
	}
	claims := Claims{
		UserID:       user.ID,
		Username:     user.Username,
		Role:         user.Role,
		Purpose:      PurposeStepUp,
		StepUpAction: action,
		SessionID:    sessionID,
		TokenVersion: user.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        tokenID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			Subject:   fmt.Sprintf("%d", user.ID),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expiresAt, nil
}

func (m *JWTManager) GenerateToken(user model.User) (string, error) {
	now := time.Now()
	tokenID, err := generateTokenID()
	if err != nil {
		return "", err
	}
	claims := Claims{
		UserID:       user.ID,
		Username:     user.Username,
		Role:         user.Role,
		TokenVersion: user.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        tokenID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
			Subject:   fmt.Sprintf("%d", user.ID),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

func (m *JWTManager) ParseToken(tokenString string) (*Claims, error) {
	return m.parseToken(tokenString, true)
}

func (m *JWTManager) RevokeToken(tokenString string) error {
	claims, err := m.parseToken(tokenString, false)
	if err != nil {
		return err
	}

	key := revocationKey(claims, tokenString)
	expireAt := time.Now().Add(m.ttl)
	if claims.ExpiresAt != nil {
		expireAt = claims.ExpiresAt.Time
	}
	if claims.ID != "" {
		return m.RevokeSession(claims.ID, claims.UserID, expireAt)
	}
	return m.revokeKey(key, claims.UserID, expireAt)
}

// RevokeSession revokes a login session using its non-bearer JTI. Callers do
// not need to retain or replay the raw JWT after AuthMiddleware has validated
// it. The in-memory revocation is applied before persistence so a storage
// failure cannot make the session usable again in this process.
func (m *JWTManager) RevokeSession(jti string, userID uint, expiresAt time.Time) error {
	if !lowerHexID(jti) || expiresAt.IsZero() || !expiresAt.UTC().After(time.Now().UTC()) {
		return fmt.Errorf("invalid session revocation")
	}
	return m.revokeKey("jti:"+jti, userID, expiresAt.UTC())
}

func (m *JWTManager) revokeKey(key string, userID uint, expireAt time.Time) error {
	now := time.Now().UTC()
	expireAt = expireAt.UTC()

	m.mu.Lock()
	m.revoked[key] = expireAt
	m.pruneRevokedLocked(now)
	m.mu.Unlock()

	// 持久化到数据库
	if m.db != nil {
		if err := m.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.TokenRevocation{
			TokenHash: key,
			UserID:    userID,
			ExpiresAt: expireAt,
		}).Error; err != nil {
			return fmt.Errorf("持久化 token 撤销记录失败: %w", err)
		}
		if err := m.db.Where("expires_at <= ?", now).Delete(&model.TokenRevocation{}).Error; err != nil {
			logger.Module("auth").Warn().Err(err).Msg("清理过期 JWT 撤销记录失败")
		}
	}
	return nil
}

// IsSessionRevoked checks a non-bearer login-session JTI against both the
// process cache and durable revocation rows. Invalid identifiers fail closed.
func (m *JWTManager) IsSessionRevoked(jti string) (bool, error) {
	if !lowerHexID(jti) {
		return true, fmt.Errorf("invalid session jti")
	}
	now := time.Now().UTC()
	key := "jti:" + jti
	m.mu.Lock()
	if now.Sub(m.lastPruneAt) > 30*time.Second {
		m.pruneRevokedLocked(now)
		m.lastPruneAt = now
	}
	expiresAt, revoked := m.revoked[key]
	m.mu.Unlock()
	if revoked && expiresAt.After(now) {
		return true, nil
	}
	if m.db == nil {
		return false, nil
	}
	var row model.TokenRevocation
	result := m.db.Select("token_hash", "expires_at").Where("token_hash = ? AND expires_at > ?", key, now).Limit(1).Find(&row)
	if result.Error != nil {
		return true, fmt.Errorf("query session revocation: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	m.mu.Lock()
	m.revoked[key] = row.ExpiresAt
	m.mu.Unlock()
	return true, nil
}

func (m *JWTManager) parseToken(tokenString string, checkRevoked bool) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("不支持的签名算法")
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("token 无效")
	}
	if checkRevoked {
		key := revocationKey(claims, tokenString)
		now := time.Now().UTC()
		m.mu.Lock()
		if now.Sub(m.lastPruneAt) > 30*time.Second {
			m.pruneRevokedLocked(now)
			m.lastPruneAt = now
		}
		_, revoked := m.revoked[key]
		m.mu.Unlock()
		if revoked {
			return nil, fmt.Errorf("token 已注销")
		}
	}
	return claims, nil
}

func (m *JWTManager) pruneRevokedLocked(now time.Time) {
	for key, expiresAt := range m.revoked {
		if !expiresAt.After(now) {
			delete(m.revoked, key)
		}
	}
}

func generateTokenID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成 token id 失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func lowerHexID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func revocationKey(claims *Claims, tokenString string) string {
	if claims != nil && claims.ID != "" {
		return "jti:" + claims.ID
	}
	sum := sha256.Sum256([]byte(tokenString))
	return "tok:" + hex.EncodeToString(sum[:16])
}
