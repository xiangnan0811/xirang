package handlers

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	nodePkg "xirang/backend/internal/node"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

func seedPreflightCapacityNode(t *testing.T, db *gorm.DB, server *dockerSSHTestServer, name string) model.Node {
	t.Helper()
	host, portText, err := net.SplitHostPort(server.addr)
	if err != nil {
		t.Fatalf("split SSH test address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse SSH test port: %v", err)
	}
	node := model.Node{
		Name:              name,
		Host:              host,
		Port:              port,
		Username:          "docker-test",
		AuthType:          "password",
		Password:          server.password,
		BackupDir:         name,
		Status:            "offline",
		ConnectionLatency: 17,
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&node).Error; err != nil {
		t.Fatalf("create %s node: %v", name, err)
	}
	return node
}

func TestMigrationPreflightReadsFreshCapacityForBothNodes(t *testing.T) {
	sourceServer := startDockerSSHTestServer(t, func(_ string, channel ssh.Channel, _ <-chan struct{}) {
		writeDockerSSHResult(channel, []byte("100G 80G\n"), 0)
	})
	targetServer := startDockerSSHTestServer(t, func(_ string, channel ssh.Channel, _ <-chan struct{}) {
		writeDockerSSHResult(channel, []byte("50G 40G\n"), 0)
	})

	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	source := seedPreflightCapacityNode(t, db, sourceServer, "fresh-capacity-source")
	target := seedPreflightCapacityNode(t, db, targetServer, "fresh-capacity-target")

	router := gin.New()
	handler := NewNodeHandler(db, nil, nodePkg.NewNodeService(gormrepo.NewNodeRepository(db)))
	router.POST("/nodes/:id/migrate-preflight", func(c *gin.Context) {
		c.Set("userID", uint(101))
		c.Set("username", "alice")
		c.Set("role", "admin")
		handler.MigratePreflight(c)
	})
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/nodes/%d/migrate-preflight", source.ID), strings.NewReader(fmt.Sprintf(`{"targetNodeId":%d}`, target.ID)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("preflight status=%d body=%s", response.Code, response.Body.String())
	}

	var envelope struct {
		Code int                      `json:"code"`
		Data MigratePreflightResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode preflight response: %v body=%s", err, response.Body.String())
	}
	if envelope.Code != http.StatusOK || !envelope.Data.CanProceed {
		t.Fatalf("capacity warning must not block migration: %+v", envelope.Data)
	}
	if envelope.Data.SourceNode.DiskUsedGB != 80 || envelope.Data.SourceNode.DiskTotalGB != 100 {
		t.Fatalf("source capacity was not read fresh: %+v", envelope.Data.SourceNode)
	}
	if envelope.Data.TargetNode.DiskUsedGB != 40 || envelope.Data.TargetNode.DiskTotalGB != 50 {
		t.Fatalf("target capacity was not read fresh: %+v", envelope.Data.TargetNode)
	}
	var diskCheck *PreflightCheckItem
	for i := range envelope.Data.Checks {
		if envelope.Data.Checks[i].Name == "disk" {
			diskCheck = &envelope.Data.Checks[i]
			break
		}
	}
	if diskCheck == nil || diskCheck.Status != "warn" {
		t.Fatalf("expected capacity shortage warning, got %+v", envelope.Data.Checks)
	}

	if command := waitDockerSSHCommand(t, sourceServer); !strings.Contains(command, "df -BG /") {
		t.Fatalf("source capacity command=%q", command)
	}
	if command := waitDockerSSHCommand(t, targetServer); !strings.Contains(command, "df -BG /") {
		t.Fatalf("target capacity command=%q", command)
	}

	var persistedSource, persistedTarget model.Node
	if err := db.First(&persistedSource, source.ID).Error; err != nil {
		t.Fatalf("reload source: %v", err)
	}
	if err := db.First(&persistedTarget, target.ID).Error; err != nil {
		t.Fatalf("reload target: %v", err)
	}
	if persistedSource.Status != source.Status || persistedTarget.Status != target.Status ||
		persistedSource.ConnectionLatency != source.ConnectionLatency || persistedTarget.ConnectionLatency != target.ConnectionLatency ||
		!persistedSource.UpdatedAt.Equal(source.UpdatedAt) || !persistedTarget.UpdatedAt.Equal(target.UpdatedAt) {
		t.Fatalf("preflight must not mutate node records: source=%+v target=%+v", persistedSource, persistedTarget)
	}
}

type preflightManagedCredential struct {
	privateKey string
	publicKey  ssh.PublicKey
}

