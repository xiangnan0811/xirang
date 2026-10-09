package handlers

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	rotationTestAdminUsername = "rotation-admin"
	rotationTestPrivateMarker = "FAKE_ROTATION_PRIVATE_KEY_FOR_TEST_ONLY"
)

type rotationTestNodeResult struct {
	NodeID    uint   `json:"node_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
}

type rotationTestResponse struct {
	Status               string                   `json:"status"`
	Reason               string                   `json:"reason"`
	PublicKeyFingerprint string                   `json:"public_key_fingerprint"`
	Results              []rotationTestNodeResult `json:"results"`
}

type rotationTestEnvelope struct {
	Code int                  `json:"code"`
	Data rotationTestResponse `json:"data"`
}

type rotationTestSession struct {
	User    model.User
	Binding middleware.SessionBinding
}

type rotationSSHServer struct {
	address              string
	hostKey              ssh.PublicKey
	candidate            ssh.PublicKey
	acceptCandidate      bool
	barrier              bool
	listener             net.Listener
	authReached          chan struct{}
	release              chan struct{}
	authAttempts         atomic.Int32
	nonCandidateAttempts atomic.Int32
}

func setSSHRotationTestEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_32_BYTES_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
}

var rotationPostgresSchemaSequence atomic.Uint64

func openSSHKeyRotationTestDB(t *testing.T, engine string) *gorm.DB {
	t.Helper()
	setSSHRotationTestEnvironment(t)
	config := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	var db *gorm.DB
	if engine == "postgres" {
		dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
		if dsn == "" {
			t.Skip("TEST_POSTGRES_DSN required for SSH key rotation PostgreSQL acceptance")
		}
		parsed, err := url.Parse(dsn)
		if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
			t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
		}
		base, err := gorm.Open(postgres.Open(dsn), config)
		if err != nil {
			t.Fatalf("open PostgreSQL rotation base: %v", err)
		}
		schema := fmt.Sprintf("ssh_rotation_%d_%d", os.Getpid(), rotationPostgresSchemaSequence.Add(1))
		if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			t.Fatalf("create PostgreSQL rotation schema: %v", err)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		db, err = gorm.Open(postgres.Open(parsed.String()), config)
		if err != nil {
			_ = base.Exec("DROP SCHEMA " + schema + " CASCADE").Error
			t.Fatalf("open PostgreSQL rotation schema: %v", err)
		}
		scopedSQLDB, err := db.DB()
		if err != nil {
			t.Fatalf("get PostgreSQL rotation pool: %v", err)
		}
		baseSQLDB, err := base.DB()
		if err != nil {
			_ = scopedSQLDB.Close()
			t.Fatalf("get PostgreSQL rotation base pool: %v", err)
		}
		t.Cleanup(func() {
			_ = scopedSQLDB.Close()
			_ = base.Exec("DROP SCHEMA " + schema + " CASCADE").Error
			_ = baseSQLDB.Close()
		})
	} else {
		db = openSSHKeyHandlerTestDB(t)
		if err := db.AutoMigrate(&model.User{}, &model.TokenRevocation{}); err != nil {
			t.Fatalf("migrate SSH rotation auth tables: %v", err)
		}
		return db
	}
	if err := db.AutoMigrate(&model.User{}, &model.TokenRevocation{}, &model.SSHKey{}, &model.Node{}, &model.NodeOwner{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate SSH rotation tables: %v", err)
	}
	return db
}

func openSSHKeyRotationProductionSQLiteDB(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	setSSHRotationTestEnvironment(t)
	dsn := fmt.Sprintf(
		"file:%s?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_txlock=immediate&_loc=UTC",
		filepath.Join(t.TempDir(), "rotation-production.db"),
	)
	config := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	db, err := gorm.Open(sqlite.Open(dsn), config)
	if err != nil {
		t.Fatalf("open production SQLite rotation database: %v", err)
	}
	lockDB, err := gorm.Open(sqlite.Open(dsn), config)
	if err != nil {
		t.Fatalf("open production SQLite rotation lock database: %v", err)
	}
	for name, database := range map[string]*gorm.DB{"rotation": db, "lock": lockDB} {
		sqlDB, sqlErr := database.DB()
		if sqlErr != nil {
			t.Fatalf("get %s SQLite pool: %v", name, sqlErr)
		}
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
		pool := sqlDB
		t.Cleanup(func() { _ = pool.Close() })
	}
	if err := db.AutoMigrate(&model.User{}, &model.TokenRevocation{}, &model.SSHKey{}, &model.Node{}, &model.NodeOwner{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate production SQLite rotation database: %v", err)
	}
	return db, lockDB
}

func startRotationSSHServer(t *testing.T, candidatePrivateKey string, acceptCandidate, barrier bool) *rotationSSHServer {
	t.Helper()
	parsedCandidate, err := ssh.ParseRawPrivateKey([]byte(candidatePrivateKey))
	if err != nil {
		t.Fatalf("parse rotation candidate key: %v", err)
	}
	candidateSigner, err := ssh.NewSignerFromKey(parsedCandidate)
	if err != nil {
		t.Fatalf("build rotation candidate signer: %v", err)
	}
	_, hostPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate rotation host key: %v", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivateKey)
	if err != nil {
		t.Fatalf("build rotation host signer: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen rotation SSH server: %v", err)
	}
	server := &rotationSSHServer{
		address:         listener.Addr().String(),
		hostKey:         hostSigner.PublicKey(),
		candidate:       candidateSigner.PublicKey(),
		acceptCandidate: acceptCandidate,
		barrier:         barrier,
		listener:        listener,
		authReached:     make(chan struct{}, 32),
		release:         make(chan struct{}),
	}
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, publicKey ssh.PublicKey) (*ssh.Permissions, error) {
			matchesCandidate := bytes.Equal(publicKey.Marshal(), server.candidate.Marshal())
			if !matchesCandidate {
				server.nonCandidateAttempts.Add(1)
			}
			server.authAttempts.Add(1)
			if server.barrier {
				select {
				case server.authReached <- struct{}{}:
				default:
				}
				<-server.release
			}
			if !server.acceptCandidate || !matchesCandidate {
				return nil, errors.New("FAKE_REMOTE_ROTATION_AUTH_FAILURE_FOR_TEST_ONLY")
			}
			return nil, nil
		},
	}
	config.AddHostKey(hostSigner)
	t.Cleanup(func() {
		select {
		case <-server.release:
		default:
			close(server.release)
		}
		_ = listener.Close()
	})
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close() //nolint:errcheck
				sshConn, channels, requests, handshakeErr := ssh.NewServerConn(conn, config)
				if handshakeErr != nil {
					return
				}
				defer sshConn.Close() //nolint:errcheck
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					_ = channel.Reject(ssh.Prohibited, "rotation test server")
				}
			}()
		}
	}()
	return server
}

func waitRotationSSHAuth(t *testing.T, server *rotationSSHServer, count int) {
	t.Helper()
	for i := range count {
		select {
		case <-server.authReached:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for rotation SSH auth barrier %d/%d", i+1, count)
		}
	}
}

func releaseRotationSSHServer(server *rotationSSHServer) {
	select {
	case <-server.release:
	default:
		close(server.release)
	}
}

func configureRotationKnownHosts(t *testing.T, servers ...*rotationSSHServer) string {
	t.Helper()
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "true")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	path := filepath.Join(t.TempDir(), "ssh", "known_hosts")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create rotation known_hosts directory: %v", err)
	}
	var content strings.Builder
	for _, server := range servers {
		content.WriteString(knownhosts.Line([]string{knownhosts.Normalize(server.address)}, server.hostKey))
		content.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		t.Fatalf("write rotation known_hosts: %v", err)
	}
	t.Setenv("SSH_KNOWN_HOSTS_PATH", path)
	return path
}

func seedRotationSession(t *testing.T, db *gorm.DB) rotationTestSession {
	t.Helper()
	user := model.User{
		Username:     rotationTestAdminUsername,
		PasswordHash: "FAKE_ROTATION_PASSWORD_HASH_FOR_TEST_ONLY",
		Role:         "admin",
		TokenVersion: 7,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create rotation session user: %v", err)
	}
	return rotationTestSession{
		User: user,
		Binding: middleware.SessionBinding{
			JTI:          strings.Repeat("a", 32),
			UserID:       user.ID,
			Role:         user.Role,
			TokenVersion: user.TokenVersion,
			ExpiresAt:    time.Now().UTC().Add(time.Hour),
		},
	}
}

func newSSHKeyRotationTestRouter(db *gorm.DB, session rotationTestSession, role string, withBinding bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, session.User.ID)
		c.Set(middleware.CtxRole, role)
		if withBinding {
			c.Set(middleware.CtxSessionBinding, session.Binding)
		}
		c.Next()
	})
	handler := NewSSHKeyHandler(db)
	secured := r.Group("")
	secured.Use(middleware.RequireRole("admin"), middleware.RBAC("ssh_keys:write"))
	secured.POST("/ssh-keys/:id/rotate", handler.Rotate)
	return r
}

func newSSHKeyRotationUpdateRouter(db *gorm.DB, session rotationTestSession) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, session.User.ID)
		c.Set(middleware.CtxRole, session.User.Role)
		c.Set(middleware.CtxSessionBinding, session.Binding)
		c.Next()
	})
	handler := NewSSHKeyHandler(db)
	r.PUT("/ssh-keys/:id", handler.Update)
	return r
}

func rotationJSONRequest(t *testing.T, method, path string, body []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func serveRotationJSON(t *testing.T, router *gin.Engine, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, rotationJSONRequest(t, method, path, body))
	return resp
}

func decodeRotationResponse(t *testing.T, resp *httptest.ResponseRecorder) rotationTestResponse {
	t.Helper()
	var envelope rotationTestEnvelope
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode rotation response status=%d: %v", resp.Code, err)
	}
	return envelope.Data
}

func rotationRequestBody(t *testing.T, privateKey, keyType, name string) []byte {
	t.Helper()
	body := map[string]any{"private_key": privateKey}
	if keyType != "" {
		body["key_type"] = keyType
	}
	if name != "" {
		body["name"] = name
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode rotation request: %v", err)
	}
	return encoded
}

func seedRotationKey(t *testing.T, db *gorm.DB, oldPrivateKey string) model.SSHKey {
	return seedRotationKeyNamed(t, db, oldPrivateKey, "old-rotation-key")
}

func seedRotationKeyNamed(t *testing.T, db *gorm.DB, oldPrivateKey, name string) model.SSHKey {
	t.Helper()
	key := model.SSHKey{
		Name:        name,
		Username:    "root",
		KeyType:     sshutil.SSHKeyTypeAuto,
		PrivateKey:  oldPrivateKey,
		Fingerprint: generateFingerprint(oldPrivateKey),
		ExpiresAt:   new(time.Now().UTC().Add(2 * time.Hour).Truncate(time.Microsecond)),
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("create rotation key: %v", err)
	}
	return key
}

func seedRotationNode(t *testing.T, db *gorm.DB, keyID uint, name, host string, port int, archived bool) model.Node {
	t.Helper()
	node := model.Node{
		Name:              name,
		Host:              host,
		Port:              port,
		Username:          "root",
		AuthType:          "key",
		SSHKeyID:          new(keyID),
		Tags:              "rotation,prod",
		Status:            "offline",
		BackupDir:         name,
		ConnectionLatency: 17,
		Archived:          archived,
	}
	seen := time.Now().UTC().Add(-time.Minute)
	node.LastSeenAt = &seen
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create rotation node %s: %v", name, err)
	}
	return node
}

func setRotationKeyScope(t *testing.T, db *gorm.DB, keyID uint, nodeIDs []uint, disabled bool, expiresAt *time.Time) {
	t.Helper()
	ids := make([]string, len(nodeIDs))
	for i, id := range nodeIDs {
		ids[i] = strconv.FormatUint(uint64(id), 10)
	}
	updates := map[string]any{
		"allowed_purposes": sshutil.PurposeSSHKeyTest,
		"allowed_node_ids": strings.Join(ids, ","),
		"disabled":         disabled,
		"expires_at":       expiresAt,
	}
	if err := db.Model(&model.SSHKey{}).Where("id = ?", keyID).Updates(updates).Error; err != nil {
		t.Fatalf("set rotation key scope: %v", err)
	}
}

func readRotationKey(t *testing.T, db *gorm.DB, keyID uint) model.SSHKey {
	t.Helper()
	var key model.SSHKey
	if err := db.First(&key, keyID).Error; err != nil {
		t.Fatalf("read rotation key: %v", err)
	}
	return key
}

func readRotationNode(t *testing.T, db *gorm.DB, nodeID uint) model.Node {
	t.Helper()
	var node model.Node
	if err := db.First(&node, nodeID).Error; err != nil {
		t.Fatalf("read rotation node: %v", err)
	}
	return node
}

func assertRotationNotSaved(t *testing.T, db *gorm.DB, keyID uint, oldPrivate, oldName string) {
	t.Helper()
	key := readRotationKey(t, db, keyID)
	if key.PrivateKey != oldPrivate || key.Fingerprint != generateFingerprint(oldPrivate) || key.Name != oldName {
		t.Fatalf("rotation must leave key unchanged: name=%q private_equal=%v", key.Name, key.PrivateKey == oldPrivate)
	}
}
func assertRotationPrivatePreserved(t *testing.T, db *gorm.DB, keyID uint, oldPrivate string) {
	t.Helper()
	key := readRotationKey(t, db, keyID)
	if key.PrivateKey != oldPrivate {
		t.Fatalf("rotation must preserve externally written key private material")
	}
}

func assertRotationAuditSafe(t *testing.T, db *gorm.DB, keyID uint, body string, forbidden ...string) {
	t.Helper()
	var events []model.CredentialAuditEvent
	if err := db.Where("action = ? AND ssh_key_id = ?", "ssh_key.rotate", keyID).Order("id asc").Find(&events).Error; err != nil {
		t.Fatalf("read rotation audit: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("rotation must write a credential audit event")
	}
	for _, event := range events {
		if event.Purpose != sshutil.PurposeSSHKeyTest || event.CredentialSource != fmt.Sprintf("ssh_key_id=%d", keyID) {
			t.Fatalf("unsafe rotation audit identity")
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encode rotation audit: %v", err)
		}
		for _, value := range forbidden {
			if bytes.Contains(encoded, []byte(value)) || strings.Contains(body, value) {
				t.Fatalf("rotation response or audit contained forbidden test material")
			}
		}
		var metadata map[string]any
		if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
			t.Fatalf("rotation audit metadata must be JSON")
		}
		for _, key := range []string{"node_count", "success_count", "failure_count", "unknown_count", "saved", "reason"} {
			if _, ok := metadata[key]; !ok {
				t.Fatalf("rotation audit metadata missing required field %q", key)
			}
		}
	}
}

func assertRotationAuditReason(t *testing.T, db *gorm.DB, keyID uint, want string) {
	t.Helper()
	var event model.CredentialAuditEvent
	if err := db.Where("action = ? AND ssh_key_id = ?", "ssh_key.rotate", keyID).Order("id desc").First(&event).Error; err != nil {
		t.Fatalf("read latest rotation audit: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
		t.Fatalf("decode latest rotation audit metadata: %v", err)
	}
	if got, _ := metadata["reason"].(string); got != want {
		t.Fatalf("rotation audit reason=%q want=%q", got, want)
	}
}

func TestSSHKeyRotationSavesOnlyAfterAllNodesVerified(t *testing.T) {
	db := openSSHKeyRotationTestDB(t, "sqlite")
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	first := startRotationSSHServer(t, candidatePrivate, true, true)
	second := startRotationSSHServer(t, candidatePrivate, true, true)
	configureRotationKnownHosts(t, first, second)
	key := seedRotationKey(t, db, oldPrivate)
	firstHost, firstPortText, _ := net.SplitHostPort(first.address)
	firstPort, _ := strconv.Atoi(firstPortText)
	secondHost, secondPortText, _ := net.SplitHostPort(second.address)
	secondPort, _ := strconv.Atoi(secondPortText)
	node1 := seedRotationNode(t, db, key.ID, "rotation-node-1", firstHost, firstPort, false)
	node2 := seedRotationNode(t, db, key.ID, "rotation-node-2", secondHost, secondPort, false)
	setRotationKeyScope(t, db, key.ID, []uint{node1.ID, node2.ID}, false, key.ExpiresAt)
	beforeKey := readRotationKey(t, db, key.ID)
	beforeNode1 := readRotationNode(t, db, node1.ID)
	beforeNode2 := readRotationNode(t, db, node2.ID)
	session := seedRotationSession(t, db)
	router := newSSHKeyRotationTestRouter(db, session, "admin", true)

	body := rotationRequestBody(t, candidatePrivate, "auto", "rotated-key")
	respCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		respCh <- serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), body)
	}()
	waitRotationSSHAuth(t, first, 1)
	waitRotationSSHAuth(t, second, 1)
	releaseRotationSSHServer(first)
	releaseRotationSSHServer(second)
	resp := <-respCh
	if resp.Code != http.StatusOK {
		t.Fatalf("successful rotation status=%d", resp.Code)
	}
	if first.nonCandidateAttempts.Load() != 0 || second.nonCandidateAttempts.Load() != 0 {
		t.Fatalf("rotation attempted a non-candidate credential: first=%d second=%d", first.nonCandidateAttempts.Load(), second.nonCandidateAttempts.Load())
	}
	if got := resp.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("rotation response cache policy=%q", got)
	}
	result := decodeRotationResponse(t, resp)
	if result.Status != "saved" || result.Reason != "" || len(result.Results) != 2 {
		t.Fatalf("successful rotation returned an unexpected response")
	}
	for _, item := range result.Results {
		if item.Status != "verified" || item.ErrorCode != "" {
			t.Fatalf("successful rotation returned an unexpected node result")
		}
	}
	if result.PublicKeyFingerprint != ssh.FingerprintSHA256(rotationSignerPublicKey(t, candidatePrivate)) {
		t.Fatalf("saved public fingerprint=%q", result.PublicKeyFingerprint)
	}
	stored := readRotationKey(t, db, key.ID)
	if stored.PrivateKey != candidatePrivate || stored.Name != "rotated-key" {
		t.Fatalf("saved candidate not persisted: name=%q private_equal=%v", stored.Name, stored.PrivateKey == candidatePrivate)
	}
	if stored.Username != beforeKey.Username || stored.KeyType != sshutil.SSHKeyTypeRSA || stored.AllowedPurposes != beforeKey.AllowedPurposes || stored.AllowedNodeIDs != beforeKey.AllowedNodeIDs || stored.AllowedNodeTags != beforeKey.AllowedNodeTags || stored.Disabled != beforeKey.Disabled || stored.ExpiresAt == nil || beforeKey.ExpiresAt == nil || !stored.ExpiresAt.Equal(*beforeKey.ExpiresAt) || !stored.CreatedAt.Equal(beforeKey.CreatedAt) || stored.LastUsedAt != nil {
		t.Fatalf("rotation changed protected key metadata")
	}
	afterNode1 := readRotationNode(t, db, node1.ID)
	afterNode2 := readRotationNode(t, db, node2.ID)
	if afterNode1.Status != beforeNode1.Status || afterNode1.ConnectionLatency != beforeNode1.ConnectionLatency || afterNode2.Status != beforeNode2.Status || afterNode2.ConnectionLatency != beforeNode2.ConnectionLatency {
		t.Fatalf("rotation must not update node health state")
	}
	assertRotationAuditSafe(t, db, key.ID, resp.Body.String(), oldPrivate, candidatePrivate, first.address, second.address, rotationTestPrivateMarker)
}

func rotationSignerPublicKey(t *testing.T, privateKey string) ssh.PublicKey {
	t.Helper()
	parsed, err := ssh.ParseRawPrivateKey([]byte(privateKey))
	if err != nil {
		t.Fatalf("parse rotation key for fingerprint: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(parsed)
	if err != nil {
		t.Fatalf("sign rotation key for fingerprint: %v", err)
	}
	return signer.PublicKey()
}

func TestSSHKeyRotationZeroNodeIsNeutralSuccess(t *testing.T) {
	db := openSSHKeyRotationTestDB(t, "sqlite")
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	key := seedRotationKey(t, db, oldPrivate)
	setRotationKeyScope(t, db, key.ID, nil, false, key.ExpiresAt)
	session := seedRotationSession(t, db)
	router := newSSHKeyRotationTestRouter(db, session, "admin", true)

	resp := serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "zero-node-rotated"))
	if resp.Code != http.StatusOK {
		t.Fatalf("zero-node rotation status=%d", resp.Code)
	}
	result := decodeRotationResponse(t, resp)
	if result.Status != "saved" || result.Reason != "" || len(result.Results) != 0 {
		t.Fatalf("zero-node rotation returned an unexpected response")
	}
	stored := readRotationKey(t, db, key.ID)
	if stored.PrivateKey != candidatePrivate || stored.Name != "zero-node-rotated" {
		t.Fatalf("zero-node candidate not saved")
	}
}

func TestSSHKeyRotationFailureReturnsEveryNodeAndOrdinaryPutStillWorksOffline(t *testing.T) {
	db := openSSHKeyRotationTestDB(t, "sqlite")
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	accepted := startRotationSSHServer(t, candidatePrivate, true, false)
	rejected := startRotationSSHServer(t, candidatePrivate, false, false)
	configureRotationKnownHosts(t, accepted, rejected)
	key := seedRotationKey(t, db, oldPrivate)
	acceptedHost, acceptedPortText, _ := net.SplitHostPort(accepted.address)
	acceptedPort, _ := strconv.Atoi(acceptedPortText)
	rejectedHost, rejectedPortText, _ := net.SplitHostPort(rejected.address)
	rejectedPort, _ := strconv.Atoi(rejectedPortText)
	node1 := seedRotationNode(t, db, key.ID, "rotation-online-node", acceptedHost, acceptedPort, false)
	node2 := seedRotationNode(t, db, key.ID, "rotation-archived-node", rejectedHost, rejectedPort, true)
	setRotationKeyScope(t, db, key.ID, []uint{node1.ID, node2.ID}, false, key.ExpiresAt)
	session := seedRotationSession(t, db)
	router := newSSHKeyRotationTestRouter(db, session, "admin", true)
	resp := serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "should-not-save"))
	if resp.Code != http.StatusOK {
		t.Fatalf("failed rotation status=%d", resp.Code)
	}
	result := decodeRotationResponse(t, resp)
	if result.Status != "not_saved" || result.Reason != "validation_failed" || len(result.Results) != 2 {
		t.Fatalf("failed rotation returned an unexpected response")
	}
	byID := make(map[uint]rotationTestNodeResult, len(result.Results))
	for _, item := range result.Results {
		byID[item.NodeID] = item
	}
	if byID[node1.ID].Status != "verified" || byID[node2.ID].Status != "failed" || byID[node2.ID].ErrorCode == "" {
		t.Fatalf("rotation did not report both verified and failed nodes")
	}
	assertRotationNotSaved(t, db, key.ID, oldPrivate, key.Name)

	updateRouter := newSSHKeyRotationUpdateRouter(db, session)
	updateBody := []byte(fmt.Sprintf(`{"name":"offline-edited","username":"root","key_type":"auto","private_key":%q}`, oldPrivate))
	updateResp := serveRotationJSON(t, updateRouter, http.MethodPut, fmt.Sprintf("/ssh-keys/%d", key.ID), updateBody)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("ordinary offline PUT should remain available: status=%d", updateResp.Code)
	}
	updated := readRotationKey(t, db, key.ID)
	if updated.Name != "offline-edited" || updated.PrivateKey != oldPrivate {
		t.Fatalf("ordinary PUT did not preserve its existing behavior")
	}
	assertRotationAuditSafe(t, db, key.ID, resp.Body.String(), oldPrivate, candidatePrivate, "FAKE_REMOTE_ROTATION_AUTH_FAILURE_FOR_TEST_ONLY", rejected.address)
}

func TestSSHKeyRotationRejectsDisabledExpiredAndOutOfScopeBeforeDial(t *testing.T) {
	for _, tc := range []struct {
		name     string
		disabled bool
		expires  *time.Time
		scope    string
		purpose  string
		wantCode string
	}{
		{name: "disabled", disabled: true, wantCode: "scope_denied"},
		{name: "expired", expires: new(time.Now().UTC().Add(-time.Minute)), wantCode: "scope_denied"},
		{name: "purpose denied", purpose: sshutil.PurposeNodeTest, wantCode: "scope_denied"},
		{name: "out of scope", scope: "999999", wantCode: "scope_denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openSSHKeyRotationTestDB(t, "sqlite")
			oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
			candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
			server := startRotationSSHServer(t, candidatePrivate, true, false)
			configureRotationKnownHosts(t, server)
			key := seedRotationKey(t, db, oldPrivate)
			host, portText, _ := net.SplitHostPort(server.address)
			port, _ := strconv.Atoi(portText)
			node := seedRotationNode(t, db, key.ID, "scope-node", host, port, false)
			if tc.scope != "" {
				if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).Updates(map[string]any{"allowed_purposes": sshutil.PurposeSSHKeyTest, "allowed_node_ids": tc.scope, "disabled": tc.disabled, "expires_at": tc.expires}).Error; err != nil {
					t.Fatalf("set out-of-scope fixture: %v", err)
				}
			} else {
				setRotationKeyScope(t, db, key.ID, []uint{node.ID}, tc.disabled, tc.expires)
			}
			if tc.purpose != "" {
				if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).Update("allowed_purposes", tc.purpose).Error; err != nil {
					t.Fatalf("set purpose scope fixture: %v", err)
				}
			}
			session := seedRotationSession(t, db)
			router := newSSHKeyRotationTestRouter(db, session, "admin", true)
			resp := serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "scope-rejected"))
			if resp.Code != http.StatusOK {
				t.Fatalf("scope rejection status=%d", resp.Code)
			}
			result := decodeRotationResponse(t, resp)
			if result.Status != "not_saved" || result.Reason != "scope_blocked" || len(result.Results) != 1 || result.Results[0].Status != "failed" || result.Results[0].ErrorCode != tc.wantCode {
				t.Fatalf("scope rejection returned an unexpected response")
			}
			if got := server.authAttempts.Load(); got != 0 {
				t.Fatalf("scope rejection must not dial SSH, auth attempts=%d", got)
			}
			assertRotationNotSaved(t, db, key.ID, oldPrivate, key.Name)
		})
	}
}

func TestSSHKeyRotationRejectsUnknownAndMismatchedHostsWithoutAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mismatch  bool
		errorCode string
	}{
		{name: "unknown", errorCode: "ssh_host_key_unknown"},
		{name: "mismatch", mismatch: true, errorCode: "ssh_host_key_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openSSHKeyRotationTestDB(t, "sqlite")
			oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
			candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
			server := startRotationSSHServer(t, candidatePrivate, true, false)
			knownHostsPath := configureRotationKnownHosts(t, server)
			if tc.mismatch {
				conflictingPublic, _, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatalf("generate conflicting host key: %v", err)
				}
				conflictingKey, err := ssh.NewPublicKey(conflictingPublic)
				if err != nil {
					t.Fatalf("build conflicting host key: %v", err)
				}
				content := []byte(knownhosts.Line([]string{knownhosts.Normalize(server.address)}, conflictingKey) + "\n")
				if err := os.WriteFile(knownHostsPath, content, 0o600); err != nil {
					t.Fatalf("write conflicting known_hosts: %v", err)
				}
			} else if err := os.WriteFile(knownHostsPath, nil, 0o600); err != nil {
				t.Fatalf("clear known_hosts for unknown host case: %v", err)
			}
			before, err := os.ReadFile(knownHostsPath)
			if err != nil {
				t.Fatalf("read known_hosts before rotation: %v", err)
			}
			t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
			t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "true")
			key := seedRotationKey(t, db, oldPrivate)
			host, portText, _ := net.SplitHostPort(server.address)
			port, _ := strconv.Atoi(portText)
			node := seedRotationNode(t, db, key.ID, "host-trust-node", host, port, false)
			setRotationKeyScope(t, db, key.ID, []uint{node.ID}, false, key.ExpiresAt)
			session := seedRotationSession(t, db)
			router := newSSHKeyRotationTestRouter(db, session, "admin", true)
			resp := serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "host-trust"))
			if resp.Code != http.StatusOK {
				t.Fatalf("host trust failure status=%d", resp.Code)
			}
			result := decodeRotationResponse(t, resp)
			if result.Status != "not_saved" || result.Reason != "validation_failed" || len(result.Results) != 1 || result.Results[0].Status != "failed" || result.Results[0].ErrorCode != tc.errorCode {
				t.Fatalf("host trust failure returned an unexpected response")
			}
			if got := server.authAttempts.Load(); got != 0 {
				t.Fatalf("host trust failure must happen before user authentication, got %d attempts", got)
			}
			after, err := os.ReadFile(knownHostsPath)
			if err != nil {
				t.Fatalf("read known_hosts after rotation: %v", err)
			}
			if string(after) != string(before) {
				t.Fatalf("rotation must not modify known_hosts")
			}
			assertRotationNotSaved(t, db, key.ID, oldPrivate, key.Name)
		})
	}
}

func TestSSHKeyRotationTrustUnavailableDoesNotSave(t *testing.T) {
	db := openSSHKeyRotationTestDB(t, "sqlite")
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	server := startRotationSSHServer(t, candidatePrivate, true, false)
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "true")
	t.Setenv("SSH_KNOWN_HOSTS_PATH", filepath.Join(t.TempDir(), "missing", "known_hosts"))
	key := seedRotationKey(t, db, oldPrivate)
	host, portText, _ := net.SplitHostPort(server.address)
	port, _ := strconv.Atoi(portText)
	node := seedRotationNode(t, db, key.ID, "trust-unavailable-node", host, port, false)
	setRotationKeyScope(t, db, key.ID, []uint{node.ID}, false, key.ExpiresAt)
	session := seedRotationSession(t, db)
	router := newSSHKeyRotationTestRouter(db, session, "admin", true)
	resp := serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "trust-unavailable"))
	if resp.Code != http.StatusOK {
		t.Fatalf("trust unavailable status=%d", resp.Code)
	}
	result := decodeRotationResponse(t, resp)
	if result.Status != "not_saved" || result.Reason != "trust_unavailable" || len(result.Results) != 1 || result.Results[0].Status != "unknown" || result.Results[0].ErrorCode != "not_checked" {
		t.Fatalf("trust unavailable returned an unexpected response")
	}
	if got := server.authAttempts.Load(); got != 0 {
		t.Fatalf("trust unavailable must not dial SSH, auth attempts=%d", got)
	}
	assertRotationNotSaved(t, db, key.ID, oldPrivate, key.Name)
}

func runRotationExpiryCase(t *testing.T, engine string) {
	t.Helper()
	db := openSSHKeyRotationTestDB(t, engine)
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	server := startRotationSSHServer(t, candidatePrivate, true, true)
	configureRotationKnownHosts(t, server)
	key := seedRotationKey(t, db, oldPrivate)
	expiration := time.Now().UTC().Add(3 * time.Second)
	if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).Update("expires_at", expiration).Error; err != nil {
		t.Fatalf("set expiring rotation key: %v", err)
	}
	host, portText, _ := net.SplitHostPort(server.address)
	port, _ := strconv.Atoi(portText)
	node := seedRotationNode(t, db, key.ID, "expiring-node", host, port, false)
	setRotationKeyScope(t, db, key.ID, []uint{node.ID}, false, &expiration)
	session := seedRotationSession(t, db)
	router := newSSHKeyRotationTestRouter(db, session, "admin", true)
	respCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		respCh <- serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "expiring"))
	}()
	waitRotationSSHAuth(t, server, 1)
	timer := time.NewTimer(time.Until(expiration) + 100*time.Millisecond)
	<-timer.C
	releaseRotationSSHServer(server)
	resp := <-respCh
	if resp.Code != http.StatusOK {
		t.Fatalf("expiry-crossing rotation status=%d", resp.Code)
	}
	result := decodeRotationResponse(t, resp)
	if result.Status != "not_saved" || result.Reason != "scope_blocked" || len(result.Results) != 1 || result.Results[0].Status != "verified" || result.Results[0].ErrorCode != "" {
		t.Fatalf("expiry-crossing rotation returned an unexpected response")
	}
	assertRotationNotSaved(t, db, key.ID, oldPrivate, key.Name)
	assertRotationAuditSafe(t, db, key.ID, resp.Body.String(), oldPrivate, candidatePrivate, server.address)
}

func runRotationConflictCase(t *testing.T, engine string, mutate func(*gorm.DB, model.SSHKey, model.Node)) {
	t.Helper()
	db := openSSHKeyRotationTestDB(t, engine)
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	server := startRotationSSHServer(t, candidatePrivate, true, true)
	configureRotationKnownHosts(t, server)
	key := seedRotationKey(t, db, oldPrivate)
	host, portText, _ := net.SplitHostPort(server.address)
	port, _ := strconv.Atoi(portText)
	node := seedRotationNode(t, db, key.ID, "conflict-node", host, port, false)
	setRotationKeyScope(t, db, key.ID, []uint{node.ID}, false, key.ExpiresAt)
	session := seedRotationSession(t, db)
	router := newSSHKeyRotationTestRouter(db, session, "admin", true)
	respCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		respCh <- serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "conflict-candidate"))
	}()
	waitRotationSSHAuth(t, server, 1)
	mutate(db, key, node)
	releaseRotationSSHServer(server)
	resp := <-respCh
	if resp.Code != http.StatusOK {
		t.Fatalf("conflict rotation status=%d", resp.Code)
	}
	result := decodeRotationResponse(t, resp)
	if result.Status != "not_saved" || result.Reason != "conflict" {
		t.Fatalf("conflict rotation returned an unexpected response")
	}
	assertRotationPrivatePreserved(t, db, key.ID, oldPrivate)
	assertRotationAuditSafe(t, db, key.ID, resp.Body.String(), oldPrivate, candidatePrivate, server.address)
}

func TestSSHKeyRotationRejectsKeyNodeAndInventoryDrift(t *testing.T) {
	t.Run("key changed", func(t *testing.T) {
		runRotationConflictCase(t, "sqlite", func(db *gorm.DB, key model.SSHKey, _ model.Node) {
			if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).Updates(map[string]any{"name": "externally-renamed"}).Error; err != nil {
				t.Fatalf("change key during validation: %v", err)
			}
			updated := readRotationKey(t, db, key.ID)
			if updated.Name != "externally-renamed" {
				t.Fatalf("external key update was not retained before commit recheck: %q", updated.Name)
			}
		})
	})
	t.Run("key expires during validation", func(t *testing.T) {
		runRotationExpiryCase(t, "sqlite")
	})
	t.Run("node endpoint changed", func(t *testing.T) {
		runRotationConflictCase(t, "sqlite", func(db *gorm.DB, _ model.SSHKey, node model.Node) {
			if err := db.Model(&model.Node{}).Where("id = ?", node.ID).Updates(map[string]any{
				"name":      "externally-renamed-node",
				"host":      "198.51.100.77",
				"username":  "other-user",
				"auth_type": "password",
				"tags":      "externally-changed",
				"archived":  true,
			}).Error; err != nil {
				t.Fatalf("change node during validation: %v", err)
			}
		})
	})
	t.Run("node deleted", func(t *testing.T) {
		runRotationConflictCase(t, "sqlite", func(db *gorm.DB, _ model.SSHKey, node model.Node) {
			if err := db.Delete(&model.Node{}, node.ID).Error; err != nil {
				t.Fatalf("delete node during validation: %v", err)
			}
		})
	})
	t.Run("node rebound", func(t *testing.T) {
		runRotationConflictCase(t, "sqlite", func(db *gorm.DB, _ model.SSHKey, node model.Node) {
			other := seedRotationKeyNamed(t, db, buildSSHKeyPrivateKeyForHandlerTest(t), "rebind-target-key")
			if err := db.Model(&model.Node{}).Where("id = ?", node.ID).Update("ssh_key_id", other.ID).Error; err != nil {
				t.Fatalf("rebind node during validation: %v", err)
			}
		})
	})
}

func TestSSHKeyRotationRejectsPhantomNodeInsert(t *testing.T) {
	runRotationConflictCase(t, "sqlite", func(db *gorm.DB, key model.SSHKey, node model.Node) {
		phantom := model.Node{
			Name:      "phantom-node",
			Host:      node.Host,
			Port:      node.Port,
			Username:  "root",
			AuthType:  "key",
			SSHKeyID:  new(key.ID),
			Tags:      "rotation,phantom",
			BackupDir: "phantom-node",
		}
		if err := db.Create(&phantom).Error; err != nil {
			t.Fatalf("insert phantom node during validation: %v", err)
		}
	})
}

func TestSSHKeyRotationIgnoresUsageAndHealthDrift(t *testing.T) {
	db := openSSHKeyRotationTestDB(t, "sqlite")
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	server := startRotationSSHServer(t, candidatePrivate, true, true)
	configureRotationKnownHosts(t, server)
	key := seedRotationKey(t, db, oldPrivate)
	host, portText, _ := net.SplitHostPort(server.address)
	port, _ := strconv.Atoi(portText)
	node := seedRotationNode(t, db, key.ID, "usage-health-node", host, port, false)
	setRotationKeyScope(t, db, key.ID, []uint{node.ID}, false, key.ExpiresAt)
	session := seedRotationSession(t, db)
	router := newSSHKeyRotationTestRouter(db, session, "admin", true)
	respCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		respCh <- serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "usage-health-rotated"))
	}()
	waitRotationSSHAuth(t, server, 1)
	usedAt := time.Now().UTC().Add(-30 * time.Second)
	if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).Update("last_used_at", usedAt).Error; err != nil {
		t.Fatalf("update external last_used_at: %v", err)
	}
	seenAt := time.Now().UTC()
	if err := db.Model(&model.Node{}).Where("id = ?", node.ID).Updates(map[string]any{
		"status":             "online",
		"connection_latency": 321,
		"last_seen_at":       seenAt,
	}).Error; err != nil {
		t.Fatalf("update external node health: %v", err)
	}
	releaseRotationSSHServer(server)
	resp := <-respCh
	if resp.Code != http.StatusOK {
		t.Fatalf("usage/health drift status=%d", resp.Code)
	}
	result := decodeRotationResponse(t, resp)
	if result.Status != "saved" {
		t.Fatalf("usage/health changes must not conflict")
	}
	storedKey := readRotationKey(t, db, key.ID)
	if storedKey.PrivateKey != candidatePrivate || storedKey.LastUsedAt == nil || !storedKey.LastUsedAt.Equal(usedAt) {
		t.Fatalf("rotation must preserve LastUsedAt while replacing key")
	}
	storedNode := readRotationNode(t, db, node.ID)
	if storedNode.Status != "online" || storedNode.ConnectionLatency != 321 || storedNode.LastSeenAt == nil || !storedNode.LastSeenAt.Equal(seenAt) {
		t.Fatalf("rotation must preserve external node health state")
	}
	assertRotationAuditSafe(t, db, key.ID, resp.Body.String(), oldPrivate, candidatePrivate, server.address)
}

func TestSSHKeyRotationRejectsMissingOrInvalidSession(t *testing.T) {
	db := openSSHKeyRotationTestDB(t, "sqlite")
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	key := seedRotationKey(t, db, oldPrivate)
	setRotationKeyScope(t, db, key.ID, nil, false, key.ExpiresAt)
	session := seedRotationSession(t, db)

	missingBindingRouter := newSSHKeyRotationTestRouter(db, session, "admin", false)
	missingResp := serveRotationJSON(t, missingBindingRouter, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "missing-binding"))
	if missingResp.Code != http.StatusUnauthorized {
		t.Fatalf("missing binding must be unauthorized, got %d", missingResp.Code)
	}
	expiredSession := session
	expiredSession.Binding.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	expiredRouter := newSSHKeyRotationTestRouter(db, expiredSession, "admin", true)
	expiredResp := serveRotationJSON(t, expiredRouter, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "expired-session"))
	if expiredResp.Code != http.StatusUnauthorized {
		t.Fatalf("expired binding must be unauthorized, got %d", expiredResp.Code)
	}
	operatorRouter := newSSHKeyRotationTestRouter(db, session, "operator", true)
	operatorResp := serveRotationJSON(t, operatorRouter, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "operator"))
	if operatorResp.Code != http.StatusForbidden {
		t.Fatalf("operator rotation must be forbidden, got %d", operatorResp.Code)
	}
	notFoundRouter := newSSHKeyRotationTestRouter(db, session, "admin", true)
	notFoundResp := serveRotationJSON(t, notFoundRouter, http.MethodPost, "/ssh-keys/999999/rotate", rotationRequestBody(t, candidatePrivate, "auto", "missing-key"))
	if notFoundResp.Code != http.StatusNotFound {
		t.Fatalf("missing key must be 404, got %d", notFoundResp.Code)
	}
}

func TestSSHKeyRotationRejectsMalformedBoundedBodies(t *testing.T) {
	db := openSSHKeyRotationTestDB(t, "sqlite")
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	key := seedRotationKey(t, db, oldPrivate)
	setRotationKeyScope(t, db, key.ID, nil, false, key.ExpiresAt)
	session := seedRotationSession(t, db)
	router := newSSHKeyRotationTestRouter(db, session, "admin", true)
	cases := []struct {
		name       string
		body       []byte
		wantStatus int
	}{
		{name: "unknown field", body: []byte(fmt.Sprintf(`{"private_key":%q,"node_ids":[1]}`, candidatePrivate)), wantStatus: http.StatusBadRequest},
		{name: "multiple JSON documents", body: append(rotationRequestBody(t, candidatePrivate, "auto", ""), []byte(`{}`)...), wantStatus: http.StatusBadRequest},
		{name: "invalid key", body: []byte(`{"private_key":"not-a-private-key"}`), wantStatus: http.StatusBadRequest},
		{name: "type mismatch", body: []byte(`{"private_key":123}`), wantStatus: http.StatusBadRequest},
		{name: "missing private key", body: []byte(`{"name":"missing"}`), wantStatus: http.StatusBadRequest},
		{name: "blank name", body: []byte(fmt.Sprintf(`{"private_key":%q,"name":"   "}`, candidatePrivate)), wantStatus: http.StatusBadRequest},
		{name: "empty name", body: []byte(fmt.Sprintf(`{"private_key":%q,"name":""}`, candidatePrivate)), wantStatus: http.StatusBadRequest},
		{name: "null key type", body: []byte(fmt.Sprintf(`{"private_key":%q,"key_type":null}`, candidatePrivate)), wantStatus: http.StatusBadRequest},
		{name: "oversize", body: append(rotationRequestBody(t, candidatePrivate, "auto", ""), bytes.Repeat([]byte(" "), 1<<20)...), wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), tc.body)
			if resp.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d", resp.Code, tc.wantStatus)
			}
			if strings.Contains(resp.Body.String(), candidatePrivate) || strings.Contains(resp.Body.String(), rotationTestPrivateMarker) {
				t.Fatalf("malformed response leaked sensitive test material")
			}
		})
	}
	oversizeBody := append(rotationRequestBody(t, candidatePrivate, "auto", ""), bytes.Repeat([]byte(" "), 1<<20)...)
	unknownLengthReq := rotationJSONRequest(t, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), oversizeBody)
	unknownLengthReq.ContentLength = -1
	unknownLengthResp := httptest.NewRecorder()
	router.ServeHTTP(unknownLengthResp, unknownLengthReq)
	if unknownLengthResp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("unknown-length oversize status=%d", unknownLengthResp.Code)
	}
	assertRotationNotSaved(t, db, key.ID, oldPrivate, key.Name)
	assertRotationAuditSafe(t, db, key.ID, unknownLengthResp.Body.String(), candidatePrivate, rotationTestPrivateMarker)
}

type rotationBodyReadNotifier struct {
	io.ReadCloser
	started  chan struct{}
	notified atomic.Bool
}

func (body *rotationBodyReadNotifier) Read(p []byte) (int, error) {
	if body.notified.CompareAndSwap(false, true) {
		close(body.started)
	}
	return body.ReadCloser.Read(p)
}

func TestSSHKeyRotationStalledBodyReadHonorsRequestDeadline(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cancelOnRead bool
	}{
		{name: "deadline expiry"},
		{name: "request cancellation", cancelOnRead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openSSHKeyRotationTestDB(t, "sqlite")
			oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
			key := seedRotationKey(t, db, oldPrivate)
			session := seedRotationSession(t, db)
			router := newSSHKeyRotationTestRouter(db, session, "admin", true)
			done := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requestCtx, cancel := context.WithTimeout(req.Context(), 200*time.Millisecond)
				defer cancel()
				var cancelWatcherDone chan struct{}
				var handlerDone chan struct{}
				if tc.cancelOnRead {
					readStarted := make(chan struct{})
					req.Body = &rotationBodyReadNotifier{ReadCloser: req.Body, started: readStarted}
					cancelWatcherDone = make(chan struct{})
					handlerDone = make(chan struct{})
					go func() {
						defer close(cancelWatcherDone)
						select {
						case <-readStarted:
							cancel()
						case <-handlerDone:
						}
					}()
				}
				req = req.WithContext(requestCtx)
				router.ServeHTTP(w, req)
				if handlerDone != nil {
					close(handlerDone)
					<-cancelWatcherDone
				}
				close(done)
			}))
			defer server.Close()

			conn, err := net.Dial("tcp", server.Listener.Addr().String())
			if err != nil {
				t.Fatalf("dial stalled-body test server: %v", err)
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			path := fmt.Sprintf("/ssh-keys/%d/rotate", key.ID)
			if _, err := fmt.Fprintf(conn,
				"POST %s HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n",
				path,
			); err != nil {
				t.Fatalf("write stalled-body request headers: %v", err)
			}
			chunk := `{"private_key":"`
			if _, err := fmt.Fprintf(conn, "%x\r\n%s\r\n", len(chunk), chunk); err != nil {
				t.Fatalf("write stalled-body request chunk: %v", err)
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("stalled rotation handler did not return after request deadline/cancellation")
			}
			assertRotationNotSaved(t, db, key.ID, oldPrivate, key.Name)
		})
	}
}

