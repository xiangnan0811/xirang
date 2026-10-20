package auth

import (
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestStepUpActionRegistrySupportsProofIssuance(t *testing.T) {
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	user := model.User{ID: 9, Username: "alice", Role: "admin", TokenVersion: 3}
	actions := AllStepUpActions()
	if len(actions) == 0 {
		t.Fatal("step-up action registry must not be empty")
	}
	seen := make(map[StepUpAction]bool, len(actions))
	for _, action := range actions {
		if seen[action] || !IsValidStepUpAction(action) {
			t.Fatalf("invalid or duplicate registered step-up action %q", action)
		}
		seen[action] = true
		parsed, err := ParseStepUpAction(string(action))
		if err != nil || parsed != action {
			t.Fatalf("parse registered action %q: action=%q err=%v", action, parsed, err)
		}
		proof, _, err := manager.GenerateStepUpToken(user, action, testStepUpSessionID)
		if err != nil {
			t.Fatalf("issue registered action %q: %v", action, err)
		}
		claims, err := manager.ParseToken(proof)
		if err != nil || claims.StepUpAction != action || claims.Purpose != PurposeStepUp {
			t.Fatalf("registered action %q proof: claims=%+v err=%v", action, claims, err)
		}
	}
	actions[0] = "future.action"
	if AllStepUpActions()[0] == actions[0] {
		t.Fatal("mutating the returned registry must not mutate registered actions")
	}
	for _, invalid := range []StepUpAction{"", "step_up", "terminal", "task_command", "future.action"} {
		if _, err := ParseStepUpAction(string(invalid)); err == nil || IsValidStepUpAction(invalid) || StepUpProofTTLForAction(invalid) != 0 {
			t.Fatalf("unknown step-up action %q accepted", invalid)
		}
	}
}

func TestGenerateStepUpTokenRejectsUnknownAction(t *testing.T) {
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	user := model.User{ID: 9, Username: "alice", Role: "admin", TokenVersion: 3}
	for _, action := range []StepUpAction{"", "future.action"} {
		if _, _, err := manager.GenerateStepUpToken(user, action, testStepUpSessionID); err == nil {
			t.Fatalf("GenerateStepUpToken accepted action %q", action)
		}
	}
}

func TestStepUpActionRetentionHoldRelease(t *testing.T) {
	if StepUpActionRetentionHoldRelease != StepUpAction("retention.hold_release") {
		t.Fatalf("unexpected retention hold-release action %q", StepUpActionRetentionHoldRelease)
	}
	if StepUpActionRetentionHoldRelease == StepUpActionRepositoryPurge {
		t.Fatal("hold release and repository purge must have isolated step-up purposes")
	}

	action, err := ParseStepUpAction("retention.hold_release")
	if err != nil {
		t.Fatalf("parse retention hold-release action: %v", err)
	}
	if action != StepUpActionRetentionHoldRelease {
		t.Fatalf("parsed action = %q, want %q", action, StepUpActionRetentionHoldRelease)
	}

	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	user := model.User{ID: 9, Username: "alice", Role: "admin", TokenVersion: 3}
	proof, _, err := manager.GenerateStepUpToken(user, StepUpActionRetentionHoldRelease, testStepUpSessionID)
	if err != nil {
		t.Fatalf("generate retention hold-release proof: %v", err)
	}
	claims, err := manager.ParseToken(proof)
	if err != nil {
		t.Fatalf("parse retention hold-release proof: %v", err)
	}
	if claims.StepUpAction != StepUpActionRetentionHoldRelease || claims.StepUpAction == StepUpActionRepositoryPurge {
		t.Fatalf("hold-release proof has non-isolated action %q", claims.StepUpAction)
	}
}

func TestSystemBackupDownloadProofIsActionBoundAndShortLived(t *testing.T) {
	action, err := ParseStepUpAction("system.backup_download")
	if err != nil || action != StepUpActionSystemBackupDownload {
		t.Fatalf("parse backup download action: action=%q err=%v", action, err)
	}
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	user := model.User{ID: 9, Username: "alice", Role: "admin", TokenVersion: 3}
	proof, _, err := manager.GenerateStepUpToken(user, action, testStepUpSessionID)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := manager.ParseToken(proof)
	if err != nil {
		t.Fatal(err)
	}
	if claims.StepUpAction != action || claims.StepUpAction == StepUpActionConfigExport || claims.Purpose != PurposeStepUp ||
		claims.IssuedAt == nil || claims.ExpiresAt == nil || claims.ExpiresAt.Sub(claims.IssuedAt.Time) != 300*time.Second {
		t.Fatalf("backup download proof must be isolated and expire after 300 seconds: %+v", claims)
	}
}

func TestStepUpActionSSHKeyReplacement(t *testing.T) {
	action, err := ParseStepUpAction("ssh_key.replace")
	if err != nil || action != StepUpActionSSHKeyReplace || StepUpActionSSHKeyReplace != StepUpAction("ssh_key.replace") {
		t.Fatalf("replacement action not registered: action=%q err=%v", action, err)
	}
	if StepUpProofTTLForAction(action) != 300*time.Second {
		t.Fatal("replacement proof must retain the exact five-minute policy")
	}
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	user := model.User{ID: 9, Username: "alice", Role: "admin", TokenVersion: 3}
	for _, sessions := range [][]string{nil, {""}, {"not-a-session"}, {"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}, {testStepUpSessionID, testStepUpSessionID}} {
		if _, _, err := manager.GenerateStepUpToken(user, action, sessions...); err == nil {
			t.Fatal("replacement proof accepted missing or invalid session binding")
		}
	}
	proof, expiry, err := manager.GenerateStepUpToken(user, action, testStepUpSessionID)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := manager.ParseToken(proof)
	if err != nil {
		t.Fatal(err)
	}
	if claims.StepUpAction != action || claims.Purpose != PurposeStepUp || claims.SessionID != testStepUpSessionID ||
		claims.UserID != user.ID || claims.Role != user.Role || claims.TokenVersion != user.TokenVersion ||
		!lowerHexID(claims.ID) || claims.IssuedAt == nil || claims.ExpiresAt == nil ||
		claims.ExpiresAt.Sub(claims.IssuedAt.Time) != 300*time.Second || !claims.ExpiresAt.Equal(expiry) {
		t.Fatal("replacement proof lost its exact action, identity, session or lifetime")
	}
}
