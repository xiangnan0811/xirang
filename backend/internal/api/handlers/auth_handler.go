package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type AuthHandler struct {
	authService     *auth.Service
	jwtManager      *auth.JWTManager
	settingsSvc     *settings.Service
	db              *gorm.DB
	captchaStore    *CaptchaStore
	contentSessions ContentSessionRevoker
}

type ContentSessionRevoker interface {
	RevokeSession(context.Context, string, string) error
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

func (h *AuthHandler) WithContentSessionRevoker(revoker ContentSessionRevoker) *AuthHandler {
	h.contentSessions = revoker
	return h
}

// verifyCaptchaAnswer validates a store-backed captcha challenge once.
func verifyCaptchaAnswer(store *CaptchaStore, id, answerRaw string) bool {
	if store == nil {
		return false
	}
	id = strings.TrimSpace(id)
	answerRaw = strings.TrimSpace(answerRaw)
	if id == "" || answerRaw == "" {
		return false
	}
	answerInt, err := strconv.Atoi(answerRaw)
	if err != nil {
		return false
	}
	return store.Verify(id, answerInt)
}

type loginRequest struct {
	Username            string `json:"username" binding:"required"`
	Password            string `json:"password" binding:"required"`
	Captcha             string `json:"captcha"`
	SecondCaptcha       string `json:"second_captcha"` // legacy free-form; ignored for security (use second_captcha_id/answer)
	CaptchaID           string `json:"captcha_id"`
	CaptchaAnswer       string `json:"captcha_answer"`
	SecondCaptchaID     string `json:"second_captcha_id"`
	SecondCaptchaAnswer string `json:"second_captcha_answer"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required"`
}

// Login godoc
// @Summary      用户登录
// @Description  使用用户名和密码登录；离线恢复后仅返回受限 recovery_token 和 enrollment，不建立登录会话。所有响应禁止缓存。
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body  body      loginRequest  true  "登录请求"
// @Success      200   {object}  handlers.Response
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      423   {object}  handlers.Response
// @Failure      429   {object}  handlers.Response
// @Failure      500   {object}  handlers.Response
// @Router       /auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	captchaEnabled := h.captchaEnabled()
	secondCaptchaEnabled := h.secondCaptchaEnabled()

	// Primary captcha must use store-backed challenges only. A free-form
	// non-empty captcha string (legacy) is never accepted — including when the
	// store is missing (fail-closed, same as second captcha).
	if captchaEnabled {
		if h.captchaStore == nil {
			respondBadRequest(c, "验证码不可用，请联系管理员")
			return
		}
		if !verifyCaptchaAnswer(h.captchaStore, req.CaptchaID, req.CaptchaAnswer) {
			respondBadRequest(c, "验证码错误或已过期")
			return
		}
	}
	// Second captcha must use the same store-backed challenge/response as the
	// primary captcha. A free-form non-empty second_captcha string is no longer
	// accepted (that path provided only false security).
	if secondCaptchaEnabled {
		if h.captchaStore == nil {
			respondBadRequest(c, "二次验证码不可用，请联系管理员")
			return
		}
		if !verifyCaptchaAnswer(h.captchaStore, req.SecondCaptchaID, req.SecondCaptchaAnswer) {
			respondBadRequest(c, "二次验证码错误或已过期")
			return
		}
	}

	result, err := h.authService.Login(req.Username, req.Password, c.ClientIP())
	if err != nil {
		if errors.Is(err, auth.ErrRecoveryEnrollmentInternal) {
			respondInternalError(c, auth.ErrRecoveryEnrollmentInternal)
			return
		}
		if lockedErr, ok := auth.IsLoginLocked(err); ok {
			retryAfter := lockedErr.RetryAfterSeconds(time.Now())
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			respondLocked(c, err.Error(), gin.H{"retry_after": retryAfter})
			return
		}
		respondUnauthorized(c, err.Error())
		return
	}
	if enrollment := result.RecoveryEnrollment; enrollment != nil {
		respondOK(c, gin.H{
			"requires_reenrollment": true,
			"recovery_token":        enrollment.Token,
			"enrollment": gin.H{
				"secret":        enrollment.Setup.Secret,
				"qr_url":        enrollment.Setup.QRURL,
				"issuer":        enrollment.Setup.Issuer,
				"enrollment_id": enrollment.Setup.EnrollmentID,
				"expires_at":    enrollment.Setup.ExpiresAt,
			},
		})
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

type recoveryEnrollmentVerifyRequest struct {
	Code string `json:"code"`
}

type recoveryEnrollmentVerifyResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

// decodeRecoveryEnrollmentRequest accepts precisely one object containing one
// string field. Token-level decoding also rejects duplicate "code" fields,
// which encoding/json's ordinary struct decoding silently overwrites.
func decodeRecoveryEnrollmentRequest(body io.Reader) (recoveryEnrollmentVerifyRequest, error) {
	decoder := json.NewDecoder(body)
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return recoveryEnrollmentVerifyRequest{}, errors.New("invalid recovery enrollment request")
	}
	field, err := decoder.Token()
	if err != nil || field != "code" {
		return recoveryEnrollmentVerifyRequest{}, errors.New("invalid recovery enrollment request")
	}
	value, err := decoder.Token()
	code, ok := value.(string)
	if err != nil || !ok || len(code) != 6 {
		return recoveryEnrollmentVerifyRequest{}, errors.New("invalid recovery enrollment request")
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return recoveryEnrollmentVerifyRequest{}, errors.New("invalid recovery enrollment request")
		}
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') {
		return recoveryEnrollmentVerifyRequest{}, errors.New("invalid recovery enrollment request")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return recoveryEnrollmentVerifyRequest{}, errors.New("invalid recovery enrollment request")
	}
	return recoveryEnrollmentVerifyRequest{Code: code}, nil
}

