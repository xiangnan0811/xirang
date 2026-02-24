package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

type AuthHandler struct {
	authService          *auth.Service
	jwtManager           *auth.JWTManager
	captchaEnabled       bool
	secondCaptchaEnabled bool
}

func NewAuthHandler(authService *auth.Service, jwtManager *auth.JWTManager, captchaEnabled bool, secondCaptchaEnabled bool) *AuthHandler {
	return &AuthHandler{
		authService:          authService,
		jwtManager:           jwtManager,
		captchaEnabled:       captchaEnabled,
		secondCaptchaEnabled: secondCaptchaEnabled,
	}
}

type loginRequest struct {
	Username      string `json:"username" binding:"required"`
	Password      string `json:"password" binding:"required"`
	Captcha       string `json:"captcha"`
	SecondCaptcha string `json:"second_captcha"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required"`
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}
	if h.captchaEnabled && strings.TrimSpace(req.Captcha) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "验证码不能为空"})
		return
	}
	if h.secondCaptchaEnabled && strings.TrimSpace(req.SecondCaptcha) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "二次验证码不能为空"})
		return
	}

	token, user, err := h.authService.Login(req.Username, req.Password, c.ClientIP())
	if err != nil {
		if lockedErr, ok := auth.IsLoginLocked(err); ok {
			retryAfter := lockedErr.RetryAfterSeconds(time.Now())
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			c.JSON(http.StatusLocked, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user": gin.H{
			"id":       user.ID,
			"username": user.Username,
			"role":     user.Role,
		},
	})
}

func (h *AuthHandler) Me(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"user": gin.H{
			"id":       c.GetUint(middleware.CtxUserID),
			"username": c.GetString(middleware.CtxUsername),
			"role":     c.GetString(middleware.CtxRole),
		},
	})
}

func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	userID := c.GetUint(middleware.CtxUserID)
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}

	if err := h.authService.ChangePassword(userID, req.CurrentPassword, req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "密码修改成功"})
}

func (h *AuthHandler) Logout(c *gin.Context) {
	if h.jwtManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "认证服务不可用"})
		return
	}

	token := c.GetString(middleware.CtxToken)
	if strings.TrimSpace(token) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 token"})
		return
	}

	if err := h.jwtManager.RevokeToken(token); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "注销失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已安全退出"})
}
