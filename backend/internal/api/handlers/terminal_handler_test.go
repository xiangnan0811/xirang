package handlers

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	gossh "golang.org/x/crypto/ssh"
)

type rawTerminalSSHServer struct {
	addr       string
	shellReady <-chan struct{}
}

func startRawTerminalSSHServer(t *testing.T, password string) rawTerminalSSHServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听测试 SSH 服务失败: %v", err)
	}
	_, hostPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("生成测试 SSH 主机密钥失败: %v", err)
	}
	hostSigner, err := gossh.NewSignerFromKey(hostPrivateKey)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("加载测试 SSH 主机密钥失败: %v", err)
	}
	serverConfig := &gossh.ServerConfig{
		PasswordCallback: func(_ gossh.ConnMetadata, supplied []byte) (*gossh.Permissions, error) {
			if string(supplied) != password {
				return nil, fmt.Errorf("test SSH password rejected")
			}
			return nil, nil
		},
	}
	serverConfig.AddHostKey(hostSigner)
	shellReady := make(chan struct{})
	var shellReadyOnce sync.Once

	go func() {
		for {
			rawConn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				serverConn, channels, requests, handshakeErr := gossh.NewServerConn(rawConn, serverConfig)
				if handshakeErr != nil {
					_ = rawConn.Close()
					return
				}
				go gossh.DiscardRequests(requests)
				go func() {
					for newChannel := range channels {
						if newChannel.ChannelType() != "session" {
							_ = newChannel.Reject(gossh.UnknownChannelType, "test server only accepts sessions")
							continue
						}
						channel, channelRequests, channelErr := newChannel.Accept()
						if channelErr != nil {
							continue
						}
						go func() {
							defer func() { _ = channel.Close() }()
							for request := range channelRequests {
								switch request.Type {
								case "pty-req", "shell", "window-change":
									_ = request.Reply(true, nil)
									if request.Type == "shell" {
										shellReadyOnce.Do(func() { close(shellReady) })
									}
								default:
									_ = request.Reply(false, nil)
								}
							}
						}()
					}
				}()
				_ = serverConn.Wait()
			}()
		}
	}()

	t.Cleanup(func() {
		_ = listener.Close()
	})
	return rawTerminalSSHServer{addr: listener.Addr().String(), shellReady: shellReady}
}

func waitForTerminalSessions(t *testing.T, handler *TerminalHandler, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		handler.mu.Lock()
		got := len(handler.sessions)
		handler.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	handler.mu.Lock()
	got := len(handler.sessions)
	handler.mu.Unlock()
	t.Fatalf("terminal session 数量未达到 %d，实际 %d", want, got)
}

type rawTerminalFixture struct {
	handler    *TerminalHandler
	server     *httptest.Server
	token      string
	proof      string
	nodeID     uint
	shellReady <-chan struct{}
}

