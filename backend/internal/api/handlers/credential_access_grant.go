package handlers

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	CredentialGrantStatusRequested = "requested"
	CredentialGrantStatusApproved  = "approved"
	CredentialGrantStatusActive    = "active"
	CredentialGrantStatusDenied    = "denied"
	CredentialGrantStatusExpired   = "expired"
	CredentialGrantStatusRevoked   = "revoked"

	CredentialGrantActionTerminalOpen = "terminal.open"

	credentialGrantKind   = "jit_grant"
	credentialGrantSource = "credential_access_grant"

	credentialGrantRequiredCode = "CREDENTIAL_GRANT_REQUIRED"
	credentialGrantMaxReasonLen = 240
	credentialGrantMinTTL       = time.Minute
	credentialGrantDefaultTTL   = 10 * time.Minute
	credentialGrantMaxTTL       = 30 * time.Minute
)

var (
	ErrCredentialGrantRequired = errors.New("credential access grant required")
	ErrCredentialGrantInvalid  = errors.New("credential access grant invalid")
	ErrCredentialGrantExpired  = errors.New("credential access grant expired")
	ErrCredentialGrantRevoked  = errors.New("credential access grant revoked")
	ErrCredentialGrantDenied   = errors.New("credential access grant denied")
)

type CredentialAccessGrantHandler struct {
	db         *gorm.DB
	jwtManager *auth.JWTManager
}

type terminalCredentialGrantRequest struct {
	NodeID              uint   `json:"node_id" binding:"required"`
	Reason              string `json:"reason" binding:"required"`
	RequestedTTLSeconds int    `json:"requested_ttl_seconds"`
}

