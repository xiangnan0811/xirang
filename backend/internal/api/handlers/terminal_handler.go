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
	"sync/atomic"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	gossh "golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

const (
	terminalSessionTimeout = 30 * time.Minute
	maxTerminalSessions    = 10
)

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

// pendingSlotCounter 单调递增，确保并发请求拿到唯一 placeholder ID（time.Now 在
// 同一纳秒可能撞）。
var pendingSlotCounter atomic.Uint64

// reserveSlotID 预占 sessions map 的一个槽位返回 placeholder ID；上限内成功，
// 上限外返回空串。Wave 2 (PR-C C3): 修 TOCTOU 漏洞——之前 ServeTerminal 是
// "先 len() 检查，再耗时拨 SSH，最后注册 session"。N 个并发请求都能通过 len
// 检查后再注册，导致实际会话数 > maxTerminalSessions。现在 reserveSlotID 在
// 持锁内一并完成"检查 + 占位"，cleanup 路径必须 freeSlot 释放或转正为真正
// session ID。
func (h *TerminalHandler) reserveSlotID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.sessions) >= maxTerminalSessions {
		return ""
	}
	// 单调计数器避免并发 goroutine 撞同一纳秒的 ID。
	id := fmt.Sprintf("term-pending-%d-%d", time.Now().UnixNano(), pendingSlotCounter.Add(1))
	// 占位 cancel=nil；ServeTerminal 真正起 SSH 后用 promoteSlot 替换。
	h.sessions[id] = nil
	return id
}

// freeSlot 释放预占的 session 槽（reserveSlotID 失败路径或 cleanup）。
func (h *TerminalHandler) freeSlot(id string) {
	if id == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, id)
}

// promoteSlot 把预占槽换成真正的 session（带可取消的 ctx cancel）。
// 调用前 reserveSlotID 必须已成功。
func (h *TerminalHandler) promoteSlot(oldID, newID string, cancel context.CancelFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, oldID)
	h.sessions[newID] = cancel
}

// writeTerminalAuditEntry 写一条非中间件路径的审计日志（dial/auth/PTY 失败等）。
// Wave 2 (PR-C C4): 之前只有"成功打开 + 关闭"写 audit；失败路径只 log 不留痕，
// 攻击者用被入侵的 admin token 可以枚举 node_id 探测节点存活而无审计可追溯。
func (h *TerminalHandler) writeTerminalAuditEntry(c *gin.Context, claims *auth.Claims, action string, statusCode int) {
	if h.db == nil {
		return
	}
	username := ""
	role := ""
	var userID uint
	if claims != nil {
		username = claims.Username
		role = claims.Role
		userID = claims.UserID
	}
	entry := model.AuditLog{
		UserID:     userID,
		Username:   username,
		Role:       role,
		Method:     "WS",
		Path:       fmt.Sprintf("/api/v1/ws/terminal?action=%s", action),
		StatusCode: statusCode,
		ClientIP:   c.ClientIP(),
		CreatedAt:  time.Now().UTC(),
	}
	_ = middleware.SaveAuditLogWithHashChain(h.db, &entry)
}