func TestSSHKeyRotationRejectsInventoryOverLimit(t *testing.T) {
	db := openSSHKeyRotationTestDB(t, "sqlite")
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	key := seedRotationKey(t, db, oldPrivate)
	ids := make([]uint, 0, 257)
	for i := range 257 {
		node := seedRotationNode(t, db, key.ID, fmt.Sprintf("inventory-node-%03d", i), "127.0.0.1", 1, i%2 == 0)
		ids = append(ids, node.ID)
	}
	setRotationKeyScope(t, db, key.ID, ids, false, key.ExpiresAt)
	session := seedRotationSession(t, db)
	router := newSSHKeyRotationTestRouter(db, session, "admin", true)
	resp := serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "too-many"))
	if resp.Code != http.StatusOK {
		t.Fatalf("inventory-limit status=%d", resp.Code)
	}
	result := decodeRotationResponse(t, resp)
	if result.Status != "not_saved" || result.Reason != "inventory_limit" || len(result.Results) != 257 {
		t.Fatalf("inventory-limit returned an unexpected response")
	}
	for _, item := range result.Results {
		if item.Status != "unknown" || item.ErrorCode != "not_checked" {
			t.Fatalf("inventory-limit returned a checked node")
		}
	}
	assertRotationNotSaved(t, db, key.ID, oldPrivate, key.Name)
}

