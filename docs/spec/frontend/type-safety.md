# 类型与 API 边界

TypeScript 静态检查不能证明网络数据可信。跨模块领域类型放在 `web/src/types/domain.ts`；raw 请求/响应类型通常私有于 `web/src/lib/api/*.ts`。当前未引入 Zod 等运行时 schema 库，API mapper 负责防御性验证。

## 类型与映射

- 导出的 API 方法、Context 值及 Hook 使用明确返回类型；本地组件类型就近声明；纯类型依赖使用 `import type`。
- 有限状态使用字符串联合或可辨识联合，禁止把任意 wire 字符串直接断言为合法状态。
- API 方法通过 `create*Api()` 工厂暴露类型化操作，正常 JSON 请求统一走 `core.ts` 的 `request<T>()`。
- 请求输入使用 camelCase，API 包装在发送前序列化准确的 snake_case 键。返回值在映射后才交给 Context 与组件，不导出 raw DTO 供组件使用。
- 未知集合先用 `Array.isArray` 检查；普通可选集合按合同降级为 `[]`。闭合产品若要求原子拒绝，则不能用空数组或字段默认值修复矛盾。
- 数值使用 `number-utils.ts` 的 `finiteNumber`、`positiveNumberOrUndefined`、`nullableFiniteNumber`；不可让 `NaN` 或 `Infinity` 进入状态。有上下界/舍入规则的表单解析留在本地。
- 未知状态降级为已有非成功、非授权状态。耦合字段必须整体验证，尤其发布摘要、Catalog、搜索和内容票据；具体闭合组合见领域主文。
- 可选时间、密钥和配置字段保留缺省语义，不生成 `Invalid Date`、虚构凭据或把敏感写入参数复制回记录模型。
- 路由敏感字符串显式验证，例如 `normalizeRedirectTarget`；存储遵守[状态管理](state-management.md#浏览器存储与认证所有权)。
- 禁止使用 `any`、`as any`、`@ts-ignore`、`@ts-expect-error` 或宽泛的不安全类型断言；不能靠屏蔽诊断代替修正类型。不得用 `unknown as T` 绕过可执行的 mapper，也不在组件内为 raw snake_case 做兼容读取。仅允许职责明确且有说明的窄断言边界。

## 响应信封与限流

服务端标准信封为 `{ code: <HTTP 状态码>, message: string, data: T }`；错误信封为 `{ code: number, message: string, data?: unknown }`，生产者要求见[后端错误处理](../backend/error-handling.md#响应与错误归属)。普通 JSON 包装不可绕过中心请求边界。

客户端额外接受 HTTP 成功且 `code=0` 的兼容响应；这是消费者兼容行为，不授权新 handler 返回 `code=0`。

| 响应 | 客户端消费者行为 |
|---|---|
| HTTP 成功，`code=0` | 返回 `data` |
| HTTP 成功，`code` 等于 HTTP 状态码（如 201） | 返回 `data` |
| HTTP 成功，其他非零 `code` | 抛出 `ApiError` |
| HTTP 错误且有合法信封 | 使用后端消息抛出 `ApiError` |
| HTTP 错误且非信封 | 使用本地化通用请求失败消息 |

`ApiError` 携带 `status`、`message`、`detail` 和可选 `retryAfter`。重试秒数优先取有效正数 `Retry-After` 响应头，其次取信封 `data.retry_after`；当前实现按秒数解析。分页使用 `PaginatedEnvelope<T>` 和 `unwrapPaginated`。

中心请求回归至少包含 HTTP 201/`code=201` 成功、HTTP 200/`code=400` 错误、429 响应头和 body 两种 retryAfter 来源及无效 header 回退。mock `Response` 必须包含 `headers.get()`。

## 跨模块映射回归

修改 credentials、app-credentials、node-metrics、silences、auth 等边界时：

- 每个受影响 mapper 测完整响应、缺失数组/可选值、无效数字和未知枚举。
- 写操作验证 camelCase 输入生成准确 snake_case body，不向记录模型合成秘密。
- 组件夹具只用 camelCase；请求失败或取消后不得提交部分对象或旧对象。
- 数值时间戳、预测值、ID 和指标都经过有限数校验；silence 的 tag/category/node 条件映射后再格式化。
- CAPTCHA 独立开关和提交字段，以及凭据原始字段边界见[凭据与访问](../domains/credentials-access.md)。

## 领域规则入口

| 修改主题 | 唯一主文 |
|---|---|
| Doctor | [告警与健康：SSH Fleet Doctor](../domains/alerting-health.md#ssh-fleet-doctor) |
| 临时凭据授权、SSH key scope、安全风险摘要与 CAPTCHA | [凭据与访问](../domains/credentials-access.md) |
| 演练摘要、完整 task-run evidence 与 trigger type | [任务执行与恢复](../domains/task-execution-recovery.md) |
| 备份可信度、健康事件时间线、告警与指标 | [告警与健康](../domains/alerting-health.md) |
| Rsync CAS 字符串、Rclone mode/profile/KMS 闭合摘要 | [备份仓库与发布](../domains/backup-repository.md) |
| 目录 current/parent/breadcrumb、分页拼接的原子边界 | [Catalog](../domains/backup-catalog.md) |
| AST、覆盖率、命中、overlay、保存搜索与证明 | [搜索](../domains/backup-search.md) |
| safePreviewV1、内容票据及传输配置 | [内容交付](../domains/backup-content-delivery.md) |
| GA readiness、inventory、ack 的闭合 DTO | [启用条件](../domains/backup-enablement.md) |

变更后执行[质量回归](quality-guidelines.md)；具体命令和交付门禁见[贡献指南](../../../CONTRIBUTING.md)。

返回[前端入口](README.md)。