// ServeTerminal godoc
// @Summary      WebSocket SSH 终端
// @Description  建立 WebSocket 连接，通过 SSH PTY 提供交互式终端（JWT 通过首条消息认证，仅 admin）
// @Tags         terminal
// @Produce      json
// @Param        node_id  query     int  true  "节点 ID"
// @Success      101
// @Failure      401  {object}  handlers.Response
// @Failure      503  {object}  handlers.Response
// @Router       /ws/terminal [get]
func (h *TerminalHandler) ServeTerminal(c *gin.Context) {
	// Wave 2 (PR-C C3): 先抢占槽位再做后续耗时操作，杜绝 N 个并发请求同时
	// 通过 len() 检查的 TOCTOU。失败立即 503，不消耗任何 SSH/WS 资源。
	pendingID := h.reserveSlotID()
	if pendingID == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "终端会话数已达上限"})
		return
	}
	// 失败路径必须释放槽位；成功路径会用 promoteSlot 转正后清掉 pendingID。
	pendingFreed := false
	freePending := func() {
		if !pendingFreed {
			h.freeSlot(pendingID)
			pendingFreed = true
		}
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		freePending()
		log.Printf("warn: terminal: websocket 升级失败: %v", err)
		return
	}

	// 等待认证消息（5 秒超时）
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, rawMsg, err := conn.ReadMessage()
	if err != nil {
		freePending()
		_ = conn.Close()
		return
	}

	var authMsg terminalAuthMessage
	if err := json.Unmarshal(rawMsg, &authMsg); err != nil || authMsg.Type != "auth" {
		freePending()
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "认证消息格式错误"))
		_ = conn.Close()
		return
	}

	claims, err := authorizeRealtimeToken(authMsg.Token, h.jwtManager, h.db, realtimeAuthRequirements{Role: "admin"})
	if err != nil {
		// Wave 2 (PR-C C4): 认证失败也写审计——claims 为 nil（拿不到 user/role），
		// 但仍记录 client IP + path 让管理员能看到尝试。
		h.writeTerminalAuditEntry(c, nil, "auth-failed", http.StatusUnauthorized)
		freePending()
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "认证失败或权限不足"))
		_ = conn.Close()
		return
	}

	// 解析 node_id 参数
	rawNodeID := c.Query("node_id")
	if rawNodeID == "" {
		freePending()
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInvalidFramePayloadData, "缺少 node_id 参数"))
		_ = conn.Close()
		return
	}
	nodeID64, err := strconv.ParseUint(rawNodeID, 10, 64)
	if err != nil {
		freePending()
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInvalidFramePayloadData, "node_id 格式无效"))
		_ = conn.Close()
		return
	}

	// 查询节点
	var node model.Node
	if err := h.db.Preload("SSHKey").First(&node, uint(nodeID64)).Error; err != nil {
		// Wave 2 (PR-C C4): 节点不存在的尝试也审计，避免被入侵账号枚举节点 ID。
		h.writeTerminalAuditEntry(c, claims, "node-not-found", http.StatusNotFound)
		freePending()
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInvalidFramePayloadData, "节点不存在"))
		_ = conn.Close()
		return
	}

	// 建立 SSH 连接
	authMethods, err := sshutil.BuildSSHAuth(node, h.db)
	if err != nil {
		log.Printf("warn: terminal: 构建 SSH 认证失败 (node=%d): %v", node.ID, err)
		h.writeTerminalAuditEntry(c, claims, "ssh-auth-init-failed", http.StatusInternalServerError)
		freePending()
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "SSH 认证初始化失败"))
		_ = conn.Close()
		return
	}

	hostKeyCallback, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		log.Printf("warn: terminal: 解析主机密钥失败 (node=%d): %v", node.ID, err)
		h.writeTerminalAuditEntry(c, claims, "host-key-failed", http.StatusInternalServerError)
		freePending()
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "主机密钥校验失败"))
		_ = conn.Close()
		return
	}

	addr := fmt.Sprintf("%s:%d", node.Host, node.Port)
	ctx, cancel := context.WithTimeout(context.Background(), terminalSessionTimeout)

	sshClient, err := sshutil.DialSSH(ctx, addr, node.Username, authMethods, hostKeyCallback)
	if err != nil {
		cancel()
		log.Printf("warn: terminal: SSH 连接失败 (node=%d): %v", node.ID, err)
		// Wave 2 (PR-C C4): SSH 拨号失败 = 节点不可达 / 端口错 / 认证拒绝 →
		// 这是 admin 滥用扫描的关键审计点。
		h.writeTerminalAuditEntry(c, claims, "ssh-dial-failed", http.StatusInternalServerError)
		freePending()
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "SSH 连接失败，请检查节点配置"))
		_ = conn.Close()
		return
	}

	session, err := sshClient.NewSession()
	if err != nil {
		cancel()
		_ = sshClient.Close()
		log.Printf("warn: terminal: SSH 会话创建失败 (node=%d): %v", node.ID, err)
		h.writeTerminalAuditEntry(c, claims, "ssh-session-failed", http.StatusInternalServerError)
		freePending()
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "SSH 会话创建失败"))
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
		_ = session.Close()
		_ = sshClient.Close()
		log.Printf("warn: terminal: 请求 PTY 失败 (node=%d): %v", node.ID, err)
		h.writeTerminalAuditEntry(c, claims, "pty-failed", http.StatusInternalServerError)
		freePending()
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "终端初始化失败"))
		_ = conn.Close()
		return
	}

	// 连接 SSH 标准 I/O
	sshStdin, err := session.StdinPipe()
	if err != nil {
		cancel()
		_ = session.Close()
		_ = sshClient.Close()
		freePending()
		_ = conn.Close()
		return
	}
	sshStdout, err := session.StdoutPipe()
	if err != nil {
		cancel()
		_ = session.Close()
		_ = sshClient.Close()
		freePending()
		_ = conn.Close()
		return
	}

	// 启动 shell
	if err := session.Shell(); err != nil {
		cancel()
		_ = session.Close()
		_ = sshClient.Close()
		log.Printf("warn: terminal: 启动 Shell 失败 (node=%d): %v", node.ID, err)
		h.writeTerminalAuditEntry(c, claims, "shell-failed", http.StatusInternalServerError)
		freePending()
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "Shell 启动失败"))
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
	_ = middleware.SaveAuditLogWithHashChain(h.db, &openEntry)

	// 注册会话：把先前 reserveSlotID 的占位 ID 替换为真正 sessionID + cancel。
	// 自此 pendingID 已被 promoteSlot 删除，无需再 freePending。
	sessionID := fmt.Sprintf("term-%d-%d", node.ID, time.Now().UnixNano())
	h.promoteSlot(pendingID, sessionID, cancel)
	pendingFreed = true

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
			_ = sshClient.Close()
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
			_ = middleware.SaveAuditLogWithHashChain(h.db, &closeEntry)
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
