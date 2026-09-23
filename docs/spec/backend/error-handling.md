# 后端错误处理

## 响应与错误归属

普通 API 使用 `backend/internal/api/handlers/response.go` 的统一封装，`code` 与 HTTP 状态一致，成功不是 `code: 0`：

```json
{"code":200,"message":"ok","data":{}}
```

Handler 使用 `respondOK`、`respondCreated`、`respondAccepted`、`respondMessage`、`respondPaginated` 及对应错误辅助函数，不临时构造 `c.JSON` map。需要新状态时先扩展命名辅助函数和响应测试。分页响应另含 `total`、`page`、`page_size`。

客户端额外容忍 `code=0` 不改变服务端标准，也不授权新 handler 返回 0；参见[客户端兼容行为](../frontend/type-safety.md#响应信封与限流)。

领域服务返回错误，由 Handler 决定 HTTP 映射。需要稳定分类时定义 sentinel，通过 `errors.Is` / `errors.As` 判断；添加上下文用 `%w`，多个失败用 `errors.Join` 保留原始身份。数据库未找到用 `errors.Is(err, gorm.ErrRecordNotFound)` 识别。可向用户解释的校验错误才映射为安全的 400 消息。

常见流程是 `parseID` → `ShouldBindJSON` → 领域校验 → 服务/查询 → 错误映射 → 响应。检查数据库的 `.Error`，条件写入还要检查 `RowsAffected`；不能把零行更新当成已成功执行状态转换。

| 情形 | 辅助函数 / HTTP 状态 |
| --- | --- |
| 无效输入 | `respondBadRequest` / 400 |
| 未认证 | `respondUnauthorized` / 401 |
| 角色或所有权不允许 | `respondForbidden` / 403 |
| 资源不存在 | `respondNotFound` / 404 |
| 重复或状态冲突 | `respondConflict` / 409 |
| 已退役资源 | `respondGone` / 410（仅既定退役合同） |
| 安全可公开的上游失败 | `respondBadGateway` / 502 |
| 暂时不可用 | `respondServiceUnavailable` / 503 |
| 当前后端不具备能力 | `respondNotImplemented` / 501 |
| 意外内部错误 | `respondInternalError` / 500 |

`respondInternalError` 在服务端记模块 `api` 与路由模板，客户端只得到通用消息。不得将 `err.Error()`、SQL、加密错误、SSH 密钥、token、命令输出、SFTP/文件内容、Docker 输出、诊断原文、配置导出或堆栈返回客户端。秘密相关路径还必须使用所属领域的安全错误类型，避免原始依赖错误进入通用日志。

缺失或未知认证角色不得按 admin 处理。请求取消和超时不自动代表业务失败；数据库日志适配器保留对 `context.Canceled` 和 `context.DeadlineExceeded` 的降噪行为，不让客户端主动中止查询产生误导的服务器错误。

## 普通 API 中间件拒绝

认证、角色、所有权的 JSON 拒绝使用中间件自己的 `respondAPIError` / `apiResponse`，不得反向导入 handlers。`code` 对齐 HTTP 状态，`message` 为安全消息，`data` 明确为 `null`。拒绝必须中止后续 Handler；审计上下文校验仍保持原有 `c.Next()` 后的时机。

缺失/无效会话为 401，角色/所有权拒绝为 403，非法资源 ID 为 400，所有权数据库或审计上下文失败为安全 500，认证依赖不可用为 503。不要统一改写 metrics、CORS、内容流、WebSocket 升级等不同协议的响应。

回归应经过真实中间件，验证响应封装、null data、中止行为及协议例外；前端保留 401 会话清理，并显示安全的 400/403/500/503 消息。证据入口为 middleware `response_test.go` 与前端 API `client.test.ts`。

## 限流

登录、API、metrics 等限流返回 429 的统一封装：

```json
{"code":429,"message":"请求过于频繁，请稍后再试","data":{"retry_after":12}}
```

有重置时间时，`Retry-After` 响应头与 `data.retry_after` 必须表示相同的正整数秒；零或负计算值钳制为 1 秒。允许的请求不设置重试头并继续执行。消息可以本地化，但不能包含 IP、用户名、token、敏感路径或限流器内部状态。

前端只经中央 `request()` / `ApiError.retryAfter` 读取 wire 字段。测试覆盖 HTTP 状态、完整封装、响应头、正值边界、metrics 一致性，以及前端头优先和 body 回退解析。

## 领域错误

凭据、Doctor、登录挑战、任务恢复、健康查询及备份资产的具体错误码、授权掩蔽、补偿和状态要求统一见[领域合同](../domains/README.md)。通用响应约定不能放宽领域的闭合错误码、无泄漏和失败关闭要求。

二次验证与临时凭据授权挑战的机器协议统一见[按操作绑定的 step-up](../domains/credentials-access.md#按操作绑定的-step-up)，不从普通 403 推断挑战类型。
