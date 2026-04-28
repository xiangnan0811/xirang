package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type AuthHandler struct {
	authService  *auth.Service
	jwtManager   *auth.JWTManager
	settingsSvc  *settings.Service
	db           *gorm.DB
	captchaStore *CaptchaStore
}

// NewAuthHandler 构造登录 handler。验证码开关从 settings 服务按需读取（key:
// login.captcha_enabled / login.second_captcha_enabled），不在构造期捕获，
// 这样 settings API 的修改可以即时生效，无需重启进程。settingsSvc 为 nil
// 时（仅测试场景）等价于两项验证码均关闭。
func NewAuthHandler(authService *auth.Service, jwtManager *auth.JWTManager, settingsSvc *settings.Service) *AuthHandler {
	return &AuthHandler{
		authService: authService,
		jwtManager:  jwtManager,
		settingsSvc: settingsSvc,
	}
}

// captchaEnabled 动态读取 login.captcha_enabled 设置；nil settings 视为关闭。
func (h *AuthHandler) captchaEnabled() bool {
	if h.settingsSvc == nil {
		return false
	}
	return strings.ToLower(h.settingsSvc.GetEffective("login.captcha_enabled")) == "true"
}

// secondCaptchaEnabled 动态读取 login.second_captcha_enabled 设置；nil settings 视为关闭。
func (h *AuthHandler) secondCaptchaEnabled() bool {
	if h.settingsSvc == nil {
		return false
	}
	return strings.ToLower(h.settingsSvc.GetEffective("login.second_captcha_enabled")) == "true"
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
	Username      string `json:"username" binding:"required"`
	Password      string `json:"password" binding:"required"`
	Captcha       string `json:"captcha"`
	SecondCaptcha string `json:"second_captcha"`
	CaptchaID     string `json:"captcha_id"`
	CaptchaAnswer string `json:"captcha_answer"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required"`
}

// Login godoc
// @Summary      用户登录
// @Description  使用用户名和密码登录，支持验证码和 2FA 预登录流程
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body  body      loginRequest  true  "登录请求"
// @Success      200   {object}  handlers.Response
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	captchaEnabled := h.captchaEnabled()

	if captchaEnabled && h.captchaStore == nil && strings.TrimSpace(req.Captcha) == "" {
		respondBadRequest(c, "验证码不能为空")
		return
	}
	if h.secondCaptchaEnabled() && strings.TrimSpace(req.SecondCaptcha) == "" {
		respondBadRequest(c, "二次验证码不能为空")
		return
	}
	if captchaEnabled && h.captchaStore != nil {
		answerRaw := strings.TrimSpace(req.CaptchaAnswer)
		id := strings.TrimSpace(req.CaptchaID)
		if id == "" || answerRaw == "" {
			respondBadRequest(c, "验证码错误或已过期")
			return
		}
		answerInt, err := strconv.Atoi(answerRaw)
		if err != nil || !h.captchaStore.Verify(id, answerInt) {
			respondBadRequest(c, "验证码错误或已过期")
			return
		}
	}

	result, err := h.authService.Login(req.Username, req.Password, c.ClientIP())
	if err != nil {
		if lockedErr, ok := auth.IsLoginLocked(err); ok {
			retryAfter := lockedErr.RetryAfterSeconds(time.Now())
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			c.JSON(http.StatusLocked, Response{Code: http.StatusLocked, Message: err.Error(), Data: gin.H{"retry_after": retryAfter}})
			return
		}
		respondUnauthorized(c, err.Error())
		return
	}
	if result.Requires2FA {
		respondOK(c, gin.H{
			"requires_2fa": true,
			"login_token":  result.LoginToken,
		})
		return
	}
	respondOK(c, gin.H{
		"token": result.Token,
		"user": gin.H{
			"id":           result.User.ID,
			"username":     result.User.Username,
			"role":         result.User.Role,
			"totp_enabled": result.User.TOTPEnabled,
		},
	})
}

// Me godoc
// @Summary      获取当前用户信息
// @Description  返回当前已认证用户的基本信息
// @Tags         auth
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /auth/me [get]
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
	respondOK(c, gin.H{
		"user": gin.H{
			"id":           userID,
			"username":     c.GetString(middleware.CtxUsername),
			"role":         c.GetString(middleware.CtxRole),
			"totp_enabled": totpEnabled,
			"onboarded":    onboarded,
		},
	})
}