func setupRawTerminalFixture(t *testing.T) rawTerminalFixture {
	t.Helper()
	db := openSSHKeyHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.CredentialAccessGrant{}, &model.CredentialAuditEvent{}, &model.AuditLog{}); err != nil {
		t.Fatalf("初始化 terminal websocket 测试表失败: %v", err)
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")

	const password = "terminal-test-password"
	sshServer := startRawTerminalSSHServer(t, password)
	host, portText, err := net.SplitHostPort(sshServer.addr)
	if err != nil {
		t.Fatalf("解析测试 SSH 地址失败: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("解析测试 SSH 端口失败: %v", err)
	}

	user := model.User{
		Username:     "terminal-admin",
		PasswordHash: "test-password-hash",
		Role:         "admin",
		TOTPEnabled:  true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建 terminal 测试用户失败: %v", err)
	}
	jwtManager := auth.NewJWTManager("terminal-test-secret", time.Hour)
	token, err := jwtManager.GenerateToken(user)
	if err != nil {
		t.Fatalf("生成 terminal 测试 token 失败: %v", err)
	}
	proof, expiresAt, err := jwtManager.GenerateStepUpToken(user, auth.StepUpActionTerminalOpen)
	if err != nil {
		t.Fatalf("生成 terminal 测试 step-up proof 失败: %v", err)
	}

	node := model.Node{
		Name:      "terminal-websocket-node",
		Host:      host,
		Port:      port,
		Username:  "root",
		AuthType:  "password",
		Password:  password,
		BackupDir: "terminal-websocket-node",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建 terminal 测试节点失败: %v", err)
	}
	now := time.Now().UTC()
	grant := model.CredentialAccessGrant{
		RequesterUserID:   user.ID,
		RequesterUsername: user.Username,
		RequesterRole:     user.Role,
		Action:            CredentialGrantActionTerminalOpen,
		Purpose:           sshutil.PurposeTerminal,
		NodeID:            credentialaudit.PtrUint(node.ID),
		Reason:            "raw websocket frame limit test",
		Status:            CredentialGrantStatusActive,
		RequestedAt:       now,
		ApprovedAt:        &now,
		ExpiresAt:         expiresAt,
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("创建 terminal 测试凭据授权失败: %v", err)
	}

	handler := NewTerminalHandler(db, jwtManager, func(*http.Request) bool { return true })
	router := gin.New()
	router.GET("/ws/terminal", handler.ServeTerminal)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return rawTerminalFixture{
		handler:    handler,
		server:     server,
		token:      token,
		proof:      proof,
		nodeID:     node.ID,
		shellReady: sshServer.shellReady,
	}
}

func dialRawWebsocket(t *testing.T, serverURL, path string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + path
	conn, response, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if response != nil {
			t.Fatalf("websocket 握手失败: %v (status=%s)", err, response.Status)
		}
		t.Fatalf("websocket 握手失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func dialRawTerminal(t *testing.T, fixture rawTerminalFixture) *websocket.Conn {
	t.Helper()
	return dialRawWebsocket(t, fixture.server.URL, fmt.Sprintf("/ws/terminal?node_id=%d", fixture.nodeID))
}

// TestTerminalHandler_ReserveSlotID_RespectsLimit 验证 Wave 2 (PR-C C3) 修复：
// reserveSlotID 在持锁内一并完成 "检查上限 + 占位"，杜绝并发请求绕过 maxTerminalSessions。
//
// 旧实现：先 len() 检查 → 拨 SSH（耗时） → 注册 session。N 个并发请求都能通过
// 第一步检查后，最终注册的 session 数会超过上限。
func TestTerminalHandler_ReserveSlotID_RespectsLimit(t *testing.T) {
	h := &TerminalHandler{
		sessions: make(map[string]context.CancelFunc),
	}

	const concurrent = 100
	var success int32

	var wg sync.WaitGroup
	wg.Add(concurrent)
	for i := 0; i < concurrent; i++ {
		go func() {
			defer wg.Done()
			id := h.reserveSlotID()
			if id != "" {
				atomic.AddInt32(&success, 1)
			}
		}()
	}
	wg.Wait()

	got := atomic.LoadInt32(&success)
	if int(got) != maxTerminalSessions {
		t.Fatalf("reserveSlotID 应严格限制为 %d，实际成功 %d 次", maxTerminalSessions, got)
	}

	// 验证 sessions map 真实大小也 = maxTerminalSessions
	h.mu.Lock()
	mapSize := len(h.sessions)
	h.mu.Unlock()
	if mapSize != maxTerminalSessions {
		t.Fatalf("sessions map 大小应 = %d，实际 %d", maxTerminalSessions, mapSize)
	}
}

// TestTerminalHandler_FreeSlot 验证 freeSlot 释放占位后，新请求能再次成功 reserve。
func TestTerminalHandler_FreeSlot(t *testing.T) {
	h := &TerminalHandler{
		sessions: make(map[string]context.CancelFunc),
	}

	// 占满
	ids := make([]string, 0, maxTerminalSessions)
	for i := 0; i < maxTerminalSessions; i++ {
		id := h.reserveSlotID()
		if id == "" {
			t.Fatalf("第 %d 次 reserveSlotID 应成功", i)
		}
		ids = append(ids, id)
	}

	// 第 N+1 个失败
	if h.reserveSlotID() != "" {
		t.Fatal("已满时 reserveSlotID 应返回空字符串")
	}

	// 释放一个，再次成功
	h.freeSlot(ids[0])
	id := h.reserveSlotID()
	if id == "" {
		t.Fatal("释放后应能再次 reserve")
	}
}

// TestTerminalHandler_PromoteSlot 验证 promoteSlot 把占位 ID 替换为真正 sessionID。
func TestTerminalHandler_PromoteSlot(t *testing.T) {
	h := &TerminalHandler{
		sessions: make(map[string]context.CancelFunc),
	}

	pendingID := h.reserveSlotID()
	if pendingID == "" {
		t.Fatal("reserve 失败")
	}

	cancelCalled := false
	cancel := func() { cancelCalled = true }
	h.promoteSlot(pendingID, "term-real-1", cancel)

	h.mu.Lock()
	_, hasPending := h.sessions[pendingID]
	storedCancel, hasReal := h.sessions["term-real-1"]
	mapSize := len(h.sessions)
	h.mu.Unlock()

	if hasPending {
		t.Error("promote 后旧占位 ID 应被删除")
	}
	if !hasReal {
		t.Error("promote 后新 session ID 应存在")
	}
	if mapSize != 1 {
		t.Errorf("map 大小应 = 1，实际 %d", mapSize)
	}
	if storedCancel == nil {
		t.Fatal("promote 注入的 cancel 应非 nil")
	}
	storedCancel()
	if !cancelCalled {
		t.Fatal("storedCancel 未被调用")
	}
}

func TestTerminalHandlerServeTerminalReturnsEnvelopeWhenFull(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &TerminalHandler{
		sessions: make(map[string]context.CancelFunc),
	}
	for i := 0; i < maxTerminalSessions; i++ {
		if id := h.reserveSlotID(); id == "" {
			t.Fatalf("第 %d 次 reserveSlotID 应成功", i)
		}
	}

	r := gin.New()
	r.GET("/api/v1/ws/terminal", h.ServeTerminal)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ws/terminal", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望状态码 503，实际: %d，响应: %s", w.Code, w.Body.String())
	}
	var envelope Response
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应信封失败: %v", err)
	}
	if envelope.Code != http.StatusServiceUnavailable || envelope.Message != "终端会话数已达上限" {
		t.Fatalf("期望终端限流响应信封，实际: %+v", envelope)
	}
}

