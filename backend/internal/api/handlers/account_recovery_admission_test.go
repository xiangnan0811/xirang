package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

func accountRecoveryAdmissionDB(t *testing.T, engine string) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := batchPersistenceDB(t, engine)
	if err := db.AutoMigrate(&model.User{}, &model.TokenRevocation{}, &model.CredentialAccessGrant{}, &model.CredentialAuditEvent{}, &model.AuditLog{}); err != nil {
		t.Fatal("migrate account recovery admission tables")
	}
	return db
}

func accountRecoveryAdmissionUser(t *testing.T, db *gorm.DB, name, role string) model.User {
	t.Helper()
	user := model.User{Username: name, Role: role, TokenVersion: 7, TOTPEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal("seed account recovery admission identity")
	}
	return user
}

func accountRecoveryAdmissionToken(t *testing.T, user model.User, purpose string) string {
	t.Helper()
	now := time.Now().UTC()
	claims := auth.Claims{
		UserID: user.ID, Username: user.Username, Role: user.Role, TokenVersion: user.TokenVersion, Purpose: purpose,
		RegisteredClaims: jwt.RegisteredClaims{
			ID: strings.Repeat("a", 32), Subject: fmt.Sprint(user.ID),
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
	// Bypass issuance guards deliberately: admission must check persisted state.
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(stepUpTestJWTSecret))
	if err != nil {
		t.Fatal("sign account recovery admission fixture")
	}
	return token
}

func accountRecoveryGrantInput() credentialGrantCreateInput {
	return credentialGrantCreateInput{
		Action: CredentialGrantActionConfigImport, Purpose: CredentialGrantPurposeConfigImport,
		Reason: "account recovery regression", RequestedTTLSeconds: 600,
	}
}

func accountRecoveryGrantMatch() credentialGrantMatch {
	return credentialGrantMatch{Action: CredentialGrantActionConfigImport, Purpose: CredentialGrantPurposeConfigImport}
}

func accountRecoveryGrantRouter(db *gorm.DB, manager *auth.JWTManager, gate gin.HandlerFunc) *gin.Engine {
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	if gate != nil {
		router.Use(gate)
	}
	handler := NewCredentialAccessGrantHandler(db, manager)
	router.POST("/grant", func(c *gin.Context) {
		if _, ok := handler.createActiveSelfGrants(c, []credentialGrantCreateInput{accountRecoveryGrantInput()}); ok {
			c.Status(http.StatusCreated)
		}
	})
	return router
}

func accountRecoveryGrantRequest(router http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/grant", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func TestAccountRecoveryRealtimeAndStepUpRejectRequiredCurrentIdentity(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := accountRecoveryAdmissionDB(t, engine)
			manager := newStepUpTestJWTManager(db)
			user := accountRecoveryAdmissionUser(t, db, "realtime-recovery-admin", "admin")
			primary := accountRecoveryAdmissionToken(t, user, "")
			proof := generateStepUpProofForAction(t, manager, user, auth.StepUpActionConfigImport)
			if _, err := authorizeRealtimeToken(context.Background(), primary, manager, db, realtimeAuthRequirements{Role: "admin"}); err != nil {
				t.Fatal("current nonzero-version primary denied before recovery")
			}
			if _, err := validateStepUpProof(db, manager, proof, user.ID, user.Role, auth.StepUpActionConfigImport); err != nil {
				t.Fatal("current nonzero-version proof denied before recovery")
			}
			for _, purpose := range []string{auth.PurposeRecoveryEnrollment, auth.Purpose2FAPending, auth.PurposeStepUp, " "} {
				if _, err := authorizeRealtimeToken(context.Background(), accountRecoveryAdmissionToken(t, user, purpose), manager, db, realtimeAuthRequirements{}); err == nil {
					t.Fatal("nonempty purpose admitted to realtime")
				}
			}
			for _, enabled := range []bool{false, true} {
				if err := db.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]any{
					"totp_reenrollment_required": true, "totp_enabled": enabled,
				}).Error; err != nil {
					t.Fatal("mark reenrollment required")
				}
				if _, err := authorizeRealtimeToken(context.Background(), primary, manager, db, realtimeAuthRequirements{}); err == nil {
					t.Fatal("required identity admitted to realtime with current version")
				}
				if _, err := validateStepUpProof(db, manager, proof, user.ID, user.Role, auth.StepUpActionConfigImport); !errors.Is(err, ErrStepUpProofInvalid) {
					t.Fatal("required identity admitted to step-up with current version")
				}
				response := accountRecoveryGrantRequest(accountRecoveryGrantRouter(db, manager, nil), primary)
				if response.Code != http.StatusUnauthorized {
					t.Fatalf("required current primary HTTP admission status=%d", response.Code)
				}
			}
		})
	}
}