type credentialGrantDTO struct {
	ID                  uint       `json:"id"`
	RequesterUserID     uint       `json:"requester_user_id"`
	RequesterUsername   string     `json:"requester_username"`
	RequesterRole       string     `json:"requester_role"`
	Action              string     `json:"action"`
	Purpose             string     `json:"purpose"`
	NodeID              *uint      `json:"node_id,omitempty"`
	TaskID              *uint      `json:"task_id,omitempty"`
	PolicyID            *uint      `json:"policy_id,omitempty"`
	Reason              string     `json:"reason"`
	Status              string     `json:"status"`
	RequestedTTLSeconds int        `json:"requested_ttl_seconds"`
	RequestedAt         time.Time  `json:"requested_at"`
	ApprovedAt          *time.Time `json:"approved_at,omitempty"`
	ApproverUserID      *uint      `json:"approver_user_id,omitempty"`
	ApproverUsername    string     `json:"approver_username,omitempty"`
	ExpiresAt           time.Time  `json:"expires_at"`
	RevokedAt           *time.Time `json:"revoked_at,omitempty"`
	RevokedByUserID     *uint      `json:"revoked_by_user_id,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

func NewCredentialAccessGrantHandler(db *gorm.DB, jwtManager *auth.JWTManager) *CredentialAccessGrantHandler {
	return &CredentialAccessGrantHandler{db: db, jwtManager: jwtManager}
}

func (h *CredentialAccessGrantHandler) RequestTerminalGrant(c *gin.Context) {
	var req terminalCredentialGrantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if req.NodeID == 0 {
		respondBadRequest(c, "节点 ID 无效")
		return
	}
	reason, err := sanitizeCredentialGrantReason(req.Reason)
	if err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	ttl, err := normalizeCredentialGrantTTL(req.RequestedTTLSeconds)
	if err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if h.db == nil {
		respondInternalError(c, fmt.Errorf("credential grant db unavailable"))
		return
	}
	if !enforceStepUpForContext(c, h.db, h.jwtManager, stepUpAuditOperation{Action: CredentialGrantActionTerminalOpen, Purpose: sshutil.PurposeTerminal, Operation: "terminal_grant"}) {
		return
	}
	if err := validateGrantRequesterContext(c, h.db); err != nil {
		respondForbidden(c, "认证状态已变化，请重新登录")
		return
	}

	var node model.Node
	if err := h.db.Select("id").First(&node, req.NodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondNotFound(c, "节点不存在")
			return
		}
		respondInternalError(c, err)
		return
	}

	now := time.Now().UTC()
	grant := model.CredentialAccessGrant{
		RequesterUserID:     middleware.CurrentUserID(c),
		RequesterUsername:   normalizeCredentialGrantIdentity(c.GetString(middleware.CtxUsername), 64),
		RequesterRole:       normalizeCredentialGrantIdentity(middleware.CurrentRole(c), 32),
		Action:              CredentialGrantActionTerminalOpen,
		Purpose:             sshutil.PurposeTerminal,
		NodeID:              credentialaudit.PtrUint(req.NodeID),
		Reason:              reason,
		Status:              CredentialGrantStatusActive,
		RequestedTTLSeconds: int(ttl.Seconds()),
		RequestedAt:         now,
		ApprovedAt:          &now,
		ApproverUserID:      credentialaudit.PtrUint(middleware.CurrentUserID(c)),
		ApproverUsername:    normalizeCredentialGrantIdentity(c.GetString(middleware.CtxUsername), 64),
		ExpiresAt:           now.Add(ttl).UTC(),
	}
	if err := h.db.WithContext(c.Request.Context()).Create(&grant).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	writeCredentialGrantAudit(c, h.db, grant, credentialaudit.OutcomeSuccess, "request", "requested")
	writeCredentialGrantAudit(c, h.db, grant, credentialaudit.OutcomeSuccess, "activate", "active")
	respondCreated(c, toCredentialGrantDTO(grant))
}

func EnforceTerminalCredentialGrantForWebSocket(c *gin.Context, db *gorm.DB, claims *auth.Claims, nodeID uint) (*model.CredentialAccessGrant, error) {
	grant, err := findActiveCredentialGrant(c.Request.Context(), db, claims, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, nodeID)
	if err != nil {
		if isCredentialGrantDenialError(err) {
			writeCredentialGrantBlockedAudit(c, db, claims, nodeID, credentialGrantStatusForError(err))
		}
		return nil, err
	}
	writeCredentialGrantAuditWithClaims(c, db, claims, *grant, credentialaudit.OutcomeSuccess, "use", "active")
	return grant, nil
}

func terminalCredentialGrantRequiredCloseReason(reason string) string {
	clean := strings.TrimSpace(util.SanitizeMessage(reason))
	if clean == "" {
		clean = "required"
	}
	return credentialGrantRequiredCode + ":" + clean
}

func terminalCredentialGrantCloseReasonForError(err error) string {
	return terminalCredentialGrantRequiredCloseReason(credentialGrantStatusForError(err))
}

func credentialGrantStatusForError(err error) string {
	switch {
	case errors.Is(err, ErrCredentialGrantRevoked):
		return CredentialGrantStatusRevoked
	case errors.Is(err, ErrCredentialGrantDenied):
		return CredentialGrantStatusDenied
	case errors.Is(err, ErrCredentialGrantExpired):
		return CredentialGrantStatusExpired
	case errors.Is(err, ErrCredentialGrantInvalid):
		return "invalid"
	default:
		return "required"
	}
}

func isCredentialGrantDenialError(err error) bool {
	return errors.Is(err, ErrCredentialGrantRequired) || errors.Is(err, ErrCredentialGrantInvalid) || errors.Is(err, ErrCredentialGrantExpired) || errors.Is(err, ErrCredentialGrantRevoked) || errors.Is(err, ErrCredentialGrantDenied)
}

func findActiveCredentialGrant(ctx context.Context, db *gorm.DB, claims *auth.Claims, action, purpose string, nodeID uint) (*model.CredentialAccessGrant, error) {
	if db == nil || claims == nil || claims.UserID == 0 || nodeID == 0 {
		return nil, ErrCredentialGrantRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var user model.User
	if err := db.WithContext(ctx).Select("id", "role").First(&user, claims.UserID).Error; err != nil {
		return nil, err
	}
	if user.Role != claims.Role || user.Role != "admin" {
		return nil, ErrCredentialGrantInvalid
	}
	now := time.Now().UTC()
	var grants []model.CredentialAccessGrant
	query := db.WithContext(ctx).
		Where("requester_user_id = ? AND requester_role = ? AND action = ? AND purpose = ? AND node_id = ?", claims.UserID, claims.Role, action, purpose, nodeID).
		Where("status IN ?", []string{CredentialGrantStatusActive, CredentialGrantStatusApproved}).
		Order("expires_at desc, id desc").
		Limit(8)
	if err := query.Find(&grants).Error; err != nil {
		return nil, err
	}
	for i := range grants {
		grant := grants[i]
		if !grant.ExpiresAt.After(now) {
			markCredentialGrantExpired(db, grant.ID, now)
			continue
		}
		return &grant, nil
	}
	status, err := latestInactiveCredentialGrantStatus(ctx, db, claims.UserID, claims.Role, action, purpose, nodeID)
	if err != nil {
		return nil, err
	}
	switch status {
	case CredentialGrantStatusRevoked:
		return nil, fmt.Errorf("%w: %w", ErrCredentialGrantInvalid, ErrCredentialGrantRevoked)
	case CredentialGrantStatusDenied:
		return nil, fmt.Errorf("%w: %w", ErrCredentialGrantInvalid, ErrCredentialGrantDenied)
	case CredentialGrantStatusExpired:
		return nil, fmt.Errorf("%w: %w", ErrCredentialGrantInvalid, ErrCredentialGrantExpired)
	}
	return nil, ErrCredentialGrantRequired
}

func latestInactiveCredentialGrantStatus(ctx context.Context, db *gorm.DB, userID uint, requesterRole, action, purpose string, nodeID uint) (string, error) {
	var grant model.CredentialAccessGrant
	err := db.WithContext(ctx).
		Select("id", "status").
		Where("requester_user_id = ? AND requester_role = ? AND action = ? AND purpose = ? AND node_id = ?", userID, requesterRole, action, purpose, nodeID).
		Where("status IN ?", []string{CredentialGrantStatusRevoked, CredentialGrantStatusDenied, CredentialGrantStatusExpired}).
		Order("updated_at desc, expires_at desc, id desc").
		Limit(1).
		Find(&grant).Error
	if err != nil {
		return "", err
	}
	return grant.Status, nil
}

func markCredentialGrantExpired(db *gorm.DB, grantID uint, now time.Time) {
	if db == nil || grantID == 0 {
		return
	}
	_ = db.Model(&model.CredentialAccessGrant{}).
		Where("id = ? AND status IN ?", grantID, []string{CredentialGrantStatusActive, CredentialGrantStatusApproved}).
		Updates(map[string]any{"status": CredentialGrantStatusExpired, "updated_at": now.UTC()}).Error
}

func sanitizeCredentialGrantReason(value string) (string, error) {
	clean := sanitizeCredentialGrantFreeText(value)
	if clean == "" {
		return "", fmt.Errorf("授权原因不能为空")
	}
	if credentialGrantTextHasSensitiveMarker(clean) {
		return "", fmt.Errorf("授权原因不能包含密码、密钥、令牌、命令输出或主机敏感信息")
	}
	if utf8.RuneCountInString(clean) > credentialGrantMaxReasonLen {
		return "", fmt.Errorf("授权原因不能超过 %d 个字符", credentialGrantMaxReasonLen)
	}
	return clean, nil
}

func normalizeCredentialGrantTTL(seconds int) (time.Duration, error) {
	if seconds <= 0 {
		return credentialGrantDefaultTTL, nil
	}
	ttl := time.Duration(seconds) * time.Second
	if ttl < credentialGrantMinTTL {
		return 0, fmt.Errorf("授权时长不能少于 %d 秒", int(credentialGrantMinTTL.Seconds()))
	}
	if ttl > credentialGrantMaxTTL {
		return 0, fmt.Errorf("授权时长不能超过 %d 秒", int(credentialGrantMaxTTL.Seconds()))
	}
	return ttl, nil
}

func toCredentialGrantDTO(grant model.CredentialAccessGrant) credentialGrantDTO {
	return credentialGrantDTO{
		ID:                  grant.ID,
		RequesterUserID:     grant.RequesterUserID,
		RequesterUsername:   grant.RequesterUsername,
		RequesterRole:       grant.RequesterRole,
		Action:              grant.Action,
		Purpose:             grant.Purpose,
		NodeID:              grant.NodeID,
		TaskID:              grant.TaskID,
		PolicyID:            grant.PolicyID,
		Reason:              grant.Reason,
		Status:              grant.Status,
		RequestedTTLSeconds: grant.RequestedTTLSeconds,
		RequestedAt:         grant.RequestedAt,
		ApprovedAt:          grant.ApprovedAt,
		ApproverUserID:      grant.ApproverUserID,
		ApproverUsername:    grant.ApproverUsername,
		ExpiresAt:           grant.ExpiresAt,
		RevokedAt:           grant.RevokedAt,
		RevokedByUserID:     grant.RevokedByUserID,
		CreatedAt:           grant.CreatedAt,
		UpdatedAt:           grant.UpdatedAt,
	}
}

func writeCredentialGrantAudit(c *gin.Context, db *gorm.DB, grant model.CredentialAccessGrant, outcome, stage, status string) {
	claims := &auth.Claims{UserID: grant.RequesterUserID, Username: grant.RequesterUsername, Role: grant.RequesterRole}
	writeCredentialGrantAuditWithClaims(c, db, claims, grant, outcome, stage, status)
}

func writeCredentialGrantAuditWithClaims(c *gin.Context, db *gorm.DB, claims *auth.Claims, grant model.CredentialAccessGrant, outcome, stage, status string) {
	event := credentialaudit.Event{
		Action:           grant.Action,
		Purpose:          grant.Purpose,
		CredentialKind:   credentialGrantKind,
		CredentialSource: credentialGrantSource,
		NodeID:           grant.NodeID,
		TaskID:           grant.TaskID,
		PolicyID:         grant.PolicyID,
		Outcome:          outcome,
		Metadata: map[string]any{
			"stage":       stage,
			"operation":   "terminal",
			"status":      status,
			"ttl_seconds": grant.RequestedTTLSeconds,
			"node_id":     derefUint(grant.NodeID),
			"grant_id":    grant.ID,
			"self_grant":  grant.ApproverUserID != nil && *grant.ApproverUserID == grant.RequesterUserID,
		},
	}
	if claims != nil {
		event.UserID = claims.UserID
		event.Username = claims.Username
		event.Role = claims.Role
	}
	writeCredentialAuditFromGin(c, db, event)
}

func writeCredentialGrantBlockedAudit(c *gin.Context, db *gorm.DB, claims *auth.Claims, nodeID uint, status string) {
	event := credentialaudit.Event{
		Action:           CredentialGrantActionTerminalOpen,
		Purpose:          sshutil.PurposeTerminal,
		CredentialKind:   credentialGrantKind,
		CredentialSource: credentialGrantSource,
		NodeID:           credentialaudit.PtrUint(nodeID),
		Outcome:          credentialaudit.OutcomeBlocked,
		Metadata: map[string]any{
			"stage":     "grant_check",
			"operation": "terminal",
			"status":    status,
			"node_id":   nodeID,
		},
	}
	if claims != nil {
		event.UserID = claims.UserID
		event.Username = claims.Username
		event.Role = claims.Role
	}
	writeCredentialAuditFromGin(c, db, event)
}

var credentialGrantSensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(password|passwd|token|secret|private[_-]?key|api[_-]?key|authorization|bearer)\s*(?:=|:|：)\s*\S+`),
	regexp.MustCompile(`(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\b(?:https?|wss?)://\S+`),
	regexp.MustCompile(`(?i)(output|stdout|stderr|stream|command|payload|content|host|endpoint|proxy|输出|命令|主机|端点|代理)\s*(?:=|:|：)`),
	regexp.MustCompile(`(?i)\b\d{1,3}(?:\.\d{1,3}){3}\b`),
}

