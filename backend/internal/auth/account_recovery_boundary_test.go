package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"xirang/backend/internal/model"
)

func TestAccountRecoveryTokenGenerationBoundaries(t *testing.T) {
	manager := NewJWTManager("FAKE_RECOVERY_BOUNDARY_SIGNING_KEY", time.Hour)
	user := model.User{ID: 1, Username: "recovery", Role: "viewer", TOTPReenrollmentRequired: true}
	expires := time.Now().UTC().Truncate(time.Second).Add(TOTPEnrollmentTTL)
	enrollmentID := strings.Repeat("a", 32)
	first, err := manager.GenerateRecoveryEnrollmentToken(user, enrollmentID, expires)
	if err != nil {
		t.Fatal("generate first recovery token")
	}
	second, err := manager.GenerateRecoveryEnrollmentToken(user, enrollmentID, expires)
	if err != nil || first == second {
		t.Fatal("recovery tokens did not have independent JTIs")
	}
	normalized, err := manager.GenerateRecoveryEnrollmentToken(user, enrollmentID, expires.Add(time.Nanosecond))
	if err != nil {
		t.Fatal("normalize subsecond enrollment expiry")
	}
	claims, err := manager.ParseToken(normalized)
	if err != nil || claims.ExpiresAt == nil || !claims.ExpiresAt.Equal(expires) {
		t.Fatal("signed enrollment expiry was not normalized to the persisted second")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*model.User, *string, *time.Time)
	}{
		{"ordinary-user", func(u *model.User, _ *string, _ *time.Time) { u.TOTPReenrollmentRequired = false }},
		{"contradictory-flags", func(u *model.User, _ *string, _ *time.Time) { u.TOTPEnabled = true }},
		{"zero-user", func(u *model.User, _ *string, _ *time.Time) { u.ID = 0 }},
		{"unknown-role", func(u *model.User, _ *string, _ *time.Time) { u.Role = "unknown" }},
		{"bad-enrollment", func(_ *model.User, id *string, _ *time.Time) { *id = "invalid" }},
		{"expired", func(_ *model.User, _ *string, e *time.Time) { *e = time.Now().Add(-time.Hour).Truncate(time.Second) }},
		{"future-lifetime", func(_ *model.User, _ *string, e *time.Time) { *e = expires.Add(time.Hour) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, id, e := user, enrollmentID, expires
			tc.mutate(&u, &id, &e)
			if token, err := manager.GenerateRecoveryEnrollmentToken(u, id, e); token != "" || !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
				t.Fatal("invalid recovery token generation input accepted")
			}
		})
	}
	manager.signTokenFunc = func(*jwt.Token) (string, error) { return "", nil }
	if token, err := manager.GenerateRecoveryEnrollmentToken(user, enrollmentID, expires); token != "" || !errors.Is(err, ErrRecoveryEnrollmentInternal) {
		t.Fatal("empty signature accepted")
	}
}

func TestAccountRecoveryExpiryRecheckedInsideTransaction(t *testing.T) {
	accountRecoveryEngines(t, func(t *testing.T, db *gorm.DB) {
		f := newAccountRecoveryFixture(t, db)
		enrollment := f.login(t)
		before := accountRecoveryRow(t, db, f.user.ID)
		var expired atomic.Bool
		f.service.nowFunc = func() time.Time {
			if expired.Load() {
				return enrollment.Setup.ExpiresAt
			}
			return time.Now()
		}
		name := "test:account-recovery-expire-before-locked-read"
		if err := db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
			if _, ok := tx.Statement.Dest.(*storedIdentityUser); ok {
				expired.Store(true)
			}
		}); err != nil {
			t.Fatal("install expiry boundary callback")
		}
		t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
		codes, err := f.service.CompleteRecoveryEnrollment(context.Background(), enrollment.Token, testTOTPCode(t, enrollment.Setup.Secret))
		if codes != nil || !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
			t.Fatal("enrollment expired while acquiring row was accepted")
		}
		accountRecoveryAssertUnchanged(t, before, accountRecoveryRow(t, db, f.user.ID))
	})
}

