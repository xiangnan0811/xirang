package handlers

import (
	"fmt"
	"strings"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type realtimeAuthRequirements struct {
	Permission string
	Role       string
}

func authorizeRealtimeToken(token string, jwtManager *auth.JWTManager, db *gorm.DB, requirements realtimeAuthRequirements) (*auth.Claims, error) {
	if jwtManager == nil {
		return nil, fmt.Errorf("认证服务不可用")
	}

	claims, err := jwtManager.ParseToken(strings.TrimSpace(token))
	if err != nil {
		return nil, fmt.Errorf("token 无效或过期")
	}
	if claims.Purpose == "2fa_pending" {
		return nil, fmt.Errorf("需要完成两步验证")
	}

	if db != nil {
		var user model.User
		if err := db.Select("token_version").First(&user, claims.UserID).Error; err != nil {
			return nil, fmt.Errorf("用户不存在或已删除")
		}
		if user.TokenVersion != claims.TokenVersion {
			return nil, fmt.Errorf("token 已失效，请重新登录")
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
