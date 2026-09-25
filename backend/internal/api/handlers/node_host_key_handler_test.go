package handlers

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xirang/backend/internal/model"
	nodePkg "xirang/backend/internal/node"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

type hostKeyHandlerFixture struct {
	server         *dockerSSHTestServer
	node           model.Node
	router         *gin.Engine
	db             *gorm.DB
	knownHostsPath string
}

func newHostKeyHandlerFixture(t *testing.T) *hostKeyHandlerFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	server := startDockerSSHTestServer(t, func(_ string, channel ssh.Channel, _ <-chan struct{}) {
		writeDockerSSHResult(channel, []byte("40G 10G\n"), 0)
	})
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "true")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	knownHostsPath := filepath.Join(t.TempDir(), "ssh", "known_hosts")
	t.Setenv("SSH_KNOWN_HOSTS_PATH", knownHostsPath)

	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.SSHKey{}, &model.Alert{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	node := seedDockerSSHHandlerNode(t, db, server)
	handler := NewNodeHandler(db, nil, nodePkg.NewNodeService(gormrepo.NewNodeRepository(db)))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("userID", uint(1))
		c.Set("username", "admin")
		c.Set("role", "admin")
		c.Next()
	})
	router.POST("/nodes/:id/test-connection", handler.TestConnection)
	router.POST("/nodes/:id/trust-host-key", handler.TrustHostKey)
	return &hostKeyHandlerFixture{server: server, node: node, router: router, db: db, knownHostsPath: knownHostsPath}
}

type hostKeyTestConnectionData struct {
	OK        bool   `json:"ok"`
	Message   string `json:"message"`
	ErrorCode string `json:"error_code"`
	HostKey   *struct {
		Algorithm         string `json:"algorithm"`
		FingerprintSHA256 string `json:"fingerprint_sha256"`
	} `json:"host_key"`
}

