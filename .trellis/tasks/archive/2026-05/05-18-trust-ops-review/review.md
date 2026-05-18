# Trust Ops 全链路审查记录

## 审查范围

覆盖五个已发布 Trust Ops 方向：

1. Verified Restore Drill Evidence
2. Backup Confidence Center
3. SSH Fleet Doctor
4. Health Incident Timeline
5. Trust Demo and Feedback Funnel

审查维度：产品链闭环、跨层 API/type 合同、安全边界、demo/docs 真实性、测试与发布验证证据。

## 结论

审查发现并修复了 5 类问题：2 个后端高风险安全/清理问题、3 个前端中低风险合同/demo 边界问题。修复后前后端完整检查通过，当前没有阻塞 umbrella roadmap 归档的未修复问题。

建议：本审查任务完成并合入后，可以继续归档 `05-17-trust-ops-roadmap`。

## 已修复问题

### Backend high: restore drill 临时 SSH key 写入与清理边界

* 文件：`backend/internal/task/drill.go`
* 问题：restore drill sandbox transfer 使用 heredoc 写入临时 SSH 私钥，且 cleanup 只在写入/chmod 成功后注册。
* 修复：提前注册 best-effort cleanup；用 `printf` + `executor.ShellEscape` 写入私钥；shell-escape rsync source user/host/path 参数。
* 风险降低：避免 heredoc 分隔符被恶意内容截断，避免写入失败路径遗留临时密钥。

### Backend high: sanitizer 未屏蔽完整 PEM private key block

* 文件：`backend/internal/util/sanitize.go`、`backend/internal/util/telegram_test.go`
* 问题：共享 sanitizer 会清理 token/password/url，但未屏蔽完整 PEM private key blocks。
* 修复：增加 PEM private key block redaction；新增 regression test。
* 风险降低：drill evidence、confidence、incident、通知错误等使用 sanitizer 的路径更不容易外泄私钥块。

### Frontend medium: no-token demo onboarding 仍调用写 API

* 文件：`web/src/components/setup-wizard.tsx`、`web/src/components/setup-wizard.test.tsx`
* 问题：demo/no-token 场景关闭 onboarding wizard 会尝试调用 `POST /me/onboarded`。
* 修复：无 token 时直接跳过 API 调用；新增测试覆盖 no-token 不调用、authenticated 仍调用。
* 风险降低：demo mode 继续保持 mock-only，不触发真实写接口。

### Frontend medium: Trust Ops numeric mappers 可能产生 NaN

* 文件：`web/src/lib/api/overview-api.ts`、`web/src/lib/api/policies-api.ts`、`web/src/lib/api/task-runs-api.ts`
* 问题：backup confidence、latest drill、task-run drill evidence 部分 numeric fields 直接 `Number(...)` 或透传，非法 wire 值可能进入 state 为 `NaN`。
* 修复：增加 finite-number normalization；invalid optional IDs 变为 `undefined`/`null`，counts/scores/durations fallback 到 `0`。
* 测试：`web/src/lib/api/overview-api.test.ts`、`web/src/lib/api/drill-evidence-api.test.ts` 新增 invalid numeric cases。

### Frontend low: demo fixture/private-key-shaped strings 与 decorative icons

* 文件：`web/src/data/mock.ts`、`web/src/components/setup-wizard.tsx`
* 问题：mock SSH key fixture 使用 private-key-shaped placeholder；setup wizard 多个装饰 icon 缺少 `aria-hidden`。
* 修复：替换为显式 redacted mock placeholder；补齐 `aria-hidden`。

## 审查通过点

* Restore Drill：保留 sandbox 边界与结构化 evidence；本轮补强临时 key 清理与写入安全。
* Backup Confidence：保持 read-only aggregation、reasons/evidence/next steps；本轮补强数字 fallback。
* SSH Fleet Doctor：后端仍不接受任意 command body，使用 allowlisted checks，API 响应不应暴露敏感字段。
* Health Incident Timeline：保持 read-only aggregation、ownership filtering、safe severity/resource/source fallback。
* Demo/Feedback：demo mode opt-in 且 mock-only；docs 和 templates 没有发现夸大 hosted demo、telemetry、user scale 或 production maturity 的声明。

## 验证证据

* `TMPDIR=/Users/weibo/Code/xirang/.tmp go -C backend test ./...` passed
* `TMPDIR=/Users/weibo/Code/xirang/.tmp npm --prefix web run check` passed
* `git diff --check` passed
* `bash scripts/check-doc-freshness.sh` completed with one non-blocking reminder and no failing exit
