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
	"gorm.io/gorm"
)

type rawTerminalSSHServer struct {
	addr             string
	shellReady       <-chan struct{}
	inputSeen        <-chan struct{}
	connectionClosed <-chan struct{}
}

func startRawTerminalSSHServerWithOptions(t *testing.T, password string, writeOutput bool) rawTerminalSSHServer {
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
	inputSeen := make(chan struct{}, 16)
	connectionClosed := make(chan struct{})
	var shellReadyOnce sync.Once
	var connectionClosedOnce sync.Once

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
							buf := make([]byte, 4096)
							for {
								n, readErr := channel.Read(buf)
								if n > 0 {
									select {
									case inputSeen <- struct{}{}:
									default:
									}
								}
								if readErr != nil {
									return
								}
							}
						}()
						go func() {
							defer func() { _ = channel.Close() }()
							for request := range channelRequests {
								switch request.Type {
								case "pty-req", "shell", "window-change":
									_ = request.Reply(true, nil)
									if request.Type == "shell" {
										shellReadyOnce.Do(func() { close(shellReady) })
										if writeOutput {
											go func() {
												payload := []byte(strings.Repeat("terminal-output-", 2048))
												for {
													if _, writeErr := channel.Write(payload); writeErr != nil {
														return
													}
												}
											}()
										}
									}
								default:
									_ = request.Reply(false, nil)
								}
							}
						}()
					}
				}()
				_ = serverConn.Wait()
				connectionClosedOnce.Do(func() { close(connectionClosed) })
			}()
		}
	}()

	t.Cleanup(func() {
		_ = listener.Close()
	})
	return rawTerminalSSHServer{
		addr:             listener.Addr().String(),
		shellReady:       shellReady,
		inputSeen:        inputSeen,
		connectionClosed: connectionClosed,
	}
}

func waitForTerminalSessions(t *testing.T, handler *TerminalHandler, want int) {
	waitForTerminalSessionsWithin(t, handler, want, 2*time.Second)
}