func TestSSHKeyRotationRejectsSessionInvalidationDuringValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *gorm.DB, rotationTestSession)
	}{
		{name: "role downgrade", mutate: func(t *testing.T, db *gorm.DB, session rotationTestSession) {
			if err := db.Model(&model.User{}).Where("id = ?", session.User.ID).Update("role", "operator").Error; err != nil {
				t.Fatalf("downgrade session user: %v", err)
			}
		}},
		{name: "token version", mutate: func(t *testing.T, db *gorm.DB, session rotationTestSession) {
			if err := db.Model(&model.User{}).Where("id = ?", session.User.ID).Update("token_version", session.Binding.TokenVersion+1).Error; err != nil {
				t.Fatalf("change token version: %v", err)
			}
		}},
		{name: "revoked", mutate: func(t *testing.T, db *gorm.DB, session rotationTestSession) {
			if err := db.Create(&model.TokenRevocation{TokenHash: "jti:" + session.Binding.JTI, UserID: session.User.ID, ExpiresAt: time.Now().UTC().Add(time.Hour)}).Error; err != nil {
				t.Fatalf("revoke rotation session: %v", err)
			}
		}},
		{name: "deleted user", mutate: func(t *testing.T, db *gorm.DB, session rotationTestSession) {
			if err := db.Delete(&model.User{}, session.User.ID).Error; err != nil {
				t.Fatalf("delete rotation session user: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openSSHKeyRotationTestDB(t, "sqlite")
			oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
			candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
			server := startRotationSSHServer(t, candidatePrivate, true, true)
			configureRotationKnownHosts(t, server)
			key := seedRotationKey(t, db, oldPrivate)
			host, portText, _ := net.SplitHostPort(server.address)
			port, _ := strconv.Atoi(portText)
			node := seedRotationNode(t, db, key.ID, "session-invalidation-node", host, port, false)
			setRotationKeyScope(t, db, key.ID, []uint{node.ID}, false, key.ExpiresAt)
			session := seedRotationSession(t, db)
			router := newSSHKeyRotationTestRouter(db, session, "admin", true)
			respCh := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				respCh <- serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "session-invalidated"))
			}()
			waitRotationSSHAuth(t, server, 1)
			tc.mutate(t, db, session)
			releaseRotationSSHServer(server)
			resp := <-respCh
			if resp.Code != http.StatusUnauthorized {
				t.Fatalf("session invalidation status=%d", resp.Code)
			}
			assertRotationNotSaved(t, db, key.ID, oldPrivate, key.Name)
			assertRotationAuditSafe(t, db, key.ID, resp.Body.String(), oldPrivate, candidatePrivate)
			assertRotationAuditReason(t, db, key.ID, "request_rejected")
		})
	}
}