func newPreflightManagedCredential(t *testing.T) preflightManagedCredential {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate managed SSH key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("load managed SSH signer: %v", err)
	}
	pemBlock, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		t.Fatalf("marshal managed SSH private key: %v", err)
	}
	return preflightManagedCredential{
		privateKey: string(pem.EncodeToMemory(pemBlock)),
		publicKey:  signer.PublicKey(),
	}
}

func configurePreflightManagedKeyServer(t *testing.T, server *dockerSSHTestServer, expected ssh.PublicKey) {
	t.Helper()
	server.config.PublicKeyCallback = func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if metadata.User() != "docker-test" || !bytes.Equal(key.Marshal(), expected.Marshal()) {
			return nil, fmt.Errorf("managed SSH test key rejected")
		}
		return nil, nil
	}
}

func seedPreflightManagedKeyNode(t *testing.T, db *gorm.DB, server *dockerSSHTestServer, name string, managed preflightManagedCredential, allowedPurposes string) (model.Node, model.SSHKey) {
	t.Helper()
	host, portText, err := net.SplitHostPort(server.addr)
	if err != nil {
		t.Fatalf("split SSH test address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse SSH test port: %v", err)
	}
	key := model.SSHKey{
		Name:            name + "-key",
		Username:        "docker-test",
		KeyType:         managed.publicKey.Type(),
		PrivateKey:      managed.privateKey,
		Fingerprint:     ssh.FingerprintSHA256(managed.publicKey),
		AllowedPurposes: allowedPurposes,
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("create %s managed key: %v", name, err)
	}
	keyID := key.ID
	node := model.Node{
		Name:              name,
		Host:              host,
		Port:              port,
		Username:          "docker-test",
		AuthType:          "key",
		SSHKeyID:          &keyID,
		BackupDir:         name,
		Status:            "offline",
		ConnectionLatency: 17,
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&node).Error; err != nil {
		t.Fatalf("create %s node: %v", name, err)
	}
	return node, key
}

func runMigrationPreflightRequest(t *testing.T, db *gorm.DB, source, target model.Node) (int, MigratePreflightResponse) {
	t.Helper()
	router := gin.New()
	handler := NewNodeHandler(db, nil, nodePkg.NewNodeService(gormrepo.NewNodeRepository(db)))
	router.POST("/nodes/:id/migrate-preflight", func(c *gin.Context) {
		c.Set("userID", uint(101))
		c.Set("username", "alice")
		c.Set("role", "admin")
		handler.MigratePreflight(c)
	})
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/nodes/%d/migrate-preflight", source.ID), strings.NewReader(fmt.Sprintf(`{"targetNodeId":%d}`, target.ID)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var envelope struct {
		Code int                      `json:"code"`
		Data MigratePreflightResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode preflight response: %v body=%s", err, response.Body.String())
	}
	return response.Code, envelope.Data
}

func assertPreflightAuditIdentity(t *testing.T, event model.CredentialAuditEvent, node model.Node, key model.SSHKey, outcome string, privateKey string) {
	t.Helper()
	if event.Purpose != sshutil.PurposeNodeMigration || event.Outcome != outcome {
		t.Fatalf("preflight audit purpose/outcome=%q/%q, want %q/%q: %+v", event.Purpose, event.Outcome, sshutil.PurposeNodeMigration, outcome, event)
	}
	if event.NodeID == nil || *event.NodeID != node.ID || event.SSHKeyID == nil || *event.SSHKeyID != key.ID {
		t.Fatalf("preflight audit identity node/key mismatch: %+v", event)
	}
	if event.CredentialKind != "ssh_key" || event.CredentialSource != fmt.Sprintf("ssh_key_id=%d", key.ID) {
		t.Fatalf("preflight audit credential identity mismatch: %+v", event)
	}
	if strings.Contains(event.Metadata, privateKey) || strings.Contains(event.ErrorMessage, privateKey) {
		t.Fatalf("preflight audit contains private key material: %+v", event)
	}
}