func respondRecoveryEnrollmentError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, auth.ErrRecoveryEnrollmentInvalid):
		c.JSON(http.StatusUnauthorized, Response{
			Code: http.StatusUnauthorized, Message: "恢复绑定凭据无效或已过期",
			Data: gin.H{"error_code": "RECOVERY_ENROLLMENT_INVALID"},
		})
	case errors.Is(err, auth.ErrTOTPCodeInvalid):
		respondForbiddenData(c, "当前验证码错误", gin.H{"error_code": "TOTP_CODE_INVALID"})
	default:
		// Never log an underlying driver error, token, code or identity row.
		respondInternalError(c, auth.ErrRecoveryEnrollmentInternal)
	}
}

// RecoveryEnrollmentVerify godoc
// @Summary      完成离线账户恢复后的受限 2FA 绑定
// @Description  仅接受 recovery_enrollment Bearer 和六位新验证码；请求体最多 1 KiB，仅允许 code 字段。成功只返回本次八个恢复码，不签发登录会话；提交结果未知时不得自动重放。所有响应禁止缓存。
// @Tags         auth
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      recoveryEnrollmentVerifyRequest  true  "新验证器验证码"
// @Success      200   {object}  handlers.Response{data=recoveryEnrollmentVerifyResponse}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      403   {object}  handlers.Response
// @Failure      429   {object}  handlers.Response
// @Failure      500   {object}  handlers.Response
// @Router       /auth/recovery/enrollment/verify [post]
func (h *AuthHandler) RecoveryEnrollmentVerify(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	if h.authService == nil || h.jwtManager == nil {
		respondRecoveryEnrollmentError(c, auth.ErrRecoveryEnrollmentInternal)
		return
	}
	headers := c.Request.Header.Values("Authorization")
	if len(headers) != 1 || !strings.HasPrefix(headers[0], "Bearer ") {
		respondRecoveryEnrollmentError(c, auth.ErrRecoveryEnrollmentInvalid)
		return
	}
	token := strings.TrimPrefix(headers[0], "Bearer ")
	if token == "" || strings.ContainsAny(token, " \t\r\n,") {
		respondRecoveryEnrollmentError(c, auth.ErrRecoveryEnrollmentInvalid)
		return
	}
	// Authenticate the capability before parsing input; the service repeats
	// stricter claim and persisted-identity checks at the transaction boundary.
	claims, err := h.jwtManager.ParseToken(token)
	if err != nil || claims.Purpose != auth.PurposeRecoveryEnrollment {
		respondRecoveryEnrollmentError(c, auth.ErrRecoveryEnrollmentInvalid)
		return
	}
	if c.Request.Body == nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request, err := decodeRecoveryEnrollmentRequest(c.Request.Body)
	if err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	codes, err := h.authService.CompleteRecoveryEnrollment(c.Request.Context(), token, request.Code)
	if err != nil {
		respondRecoveryEnrollmentError(c, err)
		return
	}
	respondOK(c, recoveryEnrollmentVerifyResponse{RecoveryCodes: codes})
}