func TestSSHKeyRotationCancellationStopsWorkersAndDoesNotSave(t *testing.T) {
	db := openSSHKeyRotationTestDB(t, "sqlite")
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	server := startRotationSSHServer(t, candidatePrivate, true, true)
	configureRotationKnownHosts(t, server)
	key := seedRotationKey(t, db, oldPrivate)
	host, portText, _ := net.SplitHostPort(server.address)
	port, _ := strconv.Atoi(portText)
	node := seedRotationNode(t, db, key.ID, "cancel-node", host, port, false)
	setRotationKeyScope(t, db, key.ID, []uint{node.ID}, false, key.ExpiresAt)
	session := seedRotationSession(t, db)
	router := newSSHKeyRotationTestRouter(db, session, "admin", true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := rotationJSONRequest(t, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "cancelled"))
	req = req.WithContext(ctx)
	resp := httptest.NewRecorder()
	respCh := make(chan struct{})
	go func() {
		router.ServeHTTP(resp, req)
		close(respCh)
	}()
	waitRotationSSHAuth(t, server, 1)
	cancel()
	releaseRotationSSHServer(server)
	select {
	case <-respCh:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled rotation did not return")
	}
	result := decodeRotationResponse(t, resp)
	if result.Status != "not_saved" || result.Reason != "validation_timeout" || len(result.Results) != 1 || result.Results[0].Status != "unknown" {
		t.Fatalf("cancelled rotation returned an unexpected response")
	}
	assertRotationAuditSafe(t, db, key.ID, resp.Body.String(), oldPrivate, candidatePrivate)
	if strings.Contains(resp.Body.String(), `"status":"saved"`) {
		t.Fatalf("cancelled rotation must not claim saved")
	}
}
func TestSSHKeyRotationDeadlineStopsHandshake(t *testing.T) {
	db := openSSHKeyRotationTestDB(t, "sqlite")
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	server := startRotationSSHServer(t, candidatePrivate, true, true)
	configureRotationKnownHosts(t, server)
	key := seedRotationKey(t, db, oldPrivate)
	host, portText, _ := net.SplitHostPort(server.address)
	port, _ := strconv.Atoi(portText)
	node := seedRotationNode(t, db, key.ID, "deadline-node", host, port, false)
	setRotationKeyScope(t, db, key.ID, []uint{node.ID}, false, key.ExpiresAt)
	session := seedRotationSession(t, db)
	router := newSSHKeyRotationTestRouter(db, session, "admin", true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := rotationJSONRequest(t, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "deadline"))
	req = req.WithContext(ctx)
	resp := httptest.NewRecorder()
	respCh := make(chan struct{})
	go func() {
		router.ServeHTTP(resp, req)
		close(respCh)
	}()
	waitRotationSSHAuth(t, server, 1)
	<-ctx.Done()
	releaseRotationSSHServer(server)
	select {
	case <-respCh:
	case <-time.After(5 * time.Second):
		t.Fatal("deadline-limited rotation did not return")
	}
	result := decodeRotationResponse(t, resp)
	if result.Status != "not_saved" || result.Reason != "validation_timeout" || len(result.Results) != 1 || result.Results[0].Status != "unknown" || result.Results[0].ErrorCode != "timeout" {
		t.Fatalf("deadline-limited rotation returned an unexpected response")
	}
	assertRotationAuditSafe(t, db, key.ID, resp.Body.String(), oldPrivate, candidatePrivate)
}