func TestAccountRecoveryWrongCodeCanRetryExplicitly(t *testing.T) {
	accountRecoveryEngines(t, func(t *testing.T, db *gorm.DB) {
		f := newAccountRecoveryFixture(t, db)
		enrollment := f.login(t)
		before := accountRecoveryRow(t, db, f.user.ID)
		// At most three distinct six-digit values match the skew window.
		// Choose a demonstrably wrong real six-digit OTP, not a format error.
		wrong := ""
		for i := range 4 {
			candidate := fmt.Sprintf("%06d", i)
			if !ValidateTOTP(enrollment.Setup.Secret, candidate) {
				wrong = candidate
				break
			}
		}
		if wrong == "" {
			t.Fatal("could not choose invalid OTP")
		}
		if codes, err := f.service.CompleteRecoveryEnrollment(context.Background(), enrollment.Token, wrong); codes != nil || !errors.Is(err, ErrTOTPCodeInvalid) {
			t.Fatal("wrong OTP was not a safe retryable rejection")
		}
		accountRecoveryAssertUnchanged(t, before, accountRecoveryRow(t, db, f.user.ID))
		if codes, err := f.service.CompleteRecoveryEnrollment(context.Background(), enrollment.Token, testTOTPCode(t, enrollment.Setup.Secret)); err != nil || len(codes) != 8 {
			t.Fatal("explicit retry after wrong OTP did not succeed")
		}
	})
}

func TestAccountRecoveryCorruptCiphertextFailsWithoutDisclosure(t *testing.T) {
	accountRecoveryEngines(t, func(t *testing.T, db *gorm.DB) {
		f := newAccountRecoveryFixture(t, db)
		enrollment := f.login(t)
		const corrupted = "enc:v2:FAKE_CORRUPTED_RECOVERY_CIPHERTEXT"
		if err := db.Table("users").Where("id = ?", f.user.ID).Update("totp_secret", corrupted).Error; err != nil {
			t.Fatal("store corrupt fixture ciphertext")
		}
		capture := &recoveryTraceLogger{}
		f.service.db = db.Session(&gorm.Session{Logger: capture})
		if codes, err := f.service.CompleteRecoveryEnrollment(context.Background(), enrollment.Token, "123456"); codes != nil || err != ErrRecoveryEnrollmentInternal {
			t.Fatal("ciphertext failure leaked completion error")
		}
		if result, err := f.service.Login(f.user.Username, "Recovery1!", "127.0.0.1"); result != nil || err != ErrRecoveryEnrollmentInternal {
			t.Fatal("ciphertext failure leaked login error")
		}
		for _, entry := range capture.snapshot() {
			for _, secret := range []string{corrupted, enrollment.Token, enrollment.Setup.Secret, f.user.PasswordHash} {
				if strings.Contains(entry.sql, secret) || strings.Contains(entry.err, secret) {
					t.Fatal("ciphertext failure leaked through SQL logging")
				}
			}
		}
	})
}

func TestAccountRecoveryCompletionNeverSignsSession(t *testing.T) {
	accountRecoveryEngines(t, func(t *testing.T, db *gorm.DB) {
		f := newAccountRecoveryFixture(t, db)
		enrollment := f.login(t)
		var signatures atomic.Int64
		f.manager.signTokenFunc = func(*jwt.Token) (string, error) {
			signatures.Add(1)
			return "", errors.New("FAKE_SESSION_SIGNING_FORBIDDEN")
		}
		codes, err := f.service.CompleteRecoveryEnrollment(context.Background(), enrollment.Token, testTOTPCode(t, enrollment.Setup.Secret))
		if err != nil || len(codes) != 8 || signatures.Load() != 0 {
			t.Fatal("recovery completion attempted session signing")
		}
	})
}
