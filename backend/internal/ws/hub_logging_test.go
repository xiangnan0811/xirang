package ws

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/logger"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

func TestPublishOverflowLoggingBound(t *testing.T) {
	var output bytes.Buffer
	previous := logger.Log
	logger.Log = zerolog.New(zerolog.SyncWriter(&output))
	t.Cleanup(func() { logger.Log = previous })
	h := NewHub(nil, nil, false)
	secret := LogEvent{Message: "secret-event-payload"}
	for i := 0; i < cap(h.broadcast); i++ {
		h.Publish(secret)
	}
	var wg sync.WaitGroup
	now := time.Unix(100, 0)
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); h.publishAt(secret, now) }()
	}
	wg.Wait()
	if got := atomic.LoadUint64(&h.droppedCount); got != 1000 {
		t.Fatalf("drops=%d", got)
	}
	if strings.Contains(output.String(), secret.Message) {
		t.Fatal("payload leaked")
	}
	if got := strings.Count(output.String(), "\n"); got != 1 {
		t.Fatalf("warnings=%d want 1", got)
	}
}

func TestHandshakeReadDiagnosticIsSafeAndLevelAware(t *testing.T) {
	for _, level := range []zerolog.Level{zerolog.InfoLevel, zerolog.DebugLevel} {
		t.Run(level.String(), func(t *testing.T) {
			var output bytes.Buffer
			previous := logger.Log
			logger.Log = zerolog.New(&output).Level(level)
			t.Cleanup(func() { logger.Log = previous })
			h := NewHub(nil, nil, true)
			done := make(chan struct{})
			r := gin.New()
			r.GET("/ws", func(c *gin.Context) {
				defer close(done)
				h.ServeWS(c, func(string) (AccessScope, error) {
					t.Error("close frame must not reach authorization")
					return AccessScope{}, nil
				})
			})
			server := httptest.NewServer(r)
			defer server.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			const secret = "client-controlled-secret-close-reason"
			if err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, secret)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("handshake did not finish")
			}
			if strings.Contains(output.String(), secret) {
				t.Fatal("close reason leaked")
			}
			if level == zerolog.InfoLevel && output.Len() != 0 {
				t.Fatalf("debug diagnostic emitted at info: %s", output.String())
			}
			if level == zerolog.DebugLevel && !strings.Contains(output.String(), "handshake read failed") {
				t.Fatal("missing safe debug diagnostic")
			}
		})
	}
}

func TestPublishOverflowWarningInterval(t *testing.T) {
	var output bytes.Buffer
	previous := logger.Log
	logger.Log = zerolog.New(&output)
	t.Cleanup(func() { logger.Log = previous })
	h := NewHub(nil, nil, false)
	for i := 0; i < cap(h.broadcast); i++ {
		h.Publish(LogEvent{})
	}
	now := time.Unix(100, 0)
	h.publishAt(LogEvent{}, now)
	h.publishAt(LogEvent{}, now.Add(30*time.Second-time.Nanosecond))
	h.publishAt(LogEvent{}, now.Add(30*time.Second))
	h.publishAt(LogEvent{}, now.Add(31*time.Second))
	if got := strings.Count(output.String(), "\n"); got != 2 {
		t.Fatalf("warnings=%d want 2", got)
	}
	if !strings.Contains(output.String(), `"dropped_total":3`) {
		t.Fatalf("missing aggregate: %s", output.String())
	}
	other := NewHub(nil, nil, false)
	other.broadcast = make(chan LogEvent)
	other.publishAt(LogEvent{}, now)
	if got := strings.Count(output.String(), "\n"); got != 3 {
		t.Fatalf("per-hub warnings=%d", got)
	}
}