func TestSSHKeyRotationSQLiteWriterLockReturnsBusyAndRestoresTimeout(t *testing.T) {
	db, lockDB := openSSHKeyRotationProductionSQLiteDB(t)
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	key := seedRotationKey(t, db, oldPrivate)
	current := readRotationKey(t, db, key.ID)
	candidate, _, err := prepareSSHKeyRotationCandidate(current, sshKeyRotationRequest{
		PrivateKey: candidatePrivate,
		KeyType:    sshutil.SSHKeyTypeAuto,
	})
	if err != nil {
		t.Fatalf("prepare rotation candidate: %v", err)
	}
	session := seedRotationSession(t, db)
	snapshot := buildSSHKeyRotationSnapshot(current, nil)

	lockTx := lockDB.Begin()
	if lockTx.Error != nil {
		t.Fatalf("begin SQLite writer lock: %v", lockTx.Error)
	}
	defer func() { _ = lockTx.Rollback().Error }()
	if err := lockTx.Exec("UPDATE nodes SET id = id WHERE 1 = 0").Error; err != nil {
		t.Fatalf("hold SQLite writer lock: %v", err)
	}

	started := time.Now()
	commitErr := commitSSHKeyRotation(context.Background(), db, session.Binding, snapshot, candidate)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("SQLite writer lock was not bounded: %s", elapsed)
	}
	if !errors.Is(commitErr, errSSHKeyRotationBusy) {
		t.Fatalf("SQLite writer lock error=%v want busy", commitErr)
	}
	assertRotationNotSaved(t, db, key.ID, oldPrivate, key.Name)
	var busyTimeout int64
	if err := db.Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error; err != nil {
		t.Fatalf("read restored SQLite busy timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("SQLite busy timeout=%d want restored production value 5000", busyTimeout)
	}
}