func (f *hostKeyHandlerFixture) testConnection(t *testing.T) hostKeyTestConnectionData {
	t.Helper()
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/nodes/%d/test-connection", f.node.ID), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("test-connection status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data hostKeyTestConnectionData `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode test-connection: %v body=%s", err, response.Body.String())
	}
	return envelope.Data
}

type hostKeyTrustResponse struct {
	Message string `json:"message"`
	Data    struct {
		Trusted           bool   `json:"trusted"`
		AlreadyTrusted    bool   `json:"already_trusted"`
		Algorithm         string `json:"algorithm"`
		FingerprintSHA256 string `json:"fingerprint_sha256"`
		ErrorCode         string `json:"error_code"`
	} `json:"data"`
}

func (f *hostKeyHandlerFixture) trust(t *testing.T, fingerprint string) (int, hostKeyTrustResponse) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"fingerprint_sha256": fingerprint})
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/nodes/%d/trust-host-key", f.node.ID), strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	var decoded hostKeyTrustResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode trust response: %v body=%s", err, response.Body.String())
	}
	return response.Code, decoded
}

func readKnownHosts(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(content)
}

func TestNodeHostKeyUnknownThenTrustThenConnect(t *testing.T) {
	f := newHostKeyHandlerFixture(t)
	expectedFingerprint := ssh.FingerprintSHA256(f.server.hostKey)

	result := f.testConnection(t)
	if result.OK || result.ErrorCode != "ssh_host_key_unknown" || result.HostKey == nil {
		t.Fatalf("unknown host must return structured host key failure, got %+v", result)
	}
	if result.HostKey.FingerprintSHA256 != expectedFingerprint || result.HostKey.Algorithm != f.server.hostKey.Type() {
		t.Fatalf("host_key=%+v want fingerprint %s", *result.HostKey, expectedFingerprint)
	}
	if strings.Contains(result.Message, f.node.Host) || strings.Contains(result.Message, f.server.addr) {
		t.Fatalf("message must not leak host: %q", result.Message)
	}
	attemptsBeforeTrust := f.server.authAttempts.Load()
	if attemptsBeforeTrust != 0 {
		t.Fatalf("rejected host key must stop before authentication, got %d attempts", attemptsBeforeTrust)
	}

	code, trusted := f.trust(t, expectedFingerprint)
	if code != http.StatusOK || !trusted.Data.Trusted || trusted.Data.AlreadyTrusted || trusted.Data.FingerprintSHA256 != expectedFingerprint {
		t.Fatalf("trust status=%d response=%+v", code, trusted)
	}
	if attempts := f.server.authAttempts.Load(); attempts != 0 {
		t.Fatalf("trust probe must not send credentials, got %d auth attempts", attempts)
	}

	if connected := f.testConnection(t); !connected.OK {
		t.Fatalf("connection after trust must succeed, got %+v", connected)
	}

	code, repeated := f.trust(t, expectedFingerprint)
	if code != http.StatusOK || !repeated.Data.AlreadyTrusted {
		t.Fatalf("repeat trust status=%d response=%+v", code, repeated)
	}
	if lines := strings.Count(strings.TrimSpace(readKnownHosts(t, f.knownHostsPath)), "\n") + 1; lines != 1 {
		t.Fatalf("known_hosts must hold exactly one entry, got %d", lines)
	}

	var events []model.CredentialAuditEvent
	if err := f.db.Where("action = ?", "node.host_key.trust").Order("id").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Outcome != "success" {
		t.Fatalf("trust must be audited per request, got %+v", events)
	}
	if !strings.Contains(events[0].Metadata, `"stage":"host_key_trust"`) || !strings.Contains(events[0].Metadata, `"host_key_algorithm":"`+f.server.hostKey.Type()+`"`) || !strings.Contains(events[0].Metadata, `"host_key_fingerprint":"`+expectedFingerprint+`"`) {
		t.Fatalf("trust audit metadata missing fields: %s", events[0].Metadata)
	}
}

func TestNodeHostKeyTrustRejectsChangedFingerprint(t *testing.T) {
	f := newHostKeyHandlerFixture(t)

	code, response := f.trust(t, "SHA256:AAAA")
	if code != http.StatusConflict || response.Data.ErrorCode != "ssh_host_key_changed" {
		t.Fatalf("wrong fingerprint status=%d response=%+v", code, response)
	}
	if content := readKnownHosts(t, f.knownHostsPath); content != "" {
		t.Fatalf("known_hosts must stay empty, got %q", content)
	}

	for _, invalid := range []string{"", "  ", "MD5:aa:bb"} {
		if code, _ := f.trust(t, invalid); code != http.StatusBadRequest {
			t.Fatalf("fingerprint %q must be rejected with 400, got %d", invalid, code)
		}
	}
}

func TestNodeHostKeyMismatchHasNoTrustPath(t *testing.T) {
	f := newHostKeyHandlerFixture(t)
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := ssh.NewPublicKey(otherPublic)
	if err != nil {
		t.Fatal(err)
	}
	if err := sshutil.AppendKnownHost(f.knownHostsPath, f.server.addr, otherKey); err != nil {
		t.Fatal(err)
	}
	before := readKnownHosts(t, f.knownHostsPath)

	result := f.testConnection(t)
	if result.OK || result.ErrorCode != "ssh_host_key_mismatch" || result.HostKey == nil || result.HostKey.FingerprintSHA256 != ssh.FingerprintSHA256(f.server.hostKey) {
		t.Fatalf("conflicting known_hosts must return mismatch, got %+v", result)
	}

	code, response := f.trust(t, ssh.FingerprintSHA256(f.server.hostKey))
	if code != http.StatusConflict || response.Data.ErrorCode != "ssh_host_key_mismatch" {
		t.Fatalf("mismatch trust status=%d response=%+v", code, response)
	}
	if after := readKnownHosts(t, f.knownHostsPath); after != before {
		t.Fatalf("known_hosts changed on mismatch: before=%q after=%q", before, after)
	}
	if attempts := f.server.authAttempts.Load(); attempts != 0 {
		t.Fatalf("no credentials may be sent to a mismatched host, got %d", attempts)
	}
}

func TestTerminalDialFailureReasonPointsAtTrustFlowWithinCloseLimit(t *testing.T) {
	key := newHostKeyTestPublicKey(t)
	for _, tc := range []struct {
		err  error
		want string
	}{
		{err: fmt.Errorf("SSH 握手失败: %w", &sshutil.HostKeyError{Kind: sshutil.HostKeyUnknown, Key: key}), want: "测试连接"},
		{err: fmt.Errorf("SSH 握手失败: %w", &sshutil.HostKeyError{Kind: sshutil.HostKeyMismatch, Key: key}), want: "known_hosts 不一致"},
		{err: fmt.Errorf("SSH 连接失败: connection refused"), want: "请检查节点配置"},
	} {
		reason := terminalDialFailureReason(tc.err)
		if !strings.Contains(reason, tc.want) {
			t.Fatalf("reason %q must contain %q", reason, tc.want)
		}
		// RFC 6455 limits the close reason to 123 bytes; longer reasons are dropped.
		if len(reason) > 123 {
			t.Fatalf("close reason %q is %d bytes, exceeds WebSocket limit", reason, len(reason))
		}
	}
}

func TestDockerVolumesSurfacesUnknownHostKeyGuidance(t *testing.T) {
	f := newHostKeyHandlerFixture(t)
	response, done := runDockerHandlerRequest(t, NewDockerHandler(f.db), f.node, context.Background())
	<-done
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Message != (&sshutil.HostKeyError{Kind: sshutil.HostKeyUnknown}).Error() {
		t.Fatalf("docker dial failure must carry host key guidance, got %q", envelope.Message)
	}
}

func newHostKeyTestPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
