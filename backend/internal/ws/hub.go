package ws

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

type LogEvent struct {
	LogID     uint      `json:"log_id"`
	TaskID    uint      `json:"task_id"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	Status    string    `json:"status,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type client struct {
	conn         *websocket.Conn
	send         chan LogEvent
	filterTaskID *uint
}

type Hub struct {
	db         *gorm.DB
	clients    map[*client]struct{}
	register   chan *client
	unregister chan *client
	broadcast  chan LogEvent
	mu         sync.RWMutex
}

const wsAuthProtocol = "xirang-auth.v1"

func NewHub(db *gorm.DB) *Hub {
	return &Hub{
		db:         db,
		clients:    make(map[*client]struct{}),
		register:   make(chan *client),
		unregister: make(chan *client),
		broadcast:  make(chan LogEvent, 256),
	}
}

func (h *Hub) Run() {
	for {
		select {
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
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	Subprotocols:    []string{wsAuthProtocol},
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func (h *Hub) ServeWS(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "升级 websocket 失败"})
		return
	}

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

	client := &client{
		conn:         conn,
		send:         make(chan LogEvent, 64),
		filterTaskID: filterTaskID,
	}
	h.register <- client

	go client.writePump(func() { h.unregister <- client })
	go client.readPump(func() { h.unregister <- client })

	for _, event := range backfillEvents {
		select {
		case client.send <- event:
		default:
			h.unregister <- client
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

	events := make([]LogEvent, 0, len(logs))
	for _, item := range logs {
		events = append(events, LogEvent{
			LogID:     item.ID,
			TaskID:    item.TaskID,
			Level:     item.Level,
			Message:   item.Message,
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
