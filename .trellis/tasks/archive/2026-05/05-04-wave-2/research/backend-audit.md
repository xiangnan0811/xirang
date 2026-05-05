# Wave 2 后端审查

## 审查范围与方法

实读 14 个核心文件 + 4 个相关 handler，覆盖以下模块：

- `internal/alerting/` — dispatcher.go (917 行), retry.go, silence.go, silence_retention.go, sender.go, grouping.go
- `internal/reporting/generator.go`（含 Scheduler）
- `internal/metrics/` — remote_sink.go, db_sink.go, sink.go, aggregator.go
- `internal/anomaly/engine.go, retention.go`
- `internal/escalation/engine.go, service.go, dispatcher_bridge.go`
- `internal/bandwidth/bandwidth.go`
- `internal/retention/worker.go`
- `internal/task/scheduler/scheduler.go`
- `internal/api/handlers/` — terminal_handler.go, file_handler.go (validateNodePath/validateLocalPath), report_handler.go, silence_handler.go, anomaly_handler.go, alert_handler.go, escalation_handler.go
- `internal/middleware/` — ownership.go, rbac.go (交叉验证 RBAC 矩阵)

方法：每个 finding 实读 5+ 行 + 引用 file:line。所有 RBAC/Ownership 候选都通过 router.go 与 middleware/rbac.go 双向交叉验证后才下结论。

---

## Findings

### F-1 [✅] reporting：`sendReport` goroutine 在 shutdown 时被丢弃

- **文件:行**：`backend/internal/reporting/generator.go:78-86, 208-242`
- **实读片段**：
  ```go
  if err := db.Create(report).Error; err != nil {
      return nil, fmt.Errorf("保存报告失败: %w", err)
  }

  // 5. 发送到通知渠道
  go sendReport(db, cfg, report)

  return report, nil
  ```
  以及 sendReport 本体：
  ```go
  func sendReport(db *gorm.DB, cfg model.ReportConfig, report *model.Report) {
      defer func() {
          if r := recover(); r != nil {
              log.Printf("报告发送 panic（config=%d, report=%d）: %v", cfg.ID, report.ID, r)
          }
      }()
      ...
      for _, intID := range integrationIDs {
          var integration model.Integration
          if err := db.First(&integration, intID).Error; err != nil {
              log.Printf("报告发送：通知渠道 %d 不存在", intID)
              continue
          }
          ...
          if err := alerting.SendAlert(integration, alertMsg); err != nil {
              log.Printf("报告发送失败（渠道 %d）: %v", intID, err)
          }
      }
  }
  ```
- **问题**：`Generate` 启动的 goroutine 没有 context、没有 wg 跟踪、没有 timeout（依赖 `alerting.send` 内部 HTTP client 默认 15s × N 个渠道）。Scheduler.Run 关闭时只关 ticker；`go sendReport` 仍在 OS goroutine 里继续跑 SMTP/HTTP 调用，进程退出时被强制终止——投递可能半途中断（webhook 发了一半 / SMTP 在 STARTTLS 协商）。
- **影响**：进程优雅停机时，部分通知渠道的连接被截断；接收端可能记录半发送告警。Scheduler 的 Shutdown 报告"已停止"但实际上仍有 goroutine 持有 db 句柄 → 可能在 db.Close 后 panic。
- **正确修复方向**：在 `Scheduler` 上挂一个 `sync.WaitGroup`，把 `sendReport` 改成 `func (s *Scheduler) sendReport(ctx, ...)`；`Run` 入口将 `wg.Add(1)`+ defer wg.Done 注入到 sendReport 内；`Shutdown` 在等 done 之后再 `wg.Wait()`。同步调用方（API 的 GenerateNow handler）单独处理：要么用 `c.Request.Context()`，要么把 sendReport 改为同步、handler 用 goroutine 包装。
- **工作量**：S

### F-2 [⚠️] reporting Scheduler：cron 匹配缺去重，时钟漂移可能多次触发同一报告