func TestAccountRecoveryGrantUsesAuthoritativeSessionVersion(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := accountRecoveryAdmissionDB(t, engine)
			manager := newStepUpTestJWTManager(db)
			user := accountRecoveryAdmissionUser(t, db, "grant-version-admin", "admin")
			other := accountRecoveryAdmissionUser(t, db, "grant-other-admin", "admin")
			token := generatePrimaryToken(t, manager, user)
			router := accountRecoveryGrantRouter(db, manager, func(c *gin.Context) {
				claims := claimsFromGinContext(c)
				if claims == nil || claims.TokenVersion != user.TokenVersion || claims.UserID != user.ID || claims.Role != user.Role {
					t.Error("claims did not preserve authoritative middleware binding")
				}
				c.Next()
			})
			if response := accountRecoveryGrantRequest(router, token); response.Code != http.StatusCreated {
				t.Fatalf("current nonzero version grant creation status=%d", response.Code)
			}
			claims := &auth.Claims{UserID: user.ID, Role: user.Role, TokenVersion: user.TokenVersion}
			if _, err := findActiveCredentialGrant(context.Background(), db, claims, accountRecoveryGrantMatch()); err != nil {
				t.Fatal("current nonzero version grant use denied")
			}
			if _, err := findActiveCredentialGrant(context.Background(), db, &auth.Claims{UserID: other.ID, Role: other.Role, TokenVersion: other.TokenVersion}, accountRecoveryGrantMatch()); !errors.Is(err, ErrCredentialGrantRequired) {
				t.Fatal("another user inherited target grant")
			}
			for _, tc := range []struct {
				name     string
				version  uint
				role     string
				required bool
			}{
				{name: "stale version", version: user.TokenVersion + 1, role: user.Role},
				{name: "required current version", version: user.TokenVersion, role: user.Role, required: true},
				{name: "changed role", version: user.TokenVersion, role: "operator"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if err := db.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]any{
						"token_version": tc.version, "role": tc.role, "totp_reenrollment_required": tc.required,
					}).Error; err != nil {
						t.Fatal("change current grant identity")
					}
					if _, err := findActiveCredentialGrant(context.Background(), db, claims, accountRecoveryGrantMatch()); !errors.Is(err, ErrCredentialGrantInvalid) {
						t.Fatal("changed identity reused grant")
					}
				})
			}
			if err := db.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]any{
				"token_version": user.TokenVersion, "role": user.Role, "totp_reenrollment_required": false,
			}).Error; err != nil {
				t.Fatal("restore independent admission fixture")
			}
			claims.Purpose = auth.PurposeRecoveryEnrollment
			if _, err := findActiveCredentialGrant(context.Background(), db, claims, accountRecoveryGrantMatch()); err == nil {
				t.Fatal("purpose-bound claims reused grant")
			}

			for _, tc := range []struct {
				name   string
				mutate func(*gin.Context)
			}{
				{name: "missing binding", mutate: func(c *gin.Context) { delete(c.Keys, middleware.CtxSessionBinding) }},
				{name: "wrong binding type", mutate: func(c *gin.Context) { c.Set(middleware.CtxSessionBinding, "not a binding") }},
				{name: "wrong user", mutate: func(c *gin.Context) { c.Set(middleware.CtxUserID, other.ID) }},
				{name: "wrong role", mutate: func(c *gin.Context) { c.Set(middleware.CtxRole, "operator") }},
			} {
				t.Run(tc.name, func(t *testing.T) {
					badRouter := accountRecoveryGrantRouter(db, manager, func(c *gin.Context) {
						tc.mutate(c)
						if claimsFromGinContext(c) != nil {
							t.Error("incomplete or mismatched binding produced claims")
						}
						c.Next()
					})
					if response := accountRecoveryGrantRequest(badRouter, token); response.Code != http.StatusForbidden {
						t.Fatalf("missing authentic binding grant creation status=%d", response.Code)
					}
				})
			}
			var count int64
			if err := db.Model(&model.CredentialAccessGrant{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatal("failed admission created grant history")
			}
		})
	}
}

