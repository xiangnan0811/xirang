package handlers

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

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