func TestSSHKeyRotationAuditDoesNotExposeRemoteFailure(t *testing.T) {
	db := openSSHKeyRotationTestDB(t, "sqlite")
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	server := startRotationSSHServer(t, candidatePrivate, false, false)
	configureRotationKnownHosts(t, server)
	key := seedRotationKey(t, db, oldPrivate)
	host, portText, _ := net.SplitHostPort(server.address)
	port, _ := strconv.Atoi(portText)
	node := seedRotationNode(t, db, key.ID, "audit-node", host, port, false)
	setRotationKeyScope(t, db, key.ID, []uint{node.ID}, false, key.ExpiresAt)
	session := seedRotationSession(t, db)
	router := newSSHKeyRotationTestRouter(db, session, "admin", true)
	resp := serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "audit-failure"))
	if resp.Code != http.StatusOK {
		t.Fatalf("audit failure status=%d", resp.Code)
	}
	if strings.Contains(resp.Body.String(), "FAKE_REMOTE_ROTATION_AUTH_FAILURE_FOR_TEST_ONLY") {
		t.Fatalf("rotation response leaked raw SSH error")
	}
	assertRotationAuditSafe(t, db, key.ID, resp.Body.String(), oldPrivate, candidatePrivate, server.address, "FAKE_REMOTE_ROTATION_AUTH_FAILURE_FOR_TEST_ONLY")
}

