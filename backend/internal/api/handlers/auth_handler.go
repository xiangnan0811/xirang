package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)



type AuthHandler struct {
	authService          *auth.Service
	jwtManager           *auth.JWTManager
	db                   *gorm.DB
	captchaEnabled       bool
	secondCaptchaEnabled bool
	captchaStore         *CaptchaStore
}

func NewAuthHandler(authService *auth.Service, jwtManager *auth.JWTManager, captchaEnabled bool, secondCaptchaEnabled bool) *AuthHandler {
	return &AuthHandler{
		authService:          authService,
		jwtManager:           jwtManager,
		captchaEnabled:       captchaEnabled,
		secondCaptchaEnabled: secondCaptchaEnabled,
	}
}

// WithDB 注入数据库，用于 2FA 相关操作。
func (h *AuthHandler) WithDB(db *gorm.DB) *AuthHandler {
	h.db = db
	return h
}

// WithCaptchaStore 注入验证码存储，用于在 Login 中校验验证码。
func (h *AuthHandler) WithCaptchaStore(store *CaptchaStore) *AuthHandler {
	h.captchaStore = store
	return h
}

type loginRequest struct {
	Username       string `json:"username" binding:"required"`
	Password       string `json:"password" binding:"required"`
	Captcha        string `json:"captcha"`
	SecondCaptcha  string `json:"second_captcha"`
	CaptchaID      string `json:"captcha_id"`
	CaptchaAnswer  string `json:"captcha_answer"`
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
	if h.captchaEnabled && h.captchaStore == nil && strings.TrimSpace(req.Captcha) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "验证码不能为空"})
		return
	}
	if h.secondCaptchaEnabled && strings.TrimSpace(req.SecondCaptcha) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "二次验证码不能为空"})
		return
	}
	if h.captchaEnabled && h.captchaStore != nil {
		answerRaw := strings.TrimSpace(req.CaptchaAnswer)
		id := strings.TrimSpace(req.CaptchaID)
		if id == "" || answerRaw == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "验证码错误或已过期"})
			return
		}
		answerInt, err := strconv.Atoi(answerRaw)
		if err != nil || !h.captchaStore.Verify(id, answerInt) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "验证码错误或已过期"})
			return
		}
	}

	result, err := h.authService.Login(req.Username, req.Password, c.ClientIP())
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
	if result.Requires2FA {
		c.JSON(http.StatusOK, gin.H{
			"requires_2fa": true,
			"login_token":  result.LoginToken,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"token": result.Token,
		"user": gin.H{
			"id":           result.User.ID,
			"username":     result.User.Username,
			"role":         result.User.Role,
			"totp_enabled": result.User.TOTPEnabled,
		},
	})
}

func (h *AuthHandler) Me(c *gin.Context) {
	userID := c.GetUint(middleware.CtxUserID)
	totpEnabled := false
	onboarded := false
	if h.db != nil && userID != 0 {
		var user model.User
		if err := h.db.Select("totp_enabled", "onboarded").First(&user, userID).Error; err == nil {
			totpEnabled = user.TOTPEnabled
			onboarded = user.Onboarded
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"user": gin.H{
			"id":           userID,
			"username":     c.GetString(middleware.CtxUsername),
			"role":         c.GetString(middleware.CtxRole),
			"totp_enabled": totpEnabled,
			"onboarded":    onboarded,
		},
	})
}

// CompleteOnboarding 标记当前用户完成引导向导。
// POST /me/onboarded
func (h *AuthHandler) CompleteOnboarding(c *gin.Context) {
	userID := c.GetUint(middleware.CtxUserID)
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "服务不可用"})
		return
	}
	if err := h.db.Model(&model.User{}).Where("id = ?", userID).Update("onboarded", true).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "引导完成"})
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

// TOTPSetup POST /auth/2fa/setup — 生成 TOTP 密钥（未保存），返回二维码 URL 和密钥。
func (h *AuthHandler) TOTPSetup(c *gin.Context) {
	username := c.GetString(middleware.CtxUsername)
	key, err := auth.GenerateTOTPSecret("息壤 XiRang", username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成 TOTP 密钥失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"secret": key.Secret(),
		"qr_url": key.URL(),
		"issuer": "息壤 XiRang",
	})
}

type totpVerifyRequest struct {
	Secret string `json:"secret" binding:"required"`
	Code   string `json:"code" binding:"required"`
}

// TOTPVerify POST /auth/2fa/verify — 验证码正确后保存 TOTP 配置并返回恢复码。
func (h *AuthHandler) TOTPVerify(c *gin.Context) {
	if h.db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务不可用"})
		return
	}
	var req totpVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}
	if !auth.ValidateTOTP(req.Secret, req.Code) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "验证码错误"})
		return
	}
	recoveryCodes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成恢复码失败"})
		return
	}
	recoveryJSON, err := json.Marshal(recoveryCodes)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成恢复码失败"})
		return
	}
	userID := c.GetUint(middleware.CtxUserID)
	var user model.User
	if err := h.db.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}
	user.TOTPSecret = req.Secret
	user.TOTPEnabled = true
	user.RecoveryCodes = string(recoveryJSON)
	if err := h.db.Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存 2FA 配置失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"recovery_codes": recoveryCodes})
}

type totpDisableRequest struct {
	Password string `json:"password" binding:"required"`
	TOTPCode string `json:"totp_code" binding:"required"`
}

// TOTPDisable POST /auth/2fa/disable — 验证密码和 TOTP 码后禁用 2FA。
func (h *AuthHandler) TOTPDisable(c *gin.Context) {
	if h.db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务不可用"})
		return
	}
	var req totpDisableRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}
	userID := c.GetUint(middleware.CtxUserID)
	var user model.User
	if err := h.db.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}
	if err := auth.CheckPassword(user.PasswordHash, req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密码错误"})
		return
	}
	if !auth.ValidateTOTP(user.TOTPSecret, req.TOTPCode) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "验证码错误"})
		return
	}
	user.TOTPSecret = ""
	user.TOTPEnabled = false
	user.RecoveryCodes = ""
	if err := h.db.Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "禁用 2FA 失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "两步验证已禁用"})
}

type totpLoginRequest struct {
	LoginToken string `json:"login_token" binding:"required"`
	TOTPCode   string `json:"totp_code" binding:"required"`
}

// TOTPLogin POST /auth/2fa/login — 验证 TOTP 码或恢复码后返回完整 JWT。
func (h *AuthHandler) TOTPLogin(c *gin.Context) {
	if h.db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务不可用"})
		return
	}
	var req totpLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}
	claims, err := h.jwtManager.ParseToken(req.LoginToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "登录令牌无效或已过期"})
		return
	}
	if claims.Purpose != "2fa_pending" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "登录令牌无效"})
		return
	}
	var user model.User
	if err := h.db.First(&user, claims.UserID).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户不存在"})
		return
	}
	// 先尝试 TOTP 验证码，再尝试恢复码。
	if !auth.ValidateTOTP(user.TOTPSecret, req.TOTPCode) {
		if user.RecoveryCodes == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "验证码错误"})
			return
		}
		remaining, ok := auth.ValidateAndConsumeRecoveryCode(user.RecoveryCodes, req.TOTPCode)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "验证码错误"})
			return
		}
		newJSON, _ := json.Marshal(remaining)
		user.RecoveryCodes = string(newJSON)
		h.db.Save(&user)
	}
	token, err := h.jwtManager.GenerateToken(user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成 token 失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user": gin.H{
			"id":           user.ID,
			"username":     user.Username,
			"role":         user.Role,
			"totp_enabled": user.TOTPEnabled,
		},
	})
}