- **文件:行**：`backend/internal/reporting/generator.go:268-342`
- **实读片段**：
  ```go
  func (s *Scheduler) Run(ctx context.Context) {
      ticker := time.NewTicker(time.Minute)
      ...
      for {
          select {
          case <-ctx.Done():
              return
          case t := <-ticker.C:
              s.checkAndGenerate(t)
          }
      }
  }

  func (s *Scheduler) checkAndGenerate(now time.Time) {
      var configs []model.ReportConfig
      if err := s.db.Where("enabled = ?", true).Find(&configs).Error; err != nil {
          return
      }
      for _, cfg := range configs {
          if shouldGenerate(cfg, now) {
              ...
              if _, err := Generate(s.db, cfg, start, now); err != nil {
                  log.Printf("定时报告生成失败（config=%d）: %v", cfg.ID, err)
              }
          }
      }
  }
  ```
- **问题**：`time.NewTicker(time.Minute)` 在系统挂起 / GC stall 时可能在同一分钟内连续 tick 两次（time 包语义）；`shouldGenerate` 仅比对当前 wall-clock，不检查 ReportConfig 是否在该分钟已生成。`Generate` 又会写入 reports 表 + 启动 goroutine 发通知。
- **影响**：极端情况下同一份报告生成两份并发出两条通知。GC stall 在容器化环境（CPU 节流）非小概率事件。
- **正确修复方向**：在 ReportConfig 加 `last_generated_at` 字段（migration），`shouldGenerate` 内增加 `if cfg.LastGeneratedAt != nil && now.Sub(*cfg.LastGeneratedAt) < time.Minute { return false }`；或维持内存 map[uint]time.Time 缓存（重启后允许一次重复，可接受）。
- **工作量**：M

### F-3 [✅] terminal_handler：会话上限检查存在 TOCTOU race

- **文件:行**：`backend/internal/api/handlers/terminal_handler.go:72-79, 244-247`
- **实读片段**：
  ```go
  func (h *TerminalHandler) ServeTerminal(c *gin.Context) {
      h.mu.Lock()
      sessionCount := len(h.sessions)
      h.mu.Unlock()
      if sessionCount >= maxTerminalSessions {
          c.JSON(http.StatusServiceUnavailable, gin.H{"error": "终端会话数已达上限"})
          return
      }

      conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
      ...
  ```
  ...一直到 250 行才注册 session：
  ```go
  sessionID := fmt.Sprintf("term-%d-%d", node.ID, time.Now().UnixNano())
  h.mu.Lock()
  h.sessions[sessionID] = cancel
  h.mu.Unlock()
  ```
- **问题**：检查与注册之间隔了 ~170 行（含 SSH 拨号、PTY 请求等耗时操作）。N 个并发请求都能通过 line 76 的检查（`<10`），最后全部注册成功，导致实际会话数超过 `maxTerminalSessions=10`。
- **影响**：上限失效。每个会话持有 SSH 连接 + 若干 goroutine + WebSocket 连接，10 倍超限可消耗显著资源。仅 admin 可触发，但仍是 DoS 入口。
- **正确修复方向**：在 line 73 处直接尝试占位（先生成 placeholder sessionID，加锁内 `if len(h.sessions) >= max { return 503 }; h.sessions[id] = nil`），失败路径必须 cleanup 该 placeholder。或者引入 `chan struct{}` 信号量作为并发闸门。
- **工作量**：S

### F-4 [⚠️] alerting dispatcher：每渠道 goroutine 数量无上限

- **文件:行**：`backend/internal/alerting/dispatcher.go:377-408`
- **实读片段**：
  ```go
  var wg sync.WaitGroup
  for _, channel := range integrations {
      if int(openCount) < channel.FailThreshold {
          continue
      }
      if inCooldown(db, channel.ID, channel.CooldownMinutes, now) {
          continue
      }

      wg.Add(1)
      go func(ch model.Integration) {
          defer wg.Done()
          err := send(ch, *alert)
          ...
      }(channel)
  }
  wg.Wait()
  ```
