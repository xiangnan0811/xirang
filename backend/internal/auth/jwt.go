package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"xirang/backend/internal/model"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	UserID   uint   `json:"uid"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

type JWTManager struct {
	secret      []byte
	ttl         time.Duration
	mu          sync.Mutex
	revoked     map[string]time.Time
	lastPruneAt time.Time
}

func NewJWTManager(secret string, ttl time.Duration) *JWTManager {
	return &JWTManager{
		secret:  []byte(secret),
		ttl:     ttl,
		revoked: make(map[string]time.Time),
	}
}

func (m *JWTManager) GenerateToken(user model.User) (string, error) {
	now := time.Now()
	tokenID, err := generateTokenID()
	if err != nil {
		return "", err
	}
	claims := Claims{
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
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

	m.mu.Lock()
	m.revoked[key] = expireAt
	m.pruneRevokedLocked(time.Now())
	m.mu.Unlock()
	return nil
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
		now := time.Now()
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

func revocationKey(claims *Claims, tokenString string) string {
	if claims != nil && claims.ID != "" {
		return "jti:" + claims.ID
	}
	sum := sha256.Sum256([]byte(tokenString))
	return "tok:" + hex.EncodeToString(sum[:16])
}