func runSSHKeyRotationPostgresSnapshotCases(t *testing.T) {
	t.Helper()
	t.Run("key changed", func(t *testing.T) {
		runRotationConflictCase(t, "postgres", func(db *gorm.DB, key model.SSHKey, _ model.Node) {
			if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).Update("name", "postgres-external-key").Error; err != nil {
				t.Fatalf("change PostgreSQL key during validation: %v", err)
			}
		})
	})
	t.Run("key expires during SSH validation", func(t *testing.T) {
		runRotationExpiryCase(t, "postgres")
	})
	t.Run("node endpoint changed", func(t *testing.T) {
		runRotationConflictCase(t, "postgres", func(db *gorm.DB, _ model.SSHKey, node model.Node) {
			if err := db.Model(&model.Node{}).Where("id = ?", node.ID).Updates(map[string]any{
				"name":      "postgres-external-node",
				"host":      "198.51.100.78",
				"username":  "other-user",
				"auth_type": "password",
				"tags":      "postgres,changed",
				"archived":  true,
			}).Error; err != nil {
				t.Fatalf("change PostgreSQL node during validation: %v", err)
			}
		})
	})
	t.Run("node deleted", func(t *testing.T) {
		runRotationConflictCase(t, "postgres", func(db *gorm.DB, _ model.SSHKey, node model.Node) {
			if err := db.Delete(&model.Node{}, node.ID).Error; err != nil {
				t.Fatalf("delete PostgreSQL node during validation: %v", err)
			}
		})
	})
	t.Run("node rebound", func(t *testing.T) {
		runRotationConflictCase(t, "postgres", func(db *gorm.DB, _ model.SSHKey, node model.Node) {
			other := seedRotationKeyNamed(t, db, buildSSHKeyPrivateKeyForHandlerTest(t), "postgres-rebind-target")
			if err := db.Model(&model.Node{}).Where("id = ?", node.ID).Update("ssh_key_id", other.ID).Error; err != nil {
				t.Fatalf("rebind PostgreSQL node during validation: %v", err)
			}
		})
	})
	t.Run("phantom inserted during SSH", func(t *testing.T) {
		runRotationConflictCase(t, "postgres", func(db *gorm.DB, key model.SSHKey, node model.Node) {
			phantom := model.Node{
				Name:      "postgres-validation-phantom",
				Host:      node.Host,
				Port:      node.Port,
				Username:  "root",
				AuthType:  "key",
				SSHKeyID:  new(key.ID),
				Tags:      "postgres,validation-phantom",
				BackupDir: "postgres-validation-phantom",
			}
			if err := db.Create(&phantom).Error; err != nil {
				t.Fatalf("insert PostgreSQL phantom during validation: %v", err)
			}
		})
	})
}

func runSSHKeyRotationPostgresCommitLockBarrier(t *testing.T) {
	t.Helper()
	db := openSSHKeyRotationTestDB(t, "postgres")
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	server := startRotationSSHServer(t, candidatePrivate, true, true)
	configureRotationKnownHosts(t, server)
	key := seedRotationKey(t, db, oldPrivate)
	host, portText, _ := net.SplitHostPort(server.address)
	port, _ := strconv.Atoi(portText)
	node := seedRotationNode(t, db, key.ID, "postgres-commit-barrier-node", host, port, false)
	setRotationKeyScope(t, db, key.ID, []uint{node.ID}, false, key.ExpiresAt)
	session := seedRotationSession(t, db)
	router := newSSHKeyRotationTestRouter(db, session, "admin", true)

	commitEntered := make(chan struct{}, 1)
	releaseCommit := make(chan struct{})
	callbackName := fmt.Sprintf("test:ssh-key-rotation-commit-barrier-%d", time.Now().UnixNano())
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "ssh_keys" {
			return
		}
		select {
		case commitEntered <- struct{}{}:
		default:
		}
		<-releaseCommit
	}); err != nil {
		t.Fatalf("register PostgreSQL commit barrier: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-releaseCommit:
		default:
			close(releaseCommit)
		}
		_ = db.Callback().Update().Remove(callbackName)
	})

	respCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		respCh <- serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "postgres-commit-barrier"))
	}()
	waitRotationSSHAuth(t, server, 1)
	releaseRotationSSHServer(server)
	select {
	case <-commitEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("PostgreSQL rotation did not reach its final key update barrier")
	}

	pool, err := db.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL commit-barrier pool: %v", err)
	}
	phantom := model.Node{
		Name:      "postgres-commit-barrier-phantom",
		Host:      host,
		Port:      port,
		Username:  "root",
		AuthType:  "key",
		SSHKeyID:  new(key.ID),
		Tags:      "postgres,commit-barrier",
		BackupDir: "postgres-commit-barrier-phantom",
	}
	insertDone := make(chan error, 1)
	go func() { insertDone <- db.Create(&phantom).Error }()
	waitForPostgresNodeWaiter(t, pool, 1)
	select {
	case err := <-insertDone:
		t.Fatalf("phantom insert completed before rotation commit: %v", err)
	default:
	}
	beforeAttempts := server.authAttempts.Load()
	close(releaseCommit)
	resp := <-respCh
	insertErr := <-insertDone
	if insertErr != nil {
		t.Fatalf("phantom insert after rotation commit: %v", insertErr)
	}
	if server.authAttempts.Load() != beforeAttempts {
		t.Fatalf("rotation commit must not perform additional SSH authentication")
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("PostgreSQL commit-barrier rotation status=%d", resp.Code)
	}
	result := decodeRotationResponse(t, resp)
	if result.Status != "saved" {
		t.Fatalf("PostgreSQL commit-barrier rotation did not save")
	}
	stored := readRotationKey(t, db, key.ID)
	if stored.PrivateKey != candidatePrivate || stored.Name != "postgres-commit-barrier" {
		t.Fatalf("PostgreSQL commit-barrier candidate was not persisted")
	}
	assertRotationAuditSafe(t, db, key.ID, resp.Body.String(), oldPrivate, candidatePrivate, server.address)
}