// CompleteOnboarding godoc
// @Summary      完成引导向导
// @Description  标记当前用户已完成新手引导向导
// @Tags         auth
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /me/onboarded [post]
func (h *AuthHandler) CompleteOnboarding(c *gin.Context) {
	userID := c.GetUint(middleware.CtxUserID)
	if userID == 0 {
		respondUnauthorized(c, "未登录")
		return
	}
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, Response{Code: http.StatusServiceUnavailable, Message: "服务不可用", Data: nil})
		return
	}
	if err := h.db.Model(&model.User{}).Where("id = ?", userID).Update("onboarded", true).Error; err != nil {
		respondInternalError(c, fmt.Errorf("更新 onboarded 失败: %w", err))
		return
	}
	respondMessage(c, "引导完成")
}

// ChangePassword godoc
// @Summary      修改密码
// @Description  修改当前用户的密码
// @Tags         auth
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      changePasswordRequest  true  "修改密码请求"
// @Success      200   {object}  handlers.Response
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /auth/change-password [post]
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	userID := c.GetUint(middleware.CtxUserID)
	if userID == 0 {
		respondUnauthorized(c, "未登录")
		return
	}

	if err := h.authService.ChangePassword(userID, req.CurrentPassword, req.NewPassword); err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	respondMessage(c, "密码修改成功")
}

// Logout godoc
// @Summary      退出登录
// @Description  撤销当前 JWT 令牌，安全退出
// @Tags         auth
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /auth/logout [post]
func (h *AuthHandler) Logout(c *gin.Context) {
	if h.jwtManager == nil {
		c.JSON(http.StatusServiceUnavailable, Response{Code: http.StatusServiceUnavailable, Message: "认证服务不可用", Data: nil})
		return
	}

	token := c.GetString(middleware.CtxToken)
	if strings.TrimSpace(token) == "" {
		respondBadRequest(c, "缺少 token")
		return
	}

	if err := h.jwtManager.RevokeToken(token); err != nil {
		respondBadRequest(c, "注销失败")
		return
	}

	respondMessage(c, "已安全退出")
}

// TOTPSetup godoc
// @Summary      初始化 2FA 密钥
// @Description  生成 TOTP 密钥并暂存，返回二维码 URL 和密钥（需调用 verify 激活）
// @Tags         auth
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /auth/2fa/setup [post]
func (h *AuthHandler) TOTPSetup(c *gin.Context) {
	if h.db == nil {
		respondInternalError(c, fmt.Errorf("db 未注入"))
		return
	}
	username := c.GetString(middleware.CtxUsername)
	key, err := auth.GenerateTOTPSecret("息壤 XiRang", username)
	if err != nil {
		respondInternalError(c, fmt.Errorf("生成 TOTP 密钥失败: %w", err))
		return
	}

	// 将 pending secret 存入 DB（TOTPEnabled 保持 false，直到 verify 成功）
	userID := c.GetUint(middleware.CtxUserID)
	if err := h.db.Model(&model.User{}).Where("id = ?", userID).
		Update("totp_secret", key.Secret()).Error; err != nil {
		respondInternalError(c, fmt.Errorf("保存密钥失败: %w", err))
		return
	}

	respondOK(c, gin.H{
		"secret": key.Secret(),
		"qr_url": key.URL(),
		"issuer": "息壤 XiRang",
	})
}

type totpVerifyRequest struct {
	Code string `json:"code" binding:"required"`
}

// TOTPVerify godoc
// @Summary      验证并激活 2FA
// @Description  使用服务端暂存的密钥校验验证码，成功后启用 2FA 并返回恢复码
// @Tags         auth
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      totpVerifyRequest  true  "TOTP 验证码"
// @Success      200   {object}  handlers.Response
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /auth/2fa/verify [post]
func (h *AuthHandler) TOTPVerify(c *gin.Context) {
	if h.db == nil {
		respondInternalError(c, fmt.Errorf("db 未注入"))
		return
	}
	var req totpVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	userID := c.GetUint(middleware.CtxUserID)
	var user model.User
	if err := h.db.First(&user, userID).Error; err != nil {
		respondNotFound(c, "用户不存在")
		return
	}
	// 使用服务端暂存的 pending secret 校验，拒绝客户端提供的 secret
	if strings.TrimSpace(user.TOTPSecret) == "" {
		respondBadRequest(c, "请先调用 setup 接口生成密钥")
		return
	}
	if user.TOTPEnabled {
		respondBadRequest(c, "两步验证已启用")
		return
	}
	if !auth.ValidateTOTP(user.TOTPSecret, req.Code) {
		respondBadRequest(c, "验证码错误")
		return
	}
	recoveryCodes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		respondInternalError(c, fmt.Errorf("生成恢复码失败: %w", err))
		return
	}
	recoveryJSON, err := json.Marshal(recoveryCodes)
	if err != nil {
		respondInternalError(c, fmt.Errorf("序列化恢复码失败: %w", err))
		return
	}
	user.TOTPEnabled = true
	user.RecoveryCodes = string(recoveryJSON)
	if err := h.db.Save(&user).Error; err != nil {
		respondInternalError(c, fmt.Errorf("保存 2FA 配置失败: %w", err))
		return
	}
	respondOK(c, gin.H{"recovery_codes": recoveryCodes})
}

