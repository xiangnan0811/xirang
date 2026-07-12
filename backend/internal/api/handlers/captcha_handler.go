package handlers

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/settings"

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
	settingsSvc  *settings.Service
}

func NewCaptchaHandler(store *CaptchaStore) *CaptchaHandler {
	return &CaptchaHandler{captchaStore: store}
}

// WithSettingsService injects settings for dual-captcha flags.
func (h *CaptchaHandler) WithSettingsService(svc *settings.Service) *CaptchaHandler {
	h.settingsSvc = svc
	return h
}

func (h *CaptchaHandler) captchaEnabled() bool {
	if h.settingsSvc == nil {
		return false
	}
	return strings.ToLower(h.settingsSvc.GetEffective("login.captcha_enabled")) == "true"
}

func (h *CaptchaHandler) secondCaptchaEnabled() bool {
	if h.settingsSvc == nil {
		return false
	}
	return strings.ToLower(h.settingsSvc.GetEffective("login.second_captcha_enabled")) == "true"
}

type captchaChallenge struct {
	id       string
	question string
}

func (h *CaptchaHandler) generateChallenge() (captchaChallenge, error) {
	a, err := rand.Int(rand.Reader, big.NewInt(20))
	if err != nil {
		return captchaChallenge{}, err
	}
	b, err := rand.Int(rand.Reader, big.NewInt(20))
	if err != nil {
		return captchaChallenge{}, err
	}
	numA := int(a.Int64()) + 1
	numB := int(b.Int64()) + 1
	id := generateCaptchaID()
	h.captchaStore.Set(id, numA+numB)
	return captchaChallenge{
		id:       id,
		question: fmt.Sprintf("%d + %d = ?", numA, numB),
	}, nil
}

// GenerateCaptcha godoc
// @Summary      生成数学验证码
// @Description  按 login.captcha_enabled / login.second_captcha_enabled 生成挑战。
// @Description  关闭的通道不返回 id/question，避免前端展示“必填但不校验”的假挑战。
// @Tags         auth
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Router       /auth/captcha [get]
func (h *CaptchaHandler) GenerateCaptcha(c *gin.Context) {
	primaryOn := h.captchaEnabled()
	secondOn := h.secondCaptchaEnabled()

	payload := gin.H{
		"enabled":         primaryOn,
		"second_required": secondOn,
	}

	if primaryOn {
		primary, err := h.generateChallenge()
		if err != nil {
			respondInternalError(c, fmt.Errorf("生成验证码失败: %w", err))
			return
		}
		payload["id"] = primary.id
		payload["question"] = primary.question
	}

	if secondOn {
		second, err := h.generateChallenge()
		if err != nil {
			respondInternalError(c, fmt.Errorf("生成二次验证码失败: %w", err))
			return
		}
		payload["second_id"] = second.id
		payload["second_question"] = second.question
	}

	respondOK(c, payload)
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