func validateGrantRequesterContext(c *gin.Context, db *gorm.DB) error {
	userID := middleware.CurrentUserID(c)
	role := middleware.CurrentRole(c)
	if userID == 0 || strings.TrimSpace(role) == "" {
		return fmt.Errorf("missing grant requester context")
	}
	var user model.User
	if err := db.WithContext(c.Request.Context()).Select("id", "role").First(&user, userID).Error; err != nil {
		return err
	}
	if user.Role != role || role != "admin" {
		return fmt.Errorf("grant requester role changed")
	}
	return nil
}

func sanitizeCredentialGrantFreeText(value string) string {
	clean := strings.TrimSpace(util.SanitizeMessage(value))
	for _, re := range credentialGrantSensitivePatterns {
		clean = re.ReplaceAllString(clean, "[REDACTED]")
	}
	return strings.TrimSpace(clean)
}

func credentialGrantTextHasSensitiveMarker(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"password", "passwd", "token", "secret", "private", "authorization", "bearer", "output", "stdout", "stderr", "stream", "command", "payload", "content", "endpoint", "proxy", "host", "输出", "命令", "主机", "端点", "代理", "[redacted]"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func normalizeCredentialGrantIdentity(value string, max int) string {
	clean := sanitizeCredentialGrantFreeText(value)
	if credentialGrantTextHasSensitiveMarker(clean) {
		clean = ""
	}
	if max <= 0 || utf8.RuneCountInString(clean) <= max {
		return clean
	}
	runes := []rune(clean)
	return string(runes[:max])
}

func derefUint(value *uint) uint {
	if value == nil {
		return 0
	}
	return *value
}
