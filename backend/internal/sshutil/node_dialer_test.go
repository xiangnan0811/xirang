package sshutil

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"golang.org/x/crypto/ssh"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestNodeDialerLoadsManagedKeyFromDBAndAuditsRepositoryPurpose(t *testing.T) {
	db := setupNodeDialerDB(t)
	key := model.SSHKey{
		Name:            "provider-reader-key",
		PrivateKey:      nodeDialerTestPrivateKey(t),
		AllowedPurposes: PurposeRepositoryProbe,
		AllowedNodeIDs:  "7",
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	node := model.Node{ID: 7, Host: "example.invalid", Port: 22, Username: "reader", AuthType: "key", SSHKeyID: &key.ID}
	dialer := NewNodeDialer(db)
	dialCalled := false
	dialer.hostKeyResolver = func() (ssh.HostKeyCallback, error) { return ssh.InsecureIgnoreHostKey(), nil }
	dialer.dial = func(context.Context, string, string, []ssh.AuthMethod, ssh.HostKeyCallback) (*ssh.Client, error) {
		dialCalled = true
		return nil, nil
	}
	_, err := dialer.Dial(context.Background(), node, PurposeRepositoryProbe, DialAuditContext{Action: "repository.probe", CorrelationID: "corr-1", UserID: 1, Username: "admin", Role: "admin"})
	if err != nil || !dialCalled {
		t.Fatalf("Dial err=%v called=%v", err, dialCalled)
	}
	var event model.CredentialAuditEvent
	if err := db.Where("action = ?", "repository.probe").First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.Purpose != PurposeRepositoryProbe || event.CredentialKind != "ssh_key" || event.CredentialSource == "" || !strings.Contains(event.Metadata, "corr-1") {
		t.Fatalf("unsafe or incomplete credential audit: %+v", event)
	}
}

func TestNodeDialerWrongPurposeStopsBeforeNetworkAndLastUsedMutation(t *testing.T) {
	db := setupNodeDialerDB(t)
	key := model.SSHKey{Name: "probe-only", PrivateKey: nodeDialerTestPrivateKey(t), AllowedPurposes: PurposeRepositoryProbe, AllowedNodeIDs: "9"}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	node := model.Node{ID: 9, Host: "example.invalid", AuthType: "key", SSHKeyID: &key.ID}
	dialer := NewNodeDialer(db)
	dialer.dial = func(context.Context, string, string, []ssh.AuthMethod, ssh.HostKeyCallback) (*ssh.Client, error) {
		t.Fatal("network dial must not run")
		return nil, nil
	}
	if _, err := dialer.Dial(context.Background(), node, PurposeRepositoryRead, DialAuditContext{Action: "repository.read"}); err == nil {
		t.Fatal("wrong-purpose key accepted")
	}
	var persisted model.SSHKey
	if err := db.First(&persisted, key.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.LastUsedAt != nil {
		t.Fatalf("blocked key last_used_at mutated: %v", persisted.LastUsedAt)
	}
}

func TestNodeDialerSanitizesDependencyErrors(t *testing.T) {
	dialer := NewNodeDialer(nil)
	dialer.buildAuth = func(model.Node, *gorm.DB, string) ([]ssh.AuthMethod, ResolvedCredential, error) {
		return nil, ResolvedCredential{Kind: "password", Source: "node.password"}, errors.New("FAKE_PASSWORD_FOR_TEST_ONLY")
	}
	_, err := dialer.Dial(context.Background(), model.Node{ID: 1}, PurposeRepositoryList, DialAuditContext{Action: "repository.list"})
	if err == nil || strings.Contains(err.Error(), "FAKE_PASSWORD_FOR_TEST_ONLY") {
		t.Fatalf("dependency error leaked: %v", err)
	}
}

func TestNodeDialerAttemptSupportsTaskPurposeForCompatibilityCallers(t *testing.T) {
	dialer := NewNodeDialer(nil)
	wantClient := &ssh.Client{}
	dialer.buildAuth = func(_ model.Node, db *gorm.DB, purpose string) ([]ssh.AuthMethod, ResolvedCredential, error) {
		if db != nil || purpose != PurposeTaskCommand {
			t.Fatalf("unexpected shared dial inputs: db=%v purpose=%q", db, purpose)
		}
		return []ssh.AuthMethod{ssh.Password("FAKE_PASSWORD_FOR_TEST_ONLY")}, ResolvedCredential{Kind: "password", Source: "node.password"}, nil
	}
	dialer.hostKeyResolver = func() (ssh.HostKeyCallback, error) { return ssh.InsecureIgnoreHostKey(), nil }
	dialer.dial = func(context.Context, string, string, []ssh.AuthMethod, ssh.HostKeyCallback) (*ssh.Client, error) {
		return wantClient, nil
	}

	client, attempt, err := dialer.DialAttempt(context.Background(), model.Node{ID: 7, AuthType: "password"}, PurposeTaskCommand)
	if err != nil || client != wantClient {
		t.Fatalf("DialAttempt client=%p err=%v", client, err)
	}
	if attempt.Stage != NodeDialStageDial || attempt.Credential.Kind != "password" || attempt.Credential.Source != "node.password" {
		t.Fatalf("DialAttempt metadata=%+v", attempt)
	}
}

func TestNilNodeDialerFailsClosedWithoutPanic(t *testing.T) {
	var dialer *NodeDialer
	_, err := dialer.Dial(context.Background(), model.Node{ID: 1}, PurposeRepositoryProbe, DialAuditContext{Action: "repository.probe"})
	if err == nil {
		t.Fatal("nil NodeDialer unexpectedly succeeded")
	}
}

func setupNodeDialerDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SSHKey{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func nodeDialerTestPrivateKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}