func waitForPostgresNodeWaiter(t *testing.T, db *sql.DB, wantAtLeast int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		err := db.QueryRow(`SELECT count(*) FROM pg_locks l JOIN pg_class c ON c.oid = l.relation WHERE c.relname = 'nodes' AND NOT l.granted`).Scan(&count)
		if err == nil && count >= wantAtLeast {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for at least %d PostgreSQL nodes lock waiters", wantAtLeast)
}

func TestSSHKeyRotationPostgres(t *testing.T) {
	db := openSSHKeyRotationTestDB(t, "postgres")
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	server := startRotationSSHServer(t, candidatePrivate, true, true)
	configureRotationKnownHosts(t, server)
	key := seedRotationKey(t, db, oldPrivate)
	host, portText, _ := net.SplitHostPort(server.address)
	port, _ := strconv.Atoi(portText)
	node := seedRotationNode(t, db, key.ID, "postgres-rotation-node", host, port, false)
	setRotationKeyScope(t, db, key.ID, []uint{node.ID}, false, key.ExpiresAt)
	session := seedRotationSession(t, db)
	router := newSSHKeyRotationTestRouter(db, session, "admin", true)
	beforeNode := readRotationNode(t, db, node.ID)

	respCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		respCh <- serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "postgres-rotated"))
	}()
	waitRotationSSHAuth(t, server, 1)

	pool, err := db.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL pool for lock proof: %v", err)
	}
	lockTx, err := pool.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin PostgreSQL lock holder: %v", err)
	}
	t.Cleanup(func() { _ = lockTx.Rollback() })
	if _, err := lockTx.ExecContext(context.Background(), `LOCK TABLE nodes IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		_ = lockTx.Rollback()
		t.Fatalf("acquire PostgreSQL nodes lock holder: %v", err)
	}
	beforeAttempts := server.authAttempts.Load()
	releaseRotationSSHServer(server)
	waitForPostgresNodeWaiter(t, pool, 1)

	phantom := model.Node{
		Name:      "postgres-phantom-node",
		Host:      host,
		Port:      port,
		Username:  "root",
		AuthType:  "key",
		SSHKeyID:  new(key.ID),
		Tags:      "postgres,phantom",
		BackupDir: "postgres-phantom-node",
	}
	insertDone := make(chan error, 1)
	go func() { insertDone <- db.Create(&phantom).Error }()
	waitForPostgresNodeWaiter(t, pool, 2)
	select {
	case err := <-insertDone:
		t.Fatalf("phantom insert completed while rotation transaction was waiting/lock holder active: %v", err)
	default:
	}
	if got := server.authAttempts.Load(); got != beforeAttempts {
		t.Fatalf("no SSH auth should occur while commit lock is pending: before=%d after=%d", beforeAttempts, got)
	}
	if err := lockTx.Rollback(); err != nil {
		t.Fatalf("release PostgreSQL lock holder: %v", err)
	}
	resp := <-respCh
	insertErr := <-insertDone
	if insertErr != nil {
		t.Fatalf("phantom insert after lock release: %v", insertErr)
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("PostgreSQL rotation status=%d", resp.Code)
	}
	result := decodeRotationResponse(t, resp)
	if result.Status != "saved" {
		t.Fatalf("PostgreSQL rotation did not commit before later phantom insert")
	}
	stored := readRotationKey(t, db, key.ID)
	if stored.PrivateKey != candidatePrivate || stored.Name != "postgres-rotated" {
		t.Fatalf("PostgreSQL candidate not persisted")
	}
	afterNode := readRotationNode(t, db, node.ID)
	if afterNode.Status != beforeNode.Status || afterNode.ConnectionLatency != beforeNode.ConnectionLatency || afterNode.LastSeenAt == nil || beforeNode.LastSeenAt == nil || !afterNode.LastSeenAt.Equal(*beforeNode.LastSeenAt) {
		t.Fatalf("PostgreSQL rotation changed health/LastSeenAt state")
	}
	assertRotationAuditSafe(t, db, key.ID, resp.Body.String(), oldPrivate, candidatePrivate, server.address)
	t.Run("snapshot conflicts", runSSHKeyRotationPostgresSnapshotCases)
	t.Run("commit lock holds through update", runSSHKeyRotationPostgresCommitLockBarrier)
	t.Run("busy timeout", func(t *testing.T) {
		busyDB := openSSHKeyRotationTestDB(t, "postgres")
		oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
		candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
		server := startRotationSSHServer(t, candidatePrivate, true, true)
		configureRotationKnownHosts(t, server)
		key := seedRotationKey(t, busyDB, oldPrivate)
		host, portText, _ := net.SplitHostPort(server.address)
		port, _ := strconv.Atoi(portText)
		node := seedRotationNode(t, busyDB, key.ID, "postgres-busy-node", host, port, false)
		setRotationKeyScope(t, busyDB, key.ID, []uint{node.ID}, false, key.ExpiresAt)
		session := seedRotationSession(t, busyDB)
		router := newSSHKeyRotationTestRouter(busyDB, session, "admin", true)
		respCh := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			respCh <- serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "postgres-busy"))
		}()
		waitRotationSSHAuth(t, server, 1)

		pool, err := busyDB.DB()
		if err != nil {
			t.Fatalf("get PostgreSQL busy-test pool: %v", err)
		}
		lockTx, err := pool.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin PostgreSQL busy-test lock holder: %v", err)
		}
		t.Cleanup(func() { _ = lockTx.Rollback() })
		if _, err := lockTx.ExecContext(context.Background(), `LOCK TABLE nodes IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			_ = lockTx.Rollback()
			t.Fatalf("acquire PostgreSQL busy-test lock holder: %v", err)
		}
		beforeAttempts := server.authAttempts.Load()
		releaseRotationSSHServer(server)
		waitForPostgresNodeWaiter(t, pool, 1)

		var resp *httptest.ResponseRecorder
		select {
		case resp = <-respCh:
		case <-time.After(5 * time.Second):
			t.Fatal("PostgreSQL busy rotation did not return after lock timeout")
		}
		if resp.Code != http.StatusOK {
			t.Fatalf("PostgreSQL busy rotation status=%d", resp.Code)
		}
		result := decodeRotationResponse(t, resp)
		if result.Status != "not_saved" || result.Reason != "busy" {
			t.Fatalf("PostgreSQL busy rotation returned an unexpected outcome")
		}
		if server.authAttempts.Load() != beforeAttempts {
			t.Fatalf("PostgreSQL busy commit must not perform SSH authentication")
		}
		assertRotationNotSaved(t, busyDB, key.ID, oldPrivate, key.Name)
		assertRotationAuditSafe(t, busyDB, key.ID, resp.Body.String(), oldPrivate, candidatePrivate, server.address)
		if err := lockTx.Rollback(); err != nil {
			t.Fatalf("release PostgreSQL busy-test lock holder: %v", err)
		}
	})
}

func TestSSHKeyRotationResponseResultsAreSorted(t *testing.T) {
	db := openSSHKeyRotationTestDB(t, "sqlite")
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	first := startRotationSSHServer(t, candidatePrivate, false, false)
	second := startRotationSSHServer(t, candidatePrivate, false, false)
	configureRotationKnownHosts(t, first, second)
	key := seedRotationKey(t, db, oldPrivate)
	firstHost, firstPortText, _ := net.SplitHostPort(first.address)
	firstPort, _ := strconv.Atoi(firstPortText)
	secondHost, secondPortText, _ := net.SplitHostPort(second.address)
	secondPort, _ := strconv.Atoi(secondPortText)
	nodes := []model.Node{
		seedRotationNode(t, db, key.ID, "sort-node-a", firstHost, firstPort, false),
		seedRotationNode(t, db, key.ID, "sort-node-b", secondHost, secondPort, false),
	}
	setRotationKeyScope(t, db, key.ID, []uint{nodes[0].ID, nodes[1].ID}, false, key.ExpiresAt)
	session := seedRotationSession(t, db)
	router := newSSHKeyRotationTestRouter(db, session, "admin", true)
	resp := serveRotationJSON(t, router, http.MethodPost, fmt.Sprintf("/ssh-keys/%d/rotate", key.ID), rotationRequestBody(t, candidatePrivate, "auto", "sorted"))
	if resp.Code != http.StatusOK {
		t.Fatalf("sorted results status=%d", resp.Code)
	}
	result := decodeRotationResponse(t, resp)
	ids := make([]uint, len(result.Results))
	for i, item := range result.Results {
		ids[i] = item.NodeID
	}
	if !sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] < ids[j] }) {
		t.Fatalf("rotation results must be ID sorted: %v", ids)
	}
}
