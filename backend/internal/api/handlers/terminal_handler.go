package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	gossh "golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

const terminalSessionTimeout = 30 * time.Minute

type TerminalHandler struct {
	db         *gorm.DB
	jwtManager *auth.JWTManager
	upgrader   websocket.Upgrader
	mu         sync.Mutex
	sessions   map[string]context.CancelFunc
}

func NewTerminalHandler(db *gorm.DB, jwtManager *auth.JWTManager, checkOrigin func(*http.Request) bool) *TerminalHandler {
	return &TerminalHandler{
		db:         db,
		jwtManager: jwtManager,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     checkOrigin,
		},
		sessions: make(map[string]context.CancelFunc),
	}
}

type terminalAuthMessage struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

type terminalResizeMessage struct {
	Type string `json:"type"`
	Cols uint32 `json:"cols"`
	Rows uint32 `json:"rows"`
}

func (h *TerminalHandler) ServeTerminal(c *gin.Context) {
	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("warn: terminal: websocket 升级失败: %v", err)
		return
	}

	// 等待认证消息（5 秒超时）
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, rawMsg, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		return
	}

	var authMsg terminalAuthMessage
	if err := json.Unmarshal(rawMsg, &authMsg); err != nil || authMsg.Type != "auth" {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "认证消息格式错误"))
		_ = conn.Close()
		return
	}

	claims, err := h.jwtManager.ParseToken(authMsg.Token)
	if err != nil || claims.Role != "admin" {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "认证失败或权限不足"))
		_ = conn.Close()
		return
	}

	// 解析 node_id 参数
	rawNodeID := c.Query("node_id")
	if rawNodeID == "" {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInvalidFramePayloadData, "缺少 node_id 参数"))
		_ = conn.Close()
		return
	}
	nodeID64, err := strconv.ParseUint(rawNodeID, 10, 64)
	if err != nil {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInvalidFramePayloadData, "node_id 格式无效"))
		_ = conn.Close()
		return
	}

	// 查询节点
	var node model.Node
	if err := h.db.Preload("SSHKey").First(&node, uint(nodeID64)).Error; err != nil {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInvalidFramePayloadData, "节点不存在"))
		_ = conn.Close()
		return
	}

	// 建立 SSH 连接
	authMethods, err := sshutil.BuildSSHAuth(node, h.db)
	if err != nil {
		msg := fmt.Sprintf("构建 SSH 认证失败: %v", err)
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, msg))
		_ = conn.Close()
		return
	}

	hostKeyCallback, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		msg := fmt.Sprintf("解析主机密钥失败: %v", err)
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, msg))
		_ = conn.Close()
		return
	}

	addr := fmt.Sprintf("%s:%d", node.Host, node.Port)
	ctx, cancel := context.WithTimeout(context.Background(), terminalSessionTimeout)

	sshClient, err := sshutil.DialSSH(ctx, addr, node.Username, authMethods, hostKeyCallback)
	if err != nil {
		cancel()
		msg := fmt.Sprintf("SSH 连接失败: %v", err)
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, msg))
		_ = conn.Close()
		return
	}

	session, err := sshClient.NewSession()
	if err != nil {
		cancel()
		sshClient.Close()
		msg := fmt.Sprintf("SSH 会话创建失败: %v", err)
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, msg))
		_ = conn.Close()
		return
	}

	// 请求 PTY
	modes := gossh.TerminalModes{
		gossh.ECHO:          1,
		gossh.TTY_OP_ISPEED: 14400,
		gossh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		cancel()
		session.Close()
		sshClient.Close()
		msg := fmt.Sprintf("请求 PTY 失败: %v", err)
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, msg))
		_ = conn.Close()
		return
	}

	// 连接 SSH 标准 I/O
	sshStdin, err := session.StdinPipe()
	if err != nil {
		cancel()
		session.Close()
		sshClient.Close()
		_ = conn.Close()
		return
	}
	sshStdout, err := session.StdoutPipe()
	if err != nil {
		cancel()
		session.Close()
		sshClient.Close()
		_ = conn.Close()
		return
	}

	// 启动 shell
	if err := session.Shell(); err != nil {
		cancel()
		session.Close()
		sshClient.Close()
		msg := fmt.Sprintf("启动 Shell 失败: %v", err)
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, msg))
		_ = conn.Close()
		return
	}

	// 清除认证阶段的读取超时
	_ = conn.SetReadDeadline(time.Time{})

	// 审计日志：terminal.open
	clientIP := c.ClientIP()
	openEntry := model.AuditLog{
		UserID:     claims.UserID,
		Username:   claims.Username,
		Role:       claims.Role,
		Method:     "WS",
		Path:       "/api/v1/ws/terminal?action=open",
		StatusCode: 101,
		ClientIP:   clientIP,
		CreatedAt:  time.Now().UTC(),
	}
	_ = h.db.Create(&openEntry)

	// 注册会话
	sessionID := fmt.Sprintf("term-%d-%d", node.ID, time.Now().UnixNano())
	h.mu.Lock()
	h.sessions[sessionID] = cancel
	h.mu.Unlock()

	var closeOnce sync.Once
	cleanup := func() {
		closeOnce.Do(func() {
			h.mu.Lock()
			if fn, ok := h.sessions[sessionID]; ok {
				fn()
				delete(h.sessions, sessionID)
			}
			h.mu.Unlock()
			_ = session.Close()
			sshClient.Close()
			// 发送正常关闭帧，让前端收到 code 1000 以便自动关闭弹窗
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			_ = conn.Close()

			// 审计日志：terminal.close
			closeEntry := model.AuditLog{
				UserID:     claims.UserID,
				Username:   claims.Username,
				Role:       claims.Role,
				Method:     "WS",
				Path:       "/api/v1/ws/terminal?action=close",
				StatusCode: 101,
				ClientIP:   clientIP,
				CreatedAt:  time.Now().UTC(),
			}
			_ = h.db.Create(&closeEntry)
		})
	}

	// SSH stdout → WebSocket（二进制帧）
	go func() {
		defer cleanup()
		buf := make([]byte, 4096)
		for {
			n, readErr := sshStdout.Read(buf)
			if n > 0 {
				if writeErr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					log.Printf("debug: terminal: ssh stdout 读取结束: %v", readErr)
				}
				return
			}
		}
	}()

	// 超时监控
	go func() {
		<-ctx.Done()
		cleanup()
	}()

	// WebSocket → SSH stdin（主循环，阻塞直到连接关闭）
	for {
		msgType, data, readErr := conn.ReadMessage()
		if readErr != nil {
			break
		}

		if msgType == websocket.TextMessage {
			// 尝试解析为控制消息（resize）
			var ctrl struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &ctrl) == nil && ctrl.Type == "resize" {
				var resizeMsg terminalResizeMessage
				if json.Unmarshal(data, &resizeMsg) == nil && resizeMsg.Cols > 0 && resizeMsg.Rows > 0 {
					_ = session.WindowChange(int(resizeMsg.Rows), int(resizeMsg.Cols))
					continue
				}
			}
			// 普通文本输入（键盘输入）
			if _, writeErr := sshStdin.Write(data); writeErr != nil {
				break
			}
		} else if msgType == websocket.BinaryMessage {
			if _, writeErr := sshStdin.Write(data); writeErr != nil {
				break
			}
		}
	}

	cleanup()
}
