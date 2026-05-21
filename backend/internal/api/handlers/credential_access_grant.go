package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

	CredentialGrantActionTerminalOpen  = "terminal.open"
	CredentialGrantActionConfigImport  = "config.import"
	CredentialGrantPurposeConfigImport = "config_import"

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

type configImportCredentialGrantRequest struct {
	Reason              string `json:"reason" binding:"required"`
	RequestedTTLSeconds int    `json:"requested_ttl_seconds"`
}

type credentialGrantRequiredData struct {
	ErrorCode string `json:"error_code"`
	Status    string `json:"status"`
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
	reason, ttl, ok := h.validateGrantRequest(c, req.Reason, req.RequestedTTLSeconds, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, "terminal_grant")
	if !ok {
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

	grant, ok := h.createActiveSelfGrant(c, credentialGrantCreateInput{
		Action:              CredentialGrantActionTerminalOpen,
		Purpose:             sshutil.PurposeTerminal,
		NodeID:              credentialaudit.PtrUint(req.NodeID),
		Reason:              reason,
		RequestedTTLSeconds: int(ttl.Seconds()),
	})
	if !ok {
		return
	}
	respondCreated(c, toCredentialGrantDTO(grant))
}

func (h *CredentialAccessGrantHandler) RequestConfigImportGrant(c *gin.Context) {
	var req configImportCredentialGrantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	reason, ttl, ok := h.validateGrantRequest(c, req.Reason, req.RequestedTTLSeconds, CredentialGrantActionConfigImport, CredentialGrantPurposeConfigImport, "settings_import")
	if !ok {
		return
	}
	grant, ok := h.createActiveSelfGrant(c, credentialGrantCreateInput{
		Action:              CredentialGrantActionConfigImport,
		Purpose:             CredentialGrantPurposeConfigImport,
		Reason:              reason,
		RequestedTTLSeconds: int(ttl.Seconds()),
	})
	if !ok {
		return
	}
	respondCreated(c, toCredentialGrantDTO(grant))
}

type credentialGrantCreateInput struct {
	Action              string
	Purpose             string
	NodeID              *uint
	TaskID              *uint
	PolicyID            *uint
	Reason              string
	RequestedTTLSeconds int
}

func (h *CredentialAccessGrantHandler) validateGrantRequest(c *gin.Context, reasonValue string, ttlSeconds int, action, purpose, operation string) (string, time.Duration, bool) {
	if h.db == nil {
		respondInternalError(c, fmt.Errorf("credential grant db unavailable"))
		return "", 0, false
	}
	if !enforceStepUpForContext(c, h.db, h.jwtManager, stepUpAuditOperation{Action: action, Purpose: purpose, Operation: operation}) {
		return "", 0, false
	}
	if err := validateGrantRequesterContext(c, h.db); err != nil {
		respondForbidden(c, "认证状态已变化，请重新登录")
		return "", 0, false
	}
	reason, err := sanitizeCredentialGrantReason(reasonValue)
	if err != nil {
		respondBadRequest(c, err.Error())
		return "", 0, false
	}
	ttl, err := normalizeCredentialGrantTTL(ttlSeconds)
	if err != nil {
		respondBadRequest(c, err.Error())
		return "", 0, false
	}
	return reason, ttl, true
}

func (h *CredentialAccessGrantHandler) createActiveSelfGrant(c *gin.Context, input credentialGrantCreateInput) (model.CredentialAccessGrant, bool) {
	now := time.Now().UTC()
	grant := model.CredentialAccessGrant{
		RequesterUserID:     middleware.CurrentUserID(c),
		RequesterUsername:   normalizeCredentialGrantIdentity(c.GetString(middleware.CtxUsername), 64),
		RequesterRole:       normalizeCredentialGrantIdentity(middleware.CurrentRole(c), 32),
		Action:              input.Action,
		Purpose:             input.Purpose,
		NodeID:              input.NodeID,
		TaskID:              input.TaskID,
		PolicyID:            input.PolicyID,
		Reason:              input.Reason,
		Status:              CredentialGrantStatusActive,
		RequestedTTLSeconds: input.RequestedTTLSeconds,
		RequestedAt:         now,
		ApprovedAt:          &now,
		ApproverUserID:      credentialaudit.PtrUint(middleware.CurrentUserID(c)),
		ApproverUsername:    normalizeCredentialGrantIdentity(c.GetString(middleware.CtxUsername), 64),
		ExpiresAt:           now.Add(time.Duration(input.RequestedTTLSeconds) * time.Second).UTC(),
	}
	if err := h.db.WithContext(c.Request.Context()).Create(&grant).Error; err != nil {
		respondInternalError(c, err)
		return model.CredentialAccessGrant{}, false
	}
	writeCredentialGrantAudit(c, h.db, grant, credentialaudit.OutcomeSuccess, "request", "requested")
	writeCredentialGrantAudit(c, h.db, grant, credentialaudit.OutcomeSuccess, "activate", "active")
	return grant, true
}

func EnforceTerminalCredentialGrantForWebSocket(c *gin.Context, db *gorm.DB, claims *auth.Claims, nodeID uint) (*model.CredentialAccessGrant, error) {
	if nodeID == 0 {
		writeCredentialGrantBlockedAudit(c, db, claims, credentialGrantMatch{Action: CredentialGrantActionTerminalOpen, Purpose: sshutil.PurposeTerminal}, credentialGrantStatusForError(ErrCredentialGrantRequired))
		return nil, ErrCredentialGrantRequired
	}
	grant, err := findActiveCredentialGrant(c.Request.Context(), db, claims, credentialGrantMatch{Action: CredentialGrantActionTerminalOpen, Purpose: sshutil.PurposeTerminal, NodeID: credentialaudit.PtrUint(nodeID)})
	if err != nil {
		if isCredentialGrantDenialError(err) {
			writeCredentialGrantBlockedAudit(c, db, claims, credentialGrantMatch{Action: CredentialGrantActionTerminalOpen, Purpose: sshutil.PurposeTerminal, NodeID: credentialaudit.PtrUint(nodeID)}, credentialGrantStatusForError(err))
		}
		return nil, err
	}
	writeCredentialGrantAuditWithClaims(c, db, claims, *grant, credentialaudit.OutcomeSuccess, "use", "active")
	return grant, nil
}

func RequireConfigImportCredentialGrant(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims := claimsFromGinContext(c)
		match := credentialGrantMatch{Action: CredentialGrantActionConfigImport, Purpose: CredentialGrantPurposeConfigImport}
		grant, err := findActiveCredentialGrant(c.Request.Context(), db, claims, match)
		if err != nil {
			if isCredentialGrantDenialError(err) {
				status := credentialGrantStatusForError(err)
				writeCredentialGrantBlockedAudit(c, db, claims, match, status)
				respondCredentialGrantRequired(c, status)
				c.Abort()
				return
			}
			respondInternalError(c, err)
			c.Abort()
			return
		}
		writeCredentialGrantAuditWithClaims(c, db, claims, *grant, credentialaudit.OutcomeSuccess, "use", "active")
		c.Next()
	}
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

type credentialGrantMatch struct {
	Action   string
	Purpose  string
	NodeID   *uint
	TaskID   *uint
	PolicyID *uint
}

func findActiveCredentialGrant(ctx context.Context, db *gorm.DB, claims *auth.Claims, match credentialGrantMatch) (*model.CredentialAccessGrant, error) {
	if db == nil || claims == nil || claims.UserID == 0 || strings.TrimSpace(match.Action) == "" || strings.TrimSpace(match.Purpose) == "" {
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
	query := applyCredentialGrantMatch(db.WithContext(ctx).
		Where("requester_user_id = ? AND requester_role = ? AND action = ? AND purpose = ?", claims.UserID, claims.Role, match.Action, match.Purpose).
		Where("status IN ?", []string{CredentialGrantStatusActive, CredentialGrantStatusApproved}), match).
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
	status, err := latestInactiveCredentialGrantStatus(ctx, db, claims.UserID, claims.Role, match)
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

func latestInactiveCredentialGrantStatus(ctx context.Context, db *gorm.DB, userID uint, requesterRole string, match credentialGrantMatch) (string, error) {
	var grant model.CredentialAccessGrant
	err := applyCredentialGrantMatch(db.WithContext(ctx).
		Select("id", "status").
		Where("requester_user_id = ? AND requester_role = ? AND action = ? AND purpose = ?", userID, requesterRole, match.Action, match.Purpose).
		Where("status IN ?", []string{CredentialGrantStatusRevoked, CredentialGrantStatusDenied, CredentialGrantStatusExpired}), match).
		Order("updated_at desc, expires_at desc, id desc").
		Limit(1).
		Find(&grant).Error
	if err != nil {
		return "", err
	}
	return grant.Status, nil
}

func applyCredentialGrantMatch(query *gorm.DB, match credentialGrantMatch) *gorm.DB {
	if match.NodeID != nil {
		query = query.Where("node_id = ?", *match.NodeID)
	} else {
		query = query.Where("node_id IS NULL")
	}
	if match.TaskID != nil {
		query = query.Where("task_id = ?", *match.TaskID)
	} else {
		query = query.Where("task_id IS NULL")
	}
	if match.PolicyID != nil {
		query = query.Where("policy_id = ?", *match.PolicyID)
	} else {
		query = query.Where("policy_id IS NULL")
	}
	return query
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
	metadata := map[string]any{
		"stage":       stage,
		"operation":   credentialGrantOperationLabel(grant.Action, grant.Purpose),
		"status":      status,
		"ttl_seconds": grant.RequestedTTLSeconds,
		"grant_id":    grant.ID,
		"self_grant":  grant.ApproverUserID != nil && *grant.ApproverUserID == grant.RequesterUserID,
	}
	if grant.NodeID != nil {
		metadata["node_id"] = derefUint(grant.NodeID)
	}
	event := credentialaudit.Event{
		Action:           grant.Action,
		Purpose:          grant.Purpose,
		CredentialKind:   credentialGrantKind,
		CredentialSource: credentialGrantSource,
		NodeID:           grant.NodeID,
		TaskID:           grant.TaskID,
		PolicyID:         grant.PolicyID,
		Outcome:          outcome,
		Metadata:         metadata,
	}
	if claims != nil {
		event.UserID = claims.UserID
		event.Username = claims.Username
		event.Role = claims.Role
	}
	writeCredentialAuditFromGin(c, db, event)
}

func writeCredentialGrantBlockedAudit(c *gin.Context, db *gorm.DB, claims *auth.Claims, match credentialGrantMatch, status string) {
	metadata := map[string]any{
		"stage":     "grant_check",
		"operation": credentialGrantOperationLabel(match.Action, match.Purpose),
		"status":    status,
	}
	if match.NodeID != nil {
		metadata["node_id"] = *match.NodeID
	}
	event := credentialaudit.Event{
		Action:           match.Action,
		Purpose:          match.Purpose,
		CredentialKind:   credentialGrantKind,
		CredentialSource: credentialGrantSource,
		NodeID:           match.NodeID,
		TaskID:           match.TaskID,
		PolicyID:         match.PolicyID,
		Outcome:          credentialaudit.OutcomeBlocked,
		Metadata:         metadata,
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

func respondCredentialGrantRequired(c *gin.Context, status string) {
	if strings.TrimSpace(status) == "" {
		status = "required"
	}
	c.JSON(http.StatusForbidden, Response{
		Code:    http.StatusForbidden,
		Message: "需要临时授权",
		Data: credentialGrantRequiredData{
			ErrorCode: credentialGrantRequiredCode,
			Status:    status,
		},
	})
}

func claimsFromGinContext(c *gin.Context) *auth.Claims {
	if c == nil {
		return nil
	}
	return &auth.Claims{
		UserID:   middleware.CurrentUserID(c),
		Username: c.GetString(middleware.CtxUsername),
		Role:     middleware.CurrentRole(c),
	}
}

func credentialGrantOperationLabel(action, purpose string) string {
	switch {
	case action == CredentialGrantActionTerminalOpen && purpose == sshutil.PurposeTerminal:
		return "terminal"
	case action == CredentialGrantActionConfigImport && purpose == CredentialGrantPurposeConfigImport:
		return "settings_import"
	default:
		return "grant"
	}
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