type totpDisableRequest struct {
	Password string `json:"password" binding:"required"`
	TOTPCode string `json:"totp_code" binding:"required"`
}

// TOTPDisable godoc
// @Summary      禁用 2FA
// @Description  验证密码和 TOTP 码后禁用两步验证
// @Tags         auth
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      totpDisableRequest  true  "禁用 2FA 请求"
// @Success      200   {object}  handlers.Response
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /auth/2fa/disable [post]
func (h *AuthHandler) TOTPDisable(c *gin.Context) {
	if h.db == nil {
		respondInternalError(c, fmt.Errorf("db 未注入"))
		return
	}
	var req totpDisableRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	userID := c.GetUint(middleware.CtxUserID)
	var user model.User
	if err := h.db.First(&user, userID).Error; err != nil {
		respondNotFound(c, "用户不存在")
		return
	}
	if err := auth.CheckPassword(user.PasswordHash, req.Password); err != nil {
		respondBadRequest(c, "密码错误")
		return
	}
	if !auth.ValidateTOTP(user.TOTPSecret, req.TOTPCode) {
		respondBadRequest(c, "验证码错误")
		return
	}
	if err := h.db.Model(&user).Updates(map[string]any{
		"totp_secret":    "",
		"totp_enabled":   false,
		"recovery_codes": "",
		"token_version":  gorm.Expr("token_version + 1"),
	}).Error; err != nil {
		respondInternalError(c, fmt.Errorf("禁用 2FA 失败: %w", err))
		return
	}
	respondMessage(c, "两步验证已禁用")
}

type totpLoginRequest struct {
	LoginToken string `json:"login_token" binding:"required"`
	TOTPCode   string `json:"totp_code" binding:"required"`
}

// TOTPLogin godoc
// @Summary      2FA 二步登录
// @Description  使用预登录令牌和 TOTP 验证码（或恢复码）完成登录，返回完整 JWT
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body  body      totpLoginRequest  true  "2FA 登录请求"
// @Success      200   {object}  handlers.Response
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /auth/2fa/login [post]
func (h *AuthHandler) TOTPLogin(c *gin.Context) {
	if h.db == nil {
		respondInternalError(c, fmt.Errorf("db 未注入"))
		return
	}
	var req totpLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	claims, err := h.jwtManager.ParseToken(req.LoginToken)
	if err != nil {
		respondUnauthorized(c, "登录令牌无效或已过期")
		return
	}
	if claims.Purpose != "2fa_pending" {
		respondUnauthorized(c, "登录令牌无效")
		return
	}
	var user model.User
	if err := h.db.First(&user, claims.UserID).Error; err != nil {
		respondUnauthorized(c, "用户不存在")
		return
	}
	// 先尝试 TOTP 验证码，再尝试恢复码。
	if !auth.ValidateTOTP(user.TOTPSecret, req.TOTPCode) {
		if user.RecoveryCodes == "" {
			respondUnauthorized(c, "验证码错误")
			return
		}
		remaining, ok := auth.ValidateAndConsumeRecoveryCode(user.RecoveryCodes, req.TOTPCode)
		if !ok {
			respondUnauthorized(c, "验证码错误")
			return
		}
		newJSON, _ := json.Marshal(remaining)
		user.RecoveryCodes = string(newJSON)
		h.db.Save(&user)
	}
	token, err := h.jwtManager.GenerateToken(user)
	if err != nil {
		respondInternalError(c, fmt.Errorf("生成 token 失败: %w", err))
		return
	}
	respondOK(c, gin.H{
		"token": token,
		"user": gin.H{
			"id":           user.ID,
			"username":     user.Username,
			"role":         user.Role,
			"totp_enabled": user.TOTPEnabled,
		},
	})
}