// Me godoc
// @Summary      获取当前用户信息
// @Description  返回当前已认证用户的基本信息
// @Tags         auth
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /me [get]
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
		respondServiceUnavailable(c, "服务不可用")
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
		respondServiceUnavailable(c, "认证服务不可用")
		return
	}

	binding, ok := middleware.CurrentSessionBinding(c)
	if !ok {
		respondBadRequest(c, "缺少会话绑定")
		return
	}

	if err := h.jwtManager.RevokeSession(binding.JTI, binding.UserID, binding.ExpiresAt); err != nil {
		respondInternalError(c, fmt.Errorf("注销 token 失败: %w", err))
		return
	}
	if h.contentSessions != nil {
		// JWT revocation is authoritative and has already succeeded. Content
		// cancellation is best-effort cleanup; its implementation records a
		// safe reconciliation signal without returning raw identifiers here.
		_ = h.contentSessions.RevokeSession(c.Request.Context(), binding.JTI, "logout")
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
	if h.authService == nil {
		respondInternalError(c, fmt.Errorf("认证服务未注入"))
		return
	}
	userID := c.GetUint(middleware.CtxUserID)
	result, err := h.authService.SetupTOTP(c.Request.Context(), userID, c.GetString(middleware.CtxUsername))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrTOTPAlreadyEnabled),
			errors.Is(err, auth.ErrTOTPEnrollmentConflict),
			errors.Is(err, auth.ErrSecurityConflict),
			errors.Is(err, gorm.ErrRecordNotFound):
			respondBadRequest(c, err.Error())
		default:
			respondInternalError(c, fmt.Errorf("保存密钥失败: %w", err))
		}
		return
	}
	respondOK(c, gin.H{
		"secret":        result.Secret,
		"qr_url":        result.QRURL,
		"issuer":        result.Issuer,
		"enrollment_id": result.EnrollmentID,
		"expires_at":    result.ExpiresAt.UTC(),
	})
}

type totpVerifyRequest struct {
	Code         string `json:"code"`
	EnrollmentID string `json:"enrollment_id"`
}