func waitForTerminalSessionsWithin(t *testing.T, handler *TerminalHandler, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
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

func waitForActiveTerminalSession(t *testing.T, handler *TerminalHandler) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		handler.mu.Lock()
		active := false
		for sessionID := range handler.sessions {
			if strings.HasPrefix(sessionID, "term-") && !strings.HasPrefix(sessionID, "term-pending-") {
				active = true
				break
			}
		}
		handler.mu.Unlock()
		if active {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("terminal active session 未建立")
}

type rawTerminalFixture struct {
	handler          *TerminalHandler
	server           *httptest.Server
	token            string
	proof            string
	nodeID           uint
	userID           uint
	jwtManager       *auth.JWTManager
	shellReady       <-chan struct{}
	inputSeen        <-chan struct{}
	connectionClosed <-chan struct{}
	handlerDone      <-chan struct{}
}

func setupRawTerminalFixture(t *testing.T) rawTerminalFixture {
	return setupRawTerminalFixtureWithOutput(t, false)
}

func setupRawTerminalFixtureWithOutput(t *testing.T, writeOutput bool) rawTerminalFixture {
	return setupRawTerminalFixtureWithTTL(t, writeOutput, time.Hour)
}

func setupRawTerminalFixtureWithTTL(t *testing.T, writeOutput bool, ttl time.Duration) rawTerminalFixture {
	t.Helper()
	db := openSSHKeyHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.TokenRevocation{}, &model.CredentialAccessGrant{}, &model.CredentialAuditEvent{}, &model.AuditLog{}); err != nil {
		t.Fatalf("初始化 terminal websocket 测试表失败: %v", err)
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")

	const password = "terminal-test-password"
	sshServer := startRawTerminalSSHServerWithOptions(t, password, writeOutput)
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
	jwtManager := auth.NewJWTManager("terminal-test-secret", ttl)
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
	handlerDone := make(chan struct{}, 16)
	router := gin.New()
	router.GET("/ws/terminal", func(c *gin.Context) {
		defer func() {
			select {
			case handlerDone <- struct{}{}:
			default:
			}
		}()
		handler.ServeTerminal(c)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return rawTerminalFixture{
		handler:          handler,
		server:           server,
		token:            token,
		proof:            proof,
		nodeID:           node.ID,
		userID:           user.ID,
		jwtManager:       jwtManager,
		shellReady:       sshServer.shellReady,
		inputSeen:        sshServer.inputSeen,
		connectionClosed: sshServer.connectionClosed,
		handlerDone:      handlerDone,
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
func authenticateRawTerminal(t *testing.T, fixture rawTerminalFixture) *websocket.Conn {
	return authenticateRawTerminalWithCredentials(t, fixture, fixture.token, fixture.proof, 1)
}

func authenticateRawTerminalWithCredentials(t *testing.T, fixture rawTerminalFixture, token, proof string, wantSessions int) *websocket.Conn {
	t.Helper()
	conn := dialRawTerminal(t, fixture)
	if err := conn.WriteJSON(terminalAuthMessage{
		Type:        "auth",
		Token:       token,
		StepUpProof: proof,
	}); err != nil {
		t.Fatalf("发送 terminal auth frame 失败: %v", err)
	}
	select {
	case <-fixture.shellReady:
	case <-time.After(2 * time.Second):
		t.Fatal("测试 SSH 服务未收到 terminal shell 请求")
	}
	waitForTerminalSessions(t, fixture.handler, wantSessions)
	return conn
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

func TestTerminalSessionRevocationClosesAndStopsInput(t *testing.T) {
	fixture := setupRawTerminalFixture(t)
	conn := authenticateRawTerminal(t, fixture)
	if err := conn.WriteMessage(websocket.TextMessage, []byte("before-revoke")); err != nil {
		t.Fatalf("发送撤销前终端输入失败: %v", err)
	}
	select {
	case <-fixture.inputSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("SSH 服务未收到撤销前终端输入")
	}
	for {
		select {
		case <-fixture.inputSeen:
		default:
			goto drained
		}
	}
drained:

	claims, err := fixture.jwtManager.ParseToken(fixture.token)
	if err != nil {
		t.Fatalf("解析 terminal token 失败: %v", err)
	}
	if err := fixture.jwtManager.RevokeSession(claims.ID, fixture.userID, claims.ExpiresAt.Time); err != nil {
		t.Fatalf("撤销 terminal token 失败: %v", err)
	}
	revokedAt := time.Now()
	waitForTerminalSessions(t, fixture.handler, 0)
	if elapsed := time.Since(revokedAt); elapsed > 1500*time.Millisecond {
		t.Fatalf("撤销后终端关闭超过轮询上限: %s", elapsed)
	}

	// The websocket may accept the write at the kernel boundary while the
	// close propagates; the SSH-side observation is the security assertion.
	_ = conn.WriteMessage(websocket.TextMessage, []byte("after-revoke"))
	select {
	case <-fixture.inputSeen:
		t.Fatal("撤销后的终端输入仍被转发到 SSH")
	case <-time.After(terminalValidationInterval + terminalValidationTimeout + 100*time.Millisecond):
	}
}

func TestTerminalSessionIdentityChangesCloseOnlyAffectedSession(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gorm.DB, uint) error
	}{
		{
			name: "token-version",
			mutate: func(db *gorm.DB, userID uint) error {
				return db.Model(&model.User{}).Where("id = ?", userID).Update("token_version", 1).Error
			},
		},
		{
			name: "role",
			mutate: func(db *gorm.DB, userID uint) error {
				return db.Model(&model.User{}).Where("id = ?", userID).Update("role", "operator").Error
			},
		},
		{
			name: "user-removal",
			mutate: func(db *gorm.DB, userID uint) error {
				return db.Delete(&model.User{}, userID).Error
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setupRawTerminalFixture(t)
			_ = authenticateRawTerminal(t, fixture)
			var mutateErr error
			for range 20 {
				mutateErr = tc.mutate(fixture.handler.db, fixture.userID)
				if mutateErr == nil || !strings.Contains(mutateErr.Error(), "locked") {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if mutateErr != nil {
				t.Fatalf("修改 terminal 身份状态失败: %v", mutateErr)
			}
			waitForTerminalSessions(t, fixture.handler, 0)
		})
	}
}

func TestTerminalSlowReaderOutputClosesBoundedlyAfterRevocation(t *testing.T) {
	fixture := setupRawTerminalFixtureWithOutput(t, true)
	_ = authenticateRawTerminal(t, fixture)

	claims, err := fixture.jwtManager.ParseToken(fixture.token)
	if err != nil {
		t.Fatalf("解析 terminal token 失败: %v", err)
	}
	if err := fixture.jwtManager.RevokeSession(claims.ID, fixture.userID, claims.ExpiresAt.Time); err != nil {
		t.Fatalf("撤销 terminal token 失败: %v", err)
	}
	started := time.Now()
	waitForTerminalSessions(t, fixture.handler, 0)
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("慢接收端关闭超过有界上限: %s", elapsed)
	}
}

func TestTerminalCloseAuditStallDoesNotBlockHandler(t *testing.T) {
	fixture := setupRawTerminalFixture(t)

	var stalled atomic.Bool
	var auditAttempts atomic.Int32
	auditStarted := make(chan struct{})
	var auditStartedOnce sync.Once
	callbackName := fmt.Sprintf("test:terminal_close_audit_stall_%d", time.Now().UnixNano())
	if err := fixture.handler.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if !stalled.Load() || tx.Statement == nil || tx.Statement.Context == nil ||
			tx.Statement.Schema == nil || tx.Statement.Schema.Table != "audit_logs" {
			return
		}
		auditAttempts.Add(1)
		auditStartedOnce.Do(func() { close(auditStarted) })
		<-tx.Statement.Context.Done()
		_ = tx.AddError(tx.Statement.Context.Err())
	}); err != nil {
		t.Fatalf("注册终端 close 审计阻塞回调失败: %v", err)
	}
	t.Cleanup(func() {
		stalled.Store(false)
		_ = fixture.handler.db.Callback().Query().Remove(callbackName)
	})

	conn := authenticateRawTerminal(t, fixture)
	waitForActiveTerminalSession(t, fixture.handler)
	stalled.Store(true)
	claims, err := fixture.jwtManager.ParseToken(fixture.token)
	if err != nil {
		t.Fatalf("解析 terminal token 失败: %v", err)
	}
	if err := fixture.jwtManager.RevokeSession(claims.ID, fixture.userID, claims.ExpiresAt.Time); err != nil {
		t.Fatalf("撤销 terminal token 失败: %v", err)
	}

	closeAuditStartedAt := time.Now()
	select {
	case <-auditStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("终端 close 审计阻塞回调未启动")
	}
	select {
	case <-fixture.handlerDone:
	case <-time.After(terminalCloseAuditTimeout + terminalCloseControlTimeout + 500*time.Millisecond):
		t.Fatal("close 审计阻塞时 ServeTerminal 未在有界期限内返回")
	}
	if elapsed := time.Since(closeAuditStartedAt); elapsed > terminalCloseAuditTimeout+terminalCloseControlTimeout+500*time.Millisecond {
		t.Fatalf("close 审计阻塞时 handler 返回超出预算: %s", elapsed)
	}
	waitForTerminalSessions(t, fixture.handler, 0)

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("close 审计阻塞时 websocket 应已关闭")
	}
	select {
	case <-fixture.connectionClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("close 审计阻塞时 SSH 连接未关闭")
	}
	if got := auditAttempts.Load(); got != 1 {
		t.Fatalf("close 审计应只尝试一次，实际 %d 次", got)
	}
}

func TestTerminalSessionJWTExpiryClosesShell(t *testing.T) {
	fixture := setupRawTerminalFixtureWithTTL(t, false, 3*time.Second)
	_ = authenticateRawTerminal(t, fixture)
	expiryWaitStarted := time.Now()
	waitForTerminalSessionsWithin(t, fixture.handler, 0, 5*time.Second)
	if elapsed := time.Since(expiryWaitStarted); elapsed > 5*time.Second {
		t.Fatalf("JWT 到期后终端未在有界期限内关闭: %s", elapsed)
	}
}

func TestTerminalSessionAuthorityFailureClosesShell(t *testing.T) {
	fixture := setupRawTerminalFixture(t)
	_ = authenticateRawTerminal(t, fixture)
	sqlDB, err := fixture.handler.db.DB()
	if err != nil {
		t.Fatalf("获取 terminal 测试 SQL 连接失败: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("关闭 terminal 测试 SQL 连接失败: %v", err)
	}
	waitForTerminalSessionsWithin(t, fixture.handler, 0, 2*time.Second)
}

func TestTerminalSessionRevocationDoesNotAffectOtherUser(t *testing.T) {
	fixture := setupRawTerminalFixture(t)

	user2 := model.User{
		Username:     "terminal-admin-other",
		PasswordHash: "test-password-hash",
		Role:         "admin",
		TOTPEnabled:  true,
	}
	if err := fixture.handler.db.Create(&user2).Error; err != nil {
		t.Fatalf("创建第二个 terminal 测试用户失败: %v", err)
	}
	token2, err := fixture.jwtManager.GenerateToken(user2)
	if err != nil {
		t.Fatalf("生成第二个 terminal token 失败: %v", err)
	}
	proof2, expiresAt2, err := fixture.jwtManager.GenerateStepUpToken(user2, auth.StepUpActionTerminalOpen)
	if err != nil {
		t.Fatalf("生成第二个 terminal step-up proof 失败: %v", err)
	}
	now := time.Now().UTC()
	if err := fixture.handler.db.Create(&model.CredentialAccessGrant{
		RequesterUserID:   user2.ID,
		RequesterUsername: user2.Username,
		RequesterRole:     user2.Role,
		Action:            CredentialGrantActionTerminalOpen,
		Purpose:           sshutil.PurposeTerminal,
		NodeID:            credentialaudit.PtrUint(fixture.nodeID),
		Reason:            "other-user terminal lifecycle",
		Status:            CredentialGrantStatusActive,
		RequestedAt:       now,
		ApprovedAt:        &now,
		ExpiresAt:         expiresAt2,
	}).Error; err != nil {
		t.Fatalf("创建第二个 terminal 凭据授权失败: %v", err)
	}
	_ = authenticateRawTerminal(t, fixture)
	otherConn := authenticateRawTerminalWithCredentials(t, fixture, token2, proof2, 2)

	firstClaims, err := fixture.jwtManager.ParseToken(fixture.token)
	if err != nil {
		t.Fatalf("解析第一个 terminal token 失败: %v", err)
	}
	if err := fixture.jwtManager.RevokeSession(firstClaims.ID, fixture.userID, firstClaims.ExpiresAt.Time); err != nil {
		t.Fatalf("撤销第一个 terminal token 失败: %v", err)
	}
	waitForTerminalSessionsWithin(t, fixture.handler, 1, 2*time.Second)

	if err := otherConn.WriteMessage(websocket.TextMessage, []byte("other-user-input")); err != nil {
		t.Fatalf("撤销一个用户后其他用户终端不应关闭: %v", err)
	}
	select {
	case <-fixture.inputSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("其他用户终端输入未被转发")
	}

	secondClaims, err := fixture.jwtManager.ParseToken(token2)
	if err != nil {
		t.Fatalf("解析第二个 terminal token 失败: %v", err)
	}
	if err := fixture.jwtManager.RevokeSession(secondClaims.ID, user2.ID, secondClaims.ExpiresAt.Time); err != nil {
		t.Fatalf("清理第二个 terminal token 失败: %v", err)
	}
	waitForTerminalSessionsWithin(t, fixture.handler, 0, 2*time.Second)
}