func TestAccountRecoveryGrantCreationRechecksAdmittedSnapshot(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		for _, change := range []string{"version", "required", "role", "deleted"} {
			t.Run(engine+"/"+change, func(t *testing.T) {
				db := accountRecoveryAdmissionDB(t, engine)
				manager := newStepUpTestJWTManager(db)
				user := accountRecoveryAdmissionUser(t, db, "grant-stale-admin", "admin")
				token := generatePrimaryToken(t, manager, user)
				admitted := make(chan struct{})
				release := make(chan struct{})
				router := accountRecoveryGrantRouter(db, manager, func(c *gin.Context) {
					close(admitted)
					<-release
					c.Next()
				})
				result := make(chan *httptest.ResponseRecorder, 1)
				go func() { result <- accountRecoveryGrantRequest(router, token) }()
				select {
				case <-admitted:
				case <-time.After(10 * time.Second):
					close(release)
					t.Fatal("request did not reach post-admission gate")
				}
				// The genuine middleware binding is now fixed. Commit an identity
				// change before allowing the producer to enter its transaction.
				err := db.Transaction(func(tx *gorm.DB) error {
					if err := auth.SerializeIdentityMutationLock(tx); err != nil {
						return err
					}
					query := tx.Model(&model.User{}).Where("id = ?", user.ID)
					switch change {
					case "version":
						return query.Update("token_version", user.TokenVersion+1).Error
					case "required":
						return query.Update("totp_reenrollment_required", true).Error
					case "role":
						return query.Update("role", "operator").Error
					default:
						return tx.Delete(&model.User{}, user.ID).Error
					}
				})
				close(release)
				if err != nil {
					t.Fatal("commit concurrent identity mutation")
				}
				select {
				case response := <-result:
					if response.Code != http.StatusForbidden {
						t.Fatalf("stale admitted grant creation status=%d", response.Code)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("grant creation did not finish")
				}
				var count int64
				if err := db.Model(&model.CredentialAccessGrant{}).Count(&count).Error; err != nil || count != 0 {
					t.Fatal("stale snapshot created grant")
				}
			})
		}
	}
}

