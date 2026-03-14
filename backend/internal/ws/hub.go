package ws

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

type LogEvent struct {
	LogID     uint      `json:"log_id"`
	TaskID    uint      `json:"task_id"`
	TaskRunID *uint     `json:"task_run_id,omitempty"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	Status    string    `json:"status,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type client struct {
	conn          *websocket.Conn
	send          chan LogEvent
	filterTaskID  *uint
	authenticated bool
}

type Hub struct {
	db               *gorm.DB
	clients          map[*client]struct{}
	register         chan *client
	unregister       chan *client
	broadcast        chan LogEvent
	mu               sync.RWMutex
	allowedOrigins   []string
	allowEmptyOrigin bool
	droppedCount     uint64
}

func NewHub(db *gorm.DB, allowedOrigins []string, allowEmptyOrigin bool) *Hub {
	return &Hub{
		db:               db,
		clients:          make(map[*client]struct{}),
		register:         make(chan *client),
		unregister:       make(chan *client, 64),
		broadcast:        make(chan LogEvent, 256),
		allowedOrigins:   allowedOrigins,
		allowEmptyOrigin: allowEmptyOrigin,
	}
}

func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = struct{}{}
			h.mu.Unlock()
		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
			}
			h.mu.Unlock()
		case event := <-h.broadcast:
			h.mu.RLock()
			for c := range h.clients {
				if c.filterTaskID != nil && event.TaskID != *c.filterTaskID {
					continue
				}
				select {
				case c.send <- event:
				default:
					go func(cc *client) { h.unregister <- cc }(c)
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (h *Hub) Publish(event LogEvent) {
	select {
	case h.broadcast <- event:
	default:
		atomic.AddUint64(&h.droppedCount, 1)
		log.Printf("warn: broadcast channel full, event dropped (total dropped: %d)", atomic.LoadUint64(&h.droppedCount))
	}
}

// CheckOrigin 供外部组件（如 TerminalHandler）复用相同的 Origin 校验策略。
func (h *Hub) CheckOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return h.allowEmptyOrigin
	}
	for _, o := range h.allowedOrigins {
		if strings.TrimSpace(o) == "*" {
			return true
		}
	}
	for _, allowed := range h.allowedOrigins {
		if strings.EqualFold(origin, strings.TrimSpace(allowed)) {
			return true
		}
	}
	return util.IsSameHostOrigin(origin, r.Host)
}

func (h *Hub) newUpgrader() websocket.Upgrader {
	return websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin == "" {
				return h.allowEmptyOrigin
			}
			for _, o := range h.allowedOrigins {
				if strings.TrimSpace(o) == "*" {
					return true
				}
			}
			for _, allowed := range h.allowedOrigins {
				if strings.EqualFold(origin, strings.TrimSpace(allowed)) {
					return true
				}
			}
			// 安全前提：浏览器保证 Host 头真实性；生产环境应通过反向代理强制设置 Host。
			if util.IsSameHostOrigin(origin, r.Host) {
				return true
			}
			return false
		},
	}
}

type authMessage struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

func (h *Hub) ServeWS(c *gin.Context, validateToken func(string) bool) {
	upgrader := h.newUpgrader()
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "升级 websocket 失败"})
		return
	}

	// 等待认证消息（5 秒超时）
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		return
	}

	var authMsg authMessage
	if err := json.Unmarshal(msg, &authMsg); err != nil || authMsg.Type != "auth" || !validateToken(authMsg.Token) {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "认证失败"))
		_ = conn.Close()
		return
	}

	// 认证通过，恢复正常读超时
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))

	var filterTaskID *uint
	if raw := c.Query("task_id"); raw != "" {
		id64, parseErr := strconv.ParseUint(raw, 10, 64)
		if parseErr == nil {
			id := uint(id64)
			filterTaskID = &id
		}
	}

	sinceID := parseUint(c.Query("since_id"))
	backfillEvents := make([]LogEvent, 0)
	if sinceID > 0 {
		events, loadErr := h.loadBackfillEvents(sinceID, filterTaskID)
		if loadErr == nil {
			backfillEvents = events
		}
	}

	cl := &client{
		conn:          conn,
		send:          make(chan LogEvent, 64),
		filterTaskID:  filterTaskID,
		authenticated: true,
	}
	h.register <- cl

	go cl.writePump(func() { h.unregister <- cl })
	go cl.readPump(func() { h.unregister <- cl })

	for _, event := range backfillEvents {
		select {
		case cl.send <- event:
		default:
			h.unregister <- cl
			return
		}
	}
}

func (h *Hub) loadBackfillEvents(sinceID uint, taskID *uint) ([]LogEvent, error) {
	if h.db == nil {
		return nil, nil
	}

	query := h.db.Model(&model.TaskLog{}).Where("id > ?", sinceID)
	if taskID != nil {
		query = query.Where("task_id = ?", *taskID)
	}

	var logs []model.TaskLog
	if err := query.Order("id asc").Limit(500).Find(&logs).Error; err != nil {
		return nil, err
	}

	taskIDs := make(map[uint]struct{})
	for _, item := range logs {
		if item.TaskID > 0 {
			taskIDs[item.TaskID] = struct{}{}
		}
	}

	taskStatusByID := make(map[uint]string, len(taskIDs))
	if len(taskIDs) > 0 {
		ids := make([]uint, 0, len(taskIDs))
		for id := range taskIDs {
			ids = append(ids, id)
		}

		var tasks []model.Task
		if err := h.db.Model(&model.Task{}).Select("id", "status").Where("id IN ?", ids).Find(&tasks).Error; err != nil {
			return nil, err
		}
		for _, item := range tasks {
			taskStatusByID[item.ID] = item.Status
		}
	}

	events := make([]LogEvent, 0, len(logs))
	for _, item := range logs {
		events = append(events, LogEvent{
			LogID:     item.ID,
			TaskID:    item.TaskID,
			Level:     item.Level,
			Message:   item.Message,
			Status:    taskStatusByID[item.TaskID],
			Timestamp: item.CreatedAt,
		})
	}
	return events, nil
}

func parseUint(raw string) uint {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0
	}
	return uint(parsed)
}

func (c *client) writePump(onClose func()) {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
		onClose()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteJSON(msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *client) readPump(onClose func()) {
	defer func() {
		_ = c.conn.Close()
		onClose()
	}()
	c.conn.SetReadLimit(1024)
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}