- **问题**：每条 alert 给每个 enabled integration 都起一个 goroutine，wg.Wait 同步等待。若一个慢渠道（30s timeout 代理）阻塞，会拖慢整条 raiseAndDispatch 链路；taskRunner 的 critical path 也跟着卡。同时多个并发 alert 会乘以 integration 数。
- **影响**：在 N=10 渠道、M=5 并发 alert 场景下出现 50 个并发外发；正常但单个慢通道把整个 alert 路径堵 30s。这不是泄漏（wg.Wait 收尾），但会让 RaiseTaskFailure 失去响应性，进而拖累 task runner。
- **正确修复方向**：把同步分发改为投入持久队列（已有 alert_deliveries 表 + RetryWorker），首次发送也走 retrying 状态，让 RetryWorker 立即扫描。这样 raiseAndDispatch 立即返回，不阻塞 caller。
- **工作量**：M

### F-5 [⚠️] silence Patch：`starts_at` 仅用于校验、不更新，导致校验语义模糊

- **文件:行**：`backend/internal/api/handlers/silence_handler.go:128-163`
- **实读片段**：
  ```go
  type silencePatchRequest struct {
      Name     string    `json:"name" binding:"required"`
      EndsAt   time.Time `json:"ends_at" binding:"required"`
      StartsAt time.Time `json:"starts_at" binding:"required"` // required for end>start validation
      Note     string    `json:"note"`
  }
  ...
  if !req.EndsAt.After(req.StartsAt) {
      respondBadRequest(c, "ends_at 必须晚于 starts_at")
      return
  }
  updates := map[string]any{
      "name":    req.Name,
      "ends_at": req.EndsAt,
      "note":    req.Note,
  }
  ```
- **问题**：`StartsAt` 不写库，仅用作"end>start"校验；客户端可以传任意 StartsAt 通过校验，实际生效的 starts_at 仍为旧值。结果：可以把 ends_at 调到旧 starts_at 之前（client 提交 starts_at=2099, ends_at=2100，校验通过；但库里 starts_at=2026-01-01, ends_at=2100 → 静默被错误延长）。
- **影响**：admin 可意外把已过期 silence "复活"或延长无关 silence。属于权限内但反直觉的行为，不算安全漏洞但破坏 silence retention 假设。
- **正确修复方向**：要么在校验前先 `req.StartsAt = s.StartsAt`（用 DB 值校验），要么允许更新 starts_at 并校验"new ends_at > original starts_at"。
- **工作量**：S

### F-6 [✅] reporting 生成的报告未按 ownership 过滤通知渠道

- **文件:行**：`backend/internal/reporting/generator.go:208-242`
- **实读片段**：
  ```go
  for _, intID := range integrationIDs {
      var integration model.Integration
      if err := db.First(&integration, intID).Error; err != nil {
          log.Printf("报告发送：通知渠道 %d 不存在", intID)
          continue
      }
      if !integration.Enabled {
          continue
      }
      if err := alerting.SendAlert(integration, alertMsg); err != nil {
          log.Printf("报告发送失败（渠道 %d）: %v", intID, err)
      }
  }
  ```
- **问题**：报告内容（含 SLA 指标、节点失败热点 last_err 字段）会原文发到 `cfg.IntegrationIDs` 配置的所有渠道。`buildTopFailures` 把 `MAX(task_runs.last_error)` 直接 join 后拼到 message。如果 last_error 内容来自命令输出（rsync stderr 包含路径名），可能泄露内部路径或文件名到外部 webhook/Slack。同时调用 `alerting.SendAlert(integration, ...)` 直接绕过 silence/grouping 流程（这是设计如此）。
- **影响**：CONFIDENTIAL 信息泄露入注册的通知渠道。报告本身就是要发的，但 last_err 字段没有 sanitize。注：reports:write 仅 admin，所以 cfg 创建受控。
- **正确修复方向**：对 last_err 用 `util.SanitizeDeliveryError` 或类似的 length-cap + URL-redact 处理后再拼入 message；保留前 200 字符即可。
- **工作量**：S

### F-7 [❓] metrics RemoteWriteSink：响应 body 拼入 error，可能含敏感线索