func TestAccountRecoveryGrantCreationSerializesBeforeRecoveryRevocation(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := accountRecoveryAdmissionDB(t, engine)
			manager := newStepUpTestJWTManager(db)
			user := accountRecoveryAdmissionUser(t, db, "grant-serialize-admin", "admin")
			token := generatePrimaryToken(t, manager, user)
			created := make(chan struct{})
			release := make(chan struct{})
			const callback = "account_recovery_pause_grant_creation"
			if err := db.Callback().Create().After("gorm:create").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "credential_access_grants" {
					close(created)
					<-release
				}
			}); err != nil {
				t.Fatal("register grant creation transaction gate")
			}
			t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- accountRecoveryGrantRequest(accountRecoveryGrantRouter(db, manager, nil), token) }()
			select {
			case <-created:
			case <-time.After(10 * time.Second):
				close(release)
				t.Fatal("grant did not reach transaction gate")
			}
			started := make(chan struct{})
			resetResult := make(chan error, 1)
			go func() {
				close(started)
				resetResult <- db.Transaction(func(tx *gorm.DB) error {
					if err := auth.SerializeIdentityMutationLock(tx); err != nil {
						return err
					}
					if err := tx.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]any{
						"token_version": user.TokenVersion + 1, "totp_reenrollment_required": true,
					}).Error; err != nil {
						return err
					}
					now := time.Now().UTC()
					return tx.Model(&model.CredentialAccessGrant{}).Where("requester_user_id = ? AND status = ?", user.ID, CredentialGrantStatusActive).
						Updates(map[string]any{"status": CredentialGrantStatusRevoked, "revoked_at": now}).Error
				})
			}()
			<-started
			close(release)
			select {
			case response := <-result:
				if response.Code != http.StatusCreated {
					t.Fatalf("pre-recovery grant creation status=%d", response.Code)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("serialized grant creation did not finish")
			}
			select {
			case err := <-resetResult:
				if err != nil {
					t.Fatal("serialized recovery mutation failed")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("serialized recovery mutation did not finish")
			}
			var grants []model.CredentialAccessGrant
			if err := db.Find(&grants).Error; err != nil || len(grants) != 1 || grants[0].Status != CredentialGrantStatusRevoked || grants[0].RevokedAt == nil {
				t.Fatal("recovery did not preserve and revoke the preceding grant")
			}
		})
	}
}

func TestAccountRecoveryRevokedGrantHistoryCannotReactivate(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := accountRecoveryAdmissionDB(t, engine)
			manager := newStepUpTestJWTManager(db)
			user := accountRecoveryAdmissionUser(t, db, "grant-revoked-admin", "admin")
			other := accountRecoveryAdmissionUser(t, db, "grant-independent-admin", "admin")
			router := accountRecoveryGrantRouter(db, manager, nil)
			for _, identity := range []model.User{user, other} {
				if response := accountRecoveryGrantRequest(router, generatePrimaryToken(t, manager, identity)); response.Code != http.StatusCreated {
					t.Fatalf("independent grant creation status=%d", response.Code)
				}
			}
			var stale model.CredentialAccessGrant
			if err := db.Where("requester_user_id = ?", user.ID).First(&stale).Error; err != nil {
				t.Fatal("load stale active grant snapshot")
			}
			now := time.Now().UTC()
			if err := db.Model(&model.CredentialAccessGrant{}).Where("id = ?", stale.ID).Updates(map[string]any{
				"status": CredentialGrantStatusRevoked, "revoked_at": now,
			}).Error; err != nil {
				t.Fatal("revoke grant history")
			}
			// Expiry is the only online state transition of an existing grant.
			// It must use a conditional update, never Save the stale active row.
			markCredentialGrantExpired(db, stale.ID, now.Add(time.Hour))
			claims := &auth.Claims{UserID: user.ID, Role: user.Role, TokenVersion: user.TokenVersion}
			if _, err := findActiveCredentialGrant(context.Background(), db, claims, accountRecoveryGrantMatch()); !errors.Is(err, ErrCredentialGrantRevoked) {
				t.Fatal("revoked grant became usable or lost revoked status")
			}
			if _, err := findActiveCredentialGrant(context.Background(), db, &auth.Claims{UserID: other.ID, Role: other.Role, TokenVersion: other.TokenVersion}, accountRecoveryGrantMatch()); err != nil {
				t.Fatal("revoking target grant changed other-user semantics")
			}
			if response := accountRecoveryGrantRequest(router, generatePrimaryToken(t, manager, user)); response.Code != http.StatusCreated {
				t.Fatal("revoked history incorrectly blocked independent new grant")
			}
			var history model.CredentialAccessGrant
			if err := db.First(&history, stale.ID).Error; err != nil || history.Status != CredentialGrantStatusRevoked || history.RevokedAt == nil {
				t.Fatal("new grant rewrote revoked history")
			}
			if _, err := findActiveCredentialGrant(context.Background(), db, claims, accountRecoveryGrantMatch()); err != nil {
				t.Fatal("fresh grant denied because of revoked history")
			}
		})
	}
}