type totpVerifyUserResponse struct {
	ID          uint   `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	TOTPEnabled bool   `json:"totp_enabled"`
}

type totpVerifyResponse struct {
	Token         string                 `json:"token"`
	User          totpVerifyUserResponse `json:"user"`
	RecoveryCodes []string               `json:"recovery_codes"`
}

func respondTOTPVerifyFailure(c *gin.Context, err error) {
	if code, ok := auth.TOTPVerifyErrorCodeFor(err); ok {
		respondBadRequestData(c, err.Error(), gin.H{"error_code": string(code)})
		return
	}
	switch {
	case errors.Is(err, auth.ErrTOTPAlreadyEnabled),
		errors.Is(err, auth.ErrSecurityConflict):
		// These states can mean another request has already changed the
		// account. Do not attach a retryable activation code.
		respondBadRequest(c, err.Error())
	default:
		respondInternalError(c, fmt.Errorf("保存 2FA 配置失败: %w", err))
	}
}

// TOTPVerify godoc
// @Summary      验证并激活 2FA
// @Description  使用当前会话绑定验证 TOTP；成功返回同 JTI、同到期时间的替换主令牌、安全用户信息及本次恢复码，不授予 step-up
// @Tags         auth
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      totpVerifyRequest  true  "TOTP 验证码"
// @Success      200   {object}  handlers.Response{data=totpVerifyResponse}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /auth/2fa/verify [post]
func (h *AuthHandler) TOTPVerify(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	if h.authService == nil {
		respondInternalError(c, fmt.Errorf("认证服务未注入"))
		return
	}
	var req totpVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	binding, ok := middleware.CurrentSessionBinding(c)
	if !ok || binding.UserID == 0 || binding.JTI == "" || binding.ExpiresAt.IsZero() {
		respondUnauthorized(c, "登录会话无效")
		return
	}
	result, err := h.authService.VerifyTOTP(c.Request.Context(), auth.TOTPActivationSession{
		JTI:          binding.JTI,
		UserID:       binding.UserID,
		Role:         binding.Role,
		TokenVersion: binding.TokenVersion,
		ExpiresAt:    binding.ExpiresAt,
	}, req.Code, req.EnrollmentID)
	if err != nil {
		respondTOTPVerifyFailure(c, err)
		return
	}
	if result == nil || strings.TrimSpace(result.Token) == "" || result.User.ID == 0 ||
		strings.TrimSpace(result.User.Username) == "" || strings.TrimSpace(result.User.Role) == "" ||
		!result.User.TOTPEnabled || result.RecoveryCodes == nil {
		respondInternalError(c, fmt.Errorf("2FA 激活响应无效"))
		return
	}
	respondOK(c, totpVerifyResponse{
		Token: result.Token,
		User: totpVerifyUserResponse{
			ID:          result.User.ID,
			Username:    result.User.Username,
			Role:        result.User.Role,
			TOTPEnabled: true,
		},
		RecoveryCodes: result.RecoveryCodes,
	})
}

type stepUpRequest struct {
	Code         string            `json:"code" binding:"required"`
	StepUpAction auth.StepUpAction `json:"step_up_action" binding:"required"`
}

type stepUpResponse struct {
	Proof           string    `json:"proof"`
	ExpiresAt       time.Time `json:"expires_at"`
	ProofTTLSeconds int       `json:"proof_ttl_seconds"`
}

func (h *AuthHandler) StepUp(c *gin.Context) {
	if h.db == nil || h.jwtManager == nil {
		respondInternalError(c, fmt.Errorf("step-up 服务未初始化"))
		return
	}
	var req stepUpRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if !auth.IsValidStepUpAction(req.StepUpAction) {
		respondBadRequest(c, "step_up_action 不合法")
		return
	}
	userID := c.GetUint(middleware.CtxUserID)
	if userID == 0 {
		respondUnauthorized(c, "未登录")
		return
	}
	session, ok := middleware.CurrentSessionBinding(c)
	if !ok || session.UserID != userID {
		respondUnauthorized(c, "登录会话无效")
		return
	}
	var user model.User
	if err := h.db.First(&user, userID).Error; err != nil {
		respondUnauthorized(c, "用户不存在")
		return
	}
	if !user.TOTPEnabled || strings.TrimSpace(user.TOTPSecret) == "" {
		writeStepUpCredentialAudit(c, h.db, nil, stepUpAuditOperation{ExpectedAction: req.StepUpAction, Action: string(req.StepUpAction), Purpose: auth.PurposeStepUp, Operation: "step_up"}, credentialaudit.OutcomeBlocked, "unavailable")
		respondForbidden(c, "请先启用两步验证")
		return
	}
	if !auth.ValidateTOTP(user.TOTPSecret, req.Code) {
		writeStepUpCredentialAudit(c, h.db, nil, stepUpAuditOperation{ExpectedAction: req.StepUpAction, Action: string(req.StepUpAction), Purpose: auth.PurposeStepUp, Operation: "step_up"}, credentialaudit.OutcomeFailure, "failed")
		respondForbidden(c, "二次验证失败")
		return
	}
	proof, expiresAt, err := h.jwtManager.GenerateStepUpToken(user, req.StepUpAction, session.JTI)
	if err != nil {
		respondInternalError(c, fmt.Errorf("生成 step-up proof 失败: %w", err))
		return
	}
	writeStepUpCredentialAudit(c, h.db, nil, stepUpAuditOperation{ExpectedAction: req.StepUpAction, Action: string(req.StepUpAction), Purpose: auth.PurposeStepUp, Operation: "step_up"}, credentialaudit.OutcomeSuccess, "issued")
	respondOK(c, stepUpResponse{
		Proof: proof, ExpiresAt: expiresAt.UTC(),
		ProofTTLSeconds: stepUpProofTTLSecondsForAction(req.StepUpAction),
	})
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
func (h *AuthHandler) TOTPDisable(c *gin.Context) {
	if h.authService == nil {
		respondInternalError(c, fmt.Errorf("认证服务未注入"))
		return
	}
	var req totpDisableRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if err := h.authService.DisableTOTP(c.Request.Context(), c.GetUint(middleware.CtxUserID), req.Password, req.TOTPCode); err != nil {
		switch {
		case errors.Is(err, auth.ErrTOTPCodeInvalid),
			strings.Contains(err.Error(), "密码错误"):
			respondBadRequest(c, err.Error())
		case errors.Is(err, auth.ErrSecurityConflict):
			respondBadRequest(c, err.Error())
		default:
			respondInternalError(c, fmt.Errorf("禁用 2FA 失败: %w", err))
		}
		return
	}
	respondMessage(c, "两步验证已禁用")
}

type recoveryCodesRemainingResponse struct {
	Remaining int `json:"remaining"`
}

type regenerateRecoveryCodesRequest struct {
	Code string `json:"code"`
}

type regenerateRecoveryCodesResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

func recoveryCodeSessionFromContext(c *gin.Context) (auth.RecoveryCodeSession, bool) {
	binding, ok := middleware.CurrentSessionBinding(c)
	if !ok || binding.JTI == "" || binding.UserID == 0 || binding.ExpiresAt.IsZero() {
		return auth.RecoveryCodeSession{}, false
	}
	return auth.RecoveryCodeSession{
		JTI:          binding.JTI,
		UserID:       binding.UserID,
		Role:         binding.Role,
		TokenVersion: binding.TokenVersion,
		ExpiresAt:    binding.ExpiresAt,
	}, true
}

func respondRecoveryCodeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, auth.ErrRecoveryCodeSessionInvalid):
		respondUnauthorized(c, "登录会话无效或已过期")
	case errors.Is(err, auth.ErrRecoveryCodeTOTPRequired):
		respondBadRequest(c, "请输入当前验证码")
	case errors.Is(err, auth.ErrRecoveryCodeTOTPInvalid):
		respondForbiddenData(c, "当前验证码错误", gin.H{
			"error_code": "RECOVERY_CODE_TOTP_INVALID",
		})
	case errors.Is(err, auth.ErrRecoveryCodeTOTPDisabled):
		respondConflictData(c, "两步验证未启用", gin.H{
			"error_code": "RECOVERY_CODE_TOTP_DISABLED",
		})
	case errors.Is(err, auth.ErrRecoveryCodeConflict):
		respondConflictData(c, "恢复码状态已变更，请重试", gin.H{
			"error_code": "RECOVERY_CODE_CONFLICT",
		})
	default:
		respondInternalError(c, fmt.Errorf("恢复码操作失败: %w", err))
	}
}

// RecoveryCodesRemaining godoc
// @Summary      查询恢复码剩余数量
// @Description  返回当前登录用户尚未使用的恢复码数量，不返回恢复码内容
// @Tags         auth
// @Security     Bearer
// @Produce      json
// @Success      200   {object} handlers.Response{data=recoveryCodesRemainingResponse}
// @Failure      401   {object} handlers.Response
// @Failure      409   {object} handlers.Response
// @Failure      500   {object} handlers.Response
// @Router       /auth/2fa/recovery-codes [get]
func (h *AuthHandler) RecoveryCodesRemaining(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	if h.authService == nil {
		respondInternalError(c, fmt.Errorf("认证服务未注入"))
		return
	}
	session, ok := recoveryCodeSessionFromContext(c)
	if !ok {
		respondUnauthorized(c, "登录会话无效或已过期")
		return
	}
	remaining, err := h.authService.RecoveryCodesRemaining(c.Request.Context(), session)
	if err != nil {
		respondRecoveryCodeError(c, err)
		return
	}
	if remaining < 0 {
		respondInternalError(c, fmt.Errorf("恢复码剩余数量无效"))
		return
	}
	respondOK(c, recoveryCodesRemainingResponse{Remaining: remaining})
}

// RegenerateRecoveryCodes godoc
// @Summary      安全重生成恢复码
// @Description  使用当前验证器验证码原子替换当前用户的全部恢复码；旧恢复码立即失效
// @Tags         auth
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      regenerateRecoveryCodesRequest  true  "当前验证器验证码"
// @Success      200   {object} handlers.Response{data=regenerateRecoveryCodesResponse}
// @Failure      400   {object} handlers.Response
// @Failure      401   {object} handlers.Response
// @Failure      403   {object} handlers.Response
// @Failure      409   {object} handlers.Response
// @Failure      429   {object} handlers.Response
// @Failure      500   {object} handlers.Response
// @Router       /auth/2fa/recovery-codes/regenerate [post]
func (h *AuthHandler) RegenerateRecoveryCodes(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	if h.authService == nil {
		respondInternalError(c, fmt.Errorf("认证服务未注入"))
		return
	}
	var req regenerateRecoveryCodesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	session, ok := recoveryCodeSessionFromContext(c)
	if !ok {
		respondUnauthorized(c, "登录会话无效或已过期")
		return
	}
	recoveryCodes, err := h.authService.RegenerateRecoveryCodes(c.Request.Context(), session, req.Code)
	if err != nil {
		respondRecoveryCodeError(c, err)
		return
	}
	if len(recoveryCodes) == 0 {
		respondInternalError(c, fmt.Errorf("恢复码生成响应无效"))
		return
	}
	respondOK(c, regenerateRecoveryCodesResponse{RecoveryCodes: recoveryCodes})
}

type totpLoginRequest struct {
	LoginToken string `json:"login_token" binding:"required"`
	TOTPCode   string `json:"totp_code" binding:"required"`
}

// TOTPLogin godoc
// @Summary      2FA 二步登录
// @Description  使用预登录令牌和 TOTP 验证码（或恢复码）完成登录，返回完整 JWT；所有响应禁止缓存
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body  body      totpLoginRequest  true  "2FA 登录请求"
// @Success      200   {object}  handlers.Response
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /auth/2fa/login [post]
func (h *AuthHandler) TOTPLogin(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	if h.authService == nil {
		respondInternalError(c, fmt.Errorf("认证服务未注入"))
		return
	}
	var req totpLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := h.authService.Complete2FALogin(c.Request.Context(), req.LoginToken, req.TOTPCode)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrPendingLoginInvalid),
			errors.Is(err, auth.ErrRecoveryCodeInvalid):
			respondUnauthorized(c, err.Error())
		default:
			respondInternalError(c, fmt.Errorf("完成 2FA 登录失败: %w", err))
		}
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