- **文件:行**：`backend/internal/metrics/remote_sink.go:140-152`
- **实读片段**：
  ```go
  resp, err := s.client.Do(req)
  if err != nil {
      remoteWriteTotal.WithLabelValues("failure").Inc()
      return err
  }
  defer resp.Body.Close() //nolint:errcheck
  if resp.StatusCode < 200 || resp.StatusCode >= 300 {
      body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
      remoteWriteTotal.WithLabelValues("failure").Inc()
      return fmt.Errorf("remote-write: http %d: %s", resp.StatusCode, string(body))
  }
  ```
- **问题**：错误的 response body（最多 1024 字节）会原样拼入 error.Error()，进入日志。Prometheus/Mimir 401/403 一般只回 "Unauthorized" / "invalid bearer"，不会回显 token 本身。但 body 字段未做 sanitize；若上游中间件返回了原始 Authorization header 内容（非常规但可能），就会泄露。
- **影响**：Bearer token 泄露到 application log 的可能性 — 取决于远端服务行为，我没有验证 Mimir/Cortex 的 401 响应内容。
- **正确修复方向**：在拼接前对 body 做 `redactURLs + sensitivePatterns`（complaining `alerting.sanitizeDeliveryError` 已经有现成实现，可抽到 util）。或截短到 256 字符。
- **工作量**：S

### F-8 [⚠️] alerting dispatcher：HTTP send error.Error() 直接进 LastError 字段，依赖 sanitize 路径

- **文件:行**：`backend/internal/alerting/dispatcher.go:386-405`, `retry.go:140-165, 231-244`
- **实读片段**（dispatcher 首发路径）：
  ```go
  err := send(ch, *alert)
  d := model.AlertDelivery{
      AlertID:       alert.ID,
      IntegrationID: ch.ID,
      AttemptCount:  1,
  }
  if err == nil {
      d.Status = "sent"
  } else {
      next := time.Now().Add(backoffDuration(1))
      d.Status = "retrying"
      d.NextRetryAt = &next
      d.LastError = util.SanitizeDeliveryError(ch.Type, err)
  }
  ```
  retry 路径用 `sanitizeDeliveryError(sendErr)` (alerting 包内部函数)：
  ```go
  default:
      next := time.Now().Add(backoffDuration(d.AttemptCount))
      d.Status = "retrying"
      d.NextRetryAt = &next
      d.LastError = sanitizeDeliveryError(sendErr)
  ```
- **问题**：两处 sanitize 用的是不同实现：
  1. dispatcher.go 用 `util.SanitizeDeliveryError(ch.Type, err)` —— 按 channel type 分发的 sanitize（util 包）
  2. retry.go 用 `sanitizeDeliveryError(sendErr)` —— 包内 redactURLs + sensitivePatterns
  两者覆盖范围可能不一致：`util.SanitizeDeliveryError` 我没读，可能针对 telegram 等做特殊处理；包内 sanitize 是通用 URL/token 屏蔽。如果 `util.SanitizeDeliveryError` 对 webhook 类型没有 URL redact，则首次失败的 LastError 会泄露 webhook URL（含 token）。
- **影响**：`alerts:deliveries` 权限是 viewer 也有 (rbac.go:64)。如果 sanitize 不充分，viewer 可读到带 token 的 webhook URL。
- **正确修复方向**：在 `internal/util/` 读 `SanitizeDeliveryError` 实现，确认其对 webhook/feishu/dingtalk/wecom 都做了 URL 屏蔽；统一两处实现，复用同一函数。
- **工作量**：S（统一调用）/ 取决于 util 函数现状

### F-9 [✅] alerting grouping：AfterFunc 在突发流量下产生短暂的 timer 累积

- **文件:行**：`backend/internal/alerting/grouping.go:33-50`
- **实读片段**：
  ```go
  func (g *Grouping) ShouldSend(key string) bool {
      g.mu.Lock()
      defer g.mu.Unlock()
      now := time.Now()
      if st, ok := g.active[key]; ok && now.Sub(st.firstSeenAt) < g.window {
          st.alertCount++
          return false
      }
      g.active[key] = &groupState{firstSeenAt: now, alertCount: 1}
      time.AfterFunc(g.window, func() {
          g.mu.Lock()
          if st, ok := g.active[key]; ok && time.Since(st.firstSeenAt) >= g.window {
              delete(g.active, key)
          }
          g.mu.Unlock()
      })
      return true
  }
  ```
