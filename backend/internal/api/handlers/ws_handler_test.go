package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"
	"xirang/backend/internal/ws"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func createWSHandlerTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/ws/logs", nil)
	return ctx, recorder
}

func TestWSHandlerServeWSNilHub(t *testing.T) {
	handler := NewWSHandler(nil, nil, nil)
	ctx, recorder := createWSHandlerTestContext()

	handler.ServeWS(ctx)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望状态码 %d，实际 %d", http.StatusServiceUnavailable, recorder.Code)
	}
	assertServiceUnavailableEnvelope(t, recorder.Body.Bytes())
}

func TestWSHandlerServeWSNilJWTManager(t *testing.T) {
	handler := NewWSHandler(&ws.Hub{}, nil, nil)
	ctx, recorder := createWSHandlerTestContext()

	handler.ServeWS(ctx)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望状态码 %d，实际 %d", http.StatusServiceUnavailable, recorder.Code)
	}
	assertServiceUnavailableEnvelope(t, recorder.Body.Bytes())
}

func TestWSHandlerDurableRevocationIsSharedAcrossManagers(t *testing.T) {
	db := openRealtimeAuthTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.TokenRevocation{}); err != nil {
		t.Fatalf("初始化 websocket 认证测试表失败: %v", err)
	}
	user := model.User{
		Username:     "shared-realtime-operator",
		PasswordHash: "test-password-hash",
		Role:         "admin",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建 websocket 认证测试用户失败: %v", err)
	}

	managerA := auth.NewJWTManager("shared-realtime-test-secret", time.Hour)
	managerB := auth.NewJWTManager("shared-realtime-test-secret", time.Hour)
	managerA.SetDB(db)
	managerB.SetDB(db)
	revokedToken, err := managerA.GenerateToken(user)
	if err != nil {
		t.Fatalf("生成待撤销 websocket token 失败: %v", err)
	}
	unrelatedToken, err := managerA.GenerateToken(user)
	if err != nil {
		t.Fatalf("生成独立 websocket token 失败: %v", err)
	}
	revokedClaims, err := managerA.ParseToken(revokedToken)
	if err != nil {
		t.Fatalf("解析待撤销 websocket token 失败: %v", err)
	}
	if err := managerA.RevokeSession(revokedClaims.ID, user.ID, revokedClaims.ExpiresAt.Time); err != nil {
		t.Fatalf("撤销 websocket token 失败: %v", err)
	}

	hub := ws.NewHub(db, nil, true)
	runCtx, cancelRun := context.WithCancel(context.Background())
	go hub.Run(runCtx)
	t.Cleanup(cancelRun)
	router := gin.New()
	handler := NewWSHandler(hub, managerB, db)
	router.GET("/ws/logs", handler.ServeWS)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	dial := func() *websocket.Conn {
		t.Helper()
		url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/logs"
		conn, response, dialErr := websocket.DefaultDialer.Dial(url, nil)
		if dialErr != nil {
			if response != nil {
				t.Fatalf("websocket 连接失败: %v (status=%s)", dialErr, response.Status)
			}
			t.Fatalf("websocket 连接失败: %v", dialErr)
		}
		return conn
	}
	readClosed := func(conn *websocket.Conn) {
		t.Helper()
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, readErr := conn.ReadMessage(); readErr == nil {
			t.Fatal("撤销的 websocket token 应在认证后关闭")
		}
	}

	revokedConn := dial()
	if err := revokedConn.WriteJSON(map[string]string{"type": "auth", "token": revokedToken}); err != nil {
		t.Fatalf("发送撤销 websocket auth 失败: %v", err)
	}
	readClosed(revokedConn)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && hub.ClientCount() != 0 {
		time.Sleep(time.Millisecond)
	}
	if got := hub.ClientCount(); got != 0 {
		t.Fatalf("撤销的 websocket token 不应注册客户端，实际 %d", got)
	}

	unrelatedConn := dial()
	t.Cleanup(func() { _ = unrelatedConn.Close() })
	if err := unrelatedConn.WriteJSON(map[string]string{"type": "auth", "token": unrelatedToken}); err != nil {
		t.Fatalf("发送独立 websocket auth 失败: %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && hub.ClientCount() != 1 {
		time.Sleep(time.Millisecond)
	}
	if got := hub.ClientCount(); got != 1 {
		t.Fatalf("独立 websocket token 应注册客户端，实际 %d", got)
	}
}

func assertServiceUnavailableEnvelope(t *testing.T, body []byte) {
	t.Helper()
	var envelope Response
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("解析响应信封失败: %v", err)
	}
	if envelope.Code != http.StatusServiceUnavailable || envelope.Message != "websocket 服务不可用" {
		t.Fatalf("期望 websocket 服务不可用信封，实际: %+v", envelope)
	}
}
