package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	StepUpHeaderName       = "X-Xirang-Step-Up"
	stepUpRequiredCode     = "STEP_UP_REQUIRED"
	stepUpProofTTLSeconds  = 300
	stepUpCredentialKind   = "step_up"
	stepUpCredentialSource = "totp"
)

type stepUpRequiredData struct {
	ErrorCode       string `json:"error_code"`
	ProofTTLSeconds int    `json:"proof_ttl_seconds"`
}

type stepUpAuditOperation struct {
	Action    string
	Purpose   string
	Operation string
}

func RequireStepUp(db *gorm.DB, jwtManager *auth.JWTManager, action, purpose, operation string) gin.HandlerFunc {
	return RequireStepUpIf(db, jwtManager, action, purpose, operation, nil)
}

func RequireStepUpIf(db *gorm.DB, jwtManager *auth.JWTManager, action, purpose, operation string, predicate func(*gin.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if predicate != nil && !predicate(c) {
			c.Next()
			return
		}
		if !enforceStepUpForContext(c, db, jwtManager, stepUpAuditOperation{Action: action, Purpose: purpose, Operation: operation}) {
			c.Abort()
			return
		}
		c.Next()
	}
}

func EnforceStepUp(c *gin.Context, db *gorm.DB, jwtManager *auth.JWTManager, action, purpose, operation string) bool {
	return enforceStepUpForContext(c, db, jwtManager, stepUpAuditOperation{Action: action, Purpose: purpose, Operation: operation})
}

func enforceStepUpForContext(c *gin.Context, db *gorm.DB, jwtManager *auth.JWTManager, op stepUpAuditOperation) bool {
	userID := middleware.CurrentUserID(c)
	role := middleware.CurrentRole(c)
	proof := strings.TrimSpace(c.GetHeader(StepUpHeaderName))
	if proof == "" {
		writeStepUpCredentialAudit(c, db, nil, op, credentialaudit.OutcomeBlocked, "required")
		respondStepUpRequired(c)
		return false
	}
	claims, err := validateStepUpProof(db, jwtManager, proof, userID, role)
	if err != nil {
		writeStepUpCredentialAudit(c, db, nil, op, credentialaudit.OutcomeBlocked, "failed")
		respondStepUpRequired(c)
		return false
	}
	writeStepUpCredentialAudit(c, db, claims, op, credentialaudit.OutcomeSuccess, "satisfied")
	return true
}

func validateStepUpProof(db *gorm.DB, jwtManager *auth.JWTManager, proof string, userID uint, role string) (*auth.Claims, error) {
	if db == nil || jwtManager == nil {
		return nil, fmt.Errorf("step-up 验证服务不可用")
	}
	proof = strings.TrimSpace(proof)
	if proof == "" {
		return nil, fmt.Errorf("缺少 step-up proof")
	}
	claims, err := jwtManager.ParseToken(proof)
	if err != nil {
		return nil, fmt.Errorf("step-up proof 无效")
	}
	if claims.Purpose != auth.PurposeStepUp {
		return nil, fmt.Errorf("step-up proof 用途不匹配")
	}
	if claims.UserID == 0 || claims.UserID != userID {
		return nil, fmt.Errorf("step-up proof 用户不匹配")
	}
	if strings.TrimSpace(role) != "" && claims.Role != role {
		return nil, fmt.Errorf("step-up proof 角色不匹配")
	}
	var user model.User
	if err := db.Select("id", "role", "token_version", "totp_enabled").First(&user, claims.UserID).Error; err != nil {
		return nil, fmt.Errorf("用户不存在或已删除")
	}
	if user.TokenVersion != claims.TokenVersion {
		return nil, fmt.Errorf("step-up proof 已失效")
	}
	if strings.TrimSpace(user.Role) != "" && user.Role != claims.Role {
		return nil, fmt.Errorf("step-up proof 角色已失效")
	}
	if !user.TOTPEnabled {
		return nil, fmt.Errorf("step-up proof 已失效")
	}
	return claims, nil
}

func respondStepUpRequired(c *gin.Context) {
	c.JSON(http.StatusForbidden, Response{
		Code:    http.StatusForbidden,
		Message: "需要二次验证",
		Data: stepUpRequiredData{
			ErrorCode:       stepUpRequiredCode,
			ProofTTLSeconds: stepUpProofTTLSeconds,
		},
	})
}

func writeStepUpCredentialAudit(c *gin.Context, db *gorm.DB, claims *auth.Claims, op stepUpAuditOperation, outcome, proofState string) {
	if strings.TrimSpace(op.Action) == "" {
		op.Action = "auth.step_up"
	}
	if strings.TrimSpace(op.Purpose) == "" {
		op.Purpose = auth.PurposeStepUp
	}
	if strings.TrimSpace(op.Operation) == "" {
		op.Operation = "step_up"
	}
	event := credentialaudit.Event{
		Action:           op.Action,
		Purpose:          op.Purpose,
		CredentialKind:   stepUpCredentialKind,
		CredentialSource: stepUpCredentialSource,
		Outcome:          outcome,
		Metadata: map[string]any{
			"stage":       "step_up",
			"proof":       proofState,
			"operation":   op.Operation,
			"ttl_seconds": stepUpProofTTLSeconds,
		},
	}
	if claims != nil {
		event.UserID = claims.UserID
		event.Username = claims.Username
		event.Role = claims.Role
	}
	writeCredentialAuditFromGin(c, db, event)
}