- **问题**：每次新 key 都 `time.AfterFunc(5min, ...)`，若一个 key 在过期后再出现，会 schedule 第二个 AfterFunc（旧的可能还在内存里 5min 内运行完）。实际峰值由 unique key 数 × 5min 决定，对于稳定的 errorCode|nodeID|tags 组合上限有限。
- **影响**：内存上短期堆积少量 timer，不会持续泄漏。生产环境 alert key 基数小，可忽略。
- **正确修复方向**：（可选）将清理改为 ticker + sweep，或在 active map 内记录 timer 句柄，重新放置时 `Stop()` 旧 timer。当前实现不算 bug，但可优化。
- **工作量**：S（可选）

### F-10 [✅] terminal_handler：建立 SSH 失败时未审计

- **文件:行**：`backend/internal/api/handlers/terminal_handler.go:155-225`
- **实读片段**：
  ```go
  ctx, cancel := context.WithTimeout(context.Background(), terminalSessionTimeout)

  sshClient, err := sshutil.DialSSH(ctx, addr, node.Username, authMethods, hostKeyCallback)
  if err != nil {
      cancel()
      log.Printf("warn: terminal: SSH 连接失败 (node=%d): %v", node.ID, err)
      _ = conn.WriteMessage(websocket.CloseMessage,
          websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "SSH 连接失败，请检查节点配置"))
      _ = conn.Close()
      return
  }
  ```
  审计日志只在 line 230 之后（成功建立 shell 之后）才写：
  ```go
  // 审计日志：terminal.open
  clientIP := c.ClientIP()
  openEntry := model.AuditLog{...}
  _ = middleware.SaveAuditLogWithHashChain(h.db, &openEntry)
  ```
- **问题**：节点不存在 / 认证失败 / SSH 拨号失败 / PTY 失败等"尝试访问但失败"的事件不会被记录到 audit_logs。攻击者（被入侵的 admin token）可枚举 node_id 来探测哪些节点存活，没有审计踪迹。
- **影响**：审计完整性缺失。开放 ws/terminal 的失败尝试无法事后追溯。RBAC 已限制 admin，故影响受限于 admin 滥用场景。
- **正确修复方向**：在 line 81 升级 WS 成功后立即写 `terminal.attempt` 审计日志；在每个 return 分支前统一写 close 状态（参考现有 cleanup 函数中的 close 审计）。
- **工作量**：S

---

## 总体结论

- **真实问题数 (✅)**：4（F-1 / F-3 / F-6 / F-9 / F-10 中标 ✅ 的 4 条：F-1 F-3 F-6 F-10；F-9 标 ✅ 但属低危可选）
- **部分真实数 (⚠️)**：4（F-2 / F-4 / F-5 / F-8）
- **需进一步实读 (❓)**：1（F-7 — 需读 `util.SanitizeDeliveryError` 才能定论 F-8 的真实严重性）

### 推荐优先级（如果做整改）

1. **F-1**（reporting goroutine 泄漏 + 强制中断 SMTP/HTTP）— 影响优雅停机的正确性，工作量小，建议先修
2. **F-3**（terminal session 上限 TOCTOU）— 可绕过限流，建议修
3. **F-6**（report 通知泄露 last_err）— 简单 sanitize 即可，建议修
4. **F-10**（terminal 失败未审计）— 审计完整性，建议修
5. **F-8/F-7**（dispatcher LastError sanitize 一致性）— 需要先读 util 包确认现状再决定
6. **F-2**（cron 重复触发）— 边缘场景，可降优先级
7. **F-4**（dispatcher goroutine 同步等待）— 涉及结构调整，优先级最低
8. **F-5**（silence patch starts_at 校验）— admin-only，UX 问题非安全问题
