package handlers

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type captchaEntry struct {
	answer    int
	expiresAt time.Time
}

// CaptchaStore 是验证码存储，支持在 CaptchaHandler 和 AuthHandler 之间共享。
type CaptchaStore struct {
	store sync.Map
}

func NewCaptchaStore() *CaptchaStore {
	s := &CaptchaStore{}
	go s.cleanupLoop()
	return s
}

func (s *CaptchaStore) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		s.store.Range(func(key, value any) bool {
			if entry, ok := value.(captchaEntry); ok && now.After(entry.expiresAt) {
				s.store.Delete(key)
			}
			return true
		})
	}
}

// Set 存入一条验证码记录，TTL 5 分钟。
func (s *CaptchaStore) Set(id string, answer int) {
	s.store.Store(id, captchaEntry{
		answer:    answer,
		expiresAt: time.Now().Add(5 * time.Minute),
	})
}

// Verify 校验并一次性删除。返回 (ok, expired)。
func (s *CaptchaStore) Verify(id string, answer int) bool {
	raw, loaded := s.store.LoadAndDelete(id)
	if !loaded {
		return false
	}
	entry, ok := raw.(captchaEntry)
	if !ok {
		return false
	}
	if time.Now().After(entry.expiresAt) {
		return false
	}
	return entry.answer == answer
}

// CaptchaHandler 处理验证码相关请求。
type CaptchaHandler struct {
	captchaStore *CaptchaStore
}

func NewCaptchaHandler(store *CaptchaStore) *CaptchaHandler {
	return &CaptchaHandler{captchaStore: store}
}

// GenerateCaptcha 生成一道加法题并返回 {id, question}。
func (h *CaptchaHandler) GenerateCaptcha(c *gin.Context) {
	a, err := rand.Int(rand.Reader, big.NewInt(20))
	if err != nil {
		respondInternalError(c, fmt.Errorf("生成验证码失败: %w", err))
		return
	}
	b, err := rand.Int(rand.Reader, big.NewInt(20))
	if err != nil {
		respondInternalError(c, fmt.Errorf("生成验证码失败: %w", err))
		return
	}

	// 取值范围 1–20
	numA := int(a.Int64()) + 1
	numB := int(b.Int64()) + 1
	answer := numA + numB

	id := generateCaptchaID()
	h.captchaStore.Set(id, answer)

	respondOK(c, gin.H{
		"id":       id,
		"question": fmt.Sprintf("%d + %d = ?", numA, numB),
	})
}

// generateCaptchaID 用 crypto/rand 生成一个 UUID 格式的字符串。
func generateCaptchaID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
