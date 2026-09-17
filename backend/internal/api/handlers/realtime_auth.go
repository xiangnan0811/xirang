package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type realtimeAuthRequirements struct {
	Permission string
	Role       string
}

const realtimeSessionRevocationValidationTimeout = 250 * time.Millisecond

func authorizeRealtimeToken(ctx context.Context, token string, jwtManager *auth.JWTManager, db *gorm.DB, requirements realtimeAuthRequirements) (*auth.Claims, error) {
	if jwtManager == nil {
		return nil, fmt.Errorf("认证服务不可用")
	}

	claims, err := jwtManager.ParseToken(strings.TrimSpace(token))
	if err != nil {
		return nil, fmt.Errorf("token 无效或过期")
	}
	if claims.ID == "" || claims.ExpiresAt == nil || claims.UserID == 0 {
		return nil, fmt.Errorf("token 会话绑定无效")
	}
	if strings.TrimSpace(claims.Purpose) != "" {
		return nil, fmt.Errorf("认证令牌用途不匹配")
	}
	// Keep realtime authentication aligned with HTTP authentication: a valid
	// signature is not enough when another Core has durably revoked the JTI.
	// The request-derived timeout prevents a stalled authority store from
	// leaving a websocket handshake waiting indefinitely.
	if ctx == nil {
		ctx = context.Background()
	}
	checkCtx, cancel := context.WithTimeout(ctx, realtimeSessionRevocationValidationTimeout)
	revoked, revokeErr := jwtManager.IsSessionRevokedContext(checkCtx, claims.ID)
	cancel()
	if revokeErr != nil {
		return nil, fmt.Errorf("认证服务不可用")
	}
	if revoked {
		return nil, fmt.Errorf("token 已注销")
	}

	if db != nil {
		var user model.User
		if err := db.Select("token_version", "role").First(&user, claims.UserID).Error; err != nil {
			return nil, fmt.Errorf("用户不存在或已删除")
		}
		if user.TokenVersion != claims.TokenVersion {
			return nil, fmt.Errorf("token 已失效，请重新登录")
		}
		if user.Role != claims.Role {
			return nil, fmt.Errorf("用户角色已变更，请重新登录")
		}
	}

	if requirements.Role != "" && claims.Role != requirements.Role {
		return nil, fmt.Errorf("权限不足")
	}
	if requirements.Permission != "" && !middleware.HasPermission(claims.Role, requirements.Permission) {
		return nil, fmt.Errorf("权限不足")
	}

	return claims, nil
}