func TestMigrationPreflightAuditsSourceAndTargetManagedCredentials(t *testing.T) {
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_PREFLIGHT_DATA_KEY_32_BYTES")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	sourceManaged := newPreflightManagedCredential(t)
	targetManaged := newPreflightManagedCredential(t)
	sourceServer := startDockerSSHTestServer(t, func(_ string, channel ssh.Channel, _ <-chan struct{}) {
		writeDockerSSHResult(channel, []byte("100G 80G\n"), 0)
	})
	targetServer := startDockerSSHTestServer(t, func(_ string, channel ssh.Channel, _ <-chan struct{}) {
		writeDockerSSHResult(channel, []byte("50G 40G\n"), 0)
	})
	configurePreflightManagedKeyServer(t, sourceServer, sourceManaged.publicKey)
	configurePreflightManagedKeyServer(t, targetServer, targetManaged.publicKey)

	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.SSHKey{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	source, sourceKey := seedPreflightManagedKeyNode(t, db, sourceServer, "managed-source", sourceManaged, sshutil.PurposeNodeMigration)
	target, targetKey := seedPreflightManagedKeyNode(t, db, targetServer, "managed-target", targetManaged, sshutil.PurposeNodeMigration)

	status, data := runMigrationPreflightRequest(t, db, source, target)
	if status != http.StatusOK || !data.CanProceed {
		t.Fatalf("capacity warning must not block migration: status=%d data=%+v", status, data)
	}
	var diskCheck *PreflightCheckItem
	for i := range data.Checks {
		if data.Checks[i].Name == "disk" {
			diskCheck = &data.Checks[i]
			break
		}
	}
	if diskCheck == nil || diskCheck.Status != "warn" {
		t.Fatalf("expected capacity shortage warning, got %+v", data.Checks)
	}
	if command := waitDockerSSHCommand(t, sourceServer); !strings.Contains(command, "df -BG /") {
		t.Fatalf("source capacity command=%q", command)
	}
	if command := waitDockerSSHCommand(t, targetServer); !strings.Contains(command, "df -BG /") {
		t.Fatalf("target capacity command=%q", command)
	}

	var events []model.CredentialAuditEvent
	if err := db.Where("action = ?", "node_migration.preflight").Order("id asc").Find(&events).Error; err != nil {
		t.Fatalf("load preflight audit events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("preflight should audit source and target independently, got %d: %+v", len(events), events)
	}
	byNode := make(map[uint]model.CredentialAuditEvent, len(events))
	for _, event := range events {
		byNode[*event.NodeID] = event
	}
	assertPreflightAuditIdentity(t, byNode[source.ID], source, sourceKey, credentialaudit.OutcomeSuccess, sourceManaged.privateKey)
	assertPreflightAuditIdentity(t, byNode[target.ID], target, targetKey, credentialaudit.OutcomeSuccess, targetManaged.privateKey)
}

func TestMigrationPreflightAuditsBlockedRetiredSourceCredentialIndependently(t *testing.T) {
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_PREFLIGHT_DATA_KEY_32_BYTES")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	sourceManaged := newPreflightManagedCredential(t)
	targetManaged := newPreflightManagedCredential(t)
	sourceServer := startDockerSSHTestServer(t, func(_ string, channel ssh.Channel, _ <-chan struct{}) {
		writeDockerSSHResult(channel, []byte("100G 80G\n"), 0)
	})
	targetServer := startDockerSSHTestServer(t, func(_ string, channel ssh.Channel, _ <-chan struct{}) {
		writeDockerSSHResult(channel, []byte("100G 20G\n"), 0)
	})
	configurePreflightManagedKeyServer(t, sourceServer, sourceManaged.publicKey)
	configurePreflightManagedKeyServer(t, targetServer, targetManaged.publicKey)

	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.SSHKey{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	source, sourceKey := seedPreflightManagedKeyNode(t, db, sourceServer, "retired-source", sourceManaged, sshutil.PurposeProbe)
	target, targetKey := seedPreflightManagedKeyNode(t, db, targetServer, "retired-target", targetManaged, sshutil.PurposeNodeMigration)

	status, data := runMigrationPreflightRequest(t, db, source, target)
	if status != http.StatusOK || !data.CanProceed {
		t.Fatalf("retired source scope must remain a warning while target succeeds: status=%d data=%+v", status, data)
	}
	if command := waitDockerSSHCommand(t, targetServer); !strings.Contains(command, "df -BG /") {
		t.Fatalf("target capacity command=%q", command)
	}
	var diskCheck *PreflightCheckItem
	for i := range data.Checks {
		if data.Checks[i].Name == "disk" {
			diskCheck = &data.Checks[i]
			break
		}
	}
	if diskCheck == nil || diskCheck.Status != "warn" {
		t.Fatalf("source scope rejection should preserve capacity warning, got %+v", data.Checks)
	}

	var events []model.CredentialAuditEvent
	if err := db.Where("action = ?", "node_migration.preflight").Order("id asc").Find(&events).Error; err != nil {
		t.Fatalf("load preflight audit events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("preflight should audit blocked source and successful target, got %d: %+v", len(events), events)
	}
	byNode := make(map[uint]model.CredentialAuditEvent, len(events))
	for _, event := range events {
		byNode[*event.NodeID] = event
	}
	assertPreflightAuditIdentity(t, byNode[source.ID], source, sourceKey, credentialaudit.OutcomeBlocked, sourceManaged.privateKey)
	assertPreflightAuditIdentity(t, byNode[target.ID], target, targetKey, credentialaudit.OutcomeSuccess, targetManaged.privateKey)
}