func TestServeTerminalRejectsOversizedAuthenticationFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewTerminalHandler(nil, nil, func(*http.Request) bool { return true })
	router := gin.New()
	router.GET("/ws/terminal", handler.ServeTerminal)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	conn := dialRawWebsocket(t, server.URL, "/ws/terminal")
	authFrame := `{"type":"auth","token":"` + strings.Repeat("a", maxTerminalAuthMessageBytes) + `"}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(authFrame)); err != nil {
		t.Fatalf("发送 oversized terminal auth frame 失败: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("oversized terminal auth frame 应关闭 websocket")
	}
	waitForTerminalSessions(t, handler, 0)
}

func TestServeTerminalRejectsOversizedAuthenticatedInput(t *testing.T) {
	fixture := setupRawTerminalFixture(t)
	conn := dialRawTerminal(t, fixture)
	if err := conn.WriteJSON(terminalAuthMessage{
		Type:        "auth",
		Token:       fixture.token,
		StepUpProof: fixture.proof,
	}); err != nil {
		t.Fatalf("发送 terminal auth frame 失败: %v", err)
	}
	select {
	case <-fixture.shellReady:
	case <-time.After(2 * time.Second):
		t.Fatal("测试 SSH 服务未收到 terminal shell 请求")
	}
	waitForTerminalSessions(t, fixture.handler, 1)

	if err := conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", maxTerminalMessageBytes+1))); err != nil {
		t.Fatalf("发送 oversized terminal input frame 失败: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("oversized authenticated terminal input 应关闭 websocket")
	}
	waitForTerminalSessions(t, fixture.handler, 0)
}
