# 后端日志合同

## 输出与级别

主日志使用 `zerolog`，由 `backend/internal/logger/logger.go` 初始化。新增调用使用 `logger.Module("name")`，每条结构化事件带稳定 `module`。历史 `log.Printf` 不是新代码范式；只有严格局部修改且未迁移周边代码时才保留原调用。

| 级别 | 使用场景 |
| --- | --- |
| Debug | 默认关闭的高频诊断，例如异常检测细节 |
| Info | 启停、初始化、保留清理等生命周期和汇总 |
| Warn | 可恢复失败、跳过、降级、可重试投递、队列饱和及值得观察的拒绝 |
| Error | 阻止请求或后台工作完成的意外失败 |
| Fatal | 进程不能安全运行的启动失败 |

使用类型化字段 `Uint`、`Int`、`Str`、`Err`、`Time` 等，不先拼接错误字符串。消息简短，说明具体动作；消息正文优先简体中文，模块名、字段名使用稳定英文标识符。时间戳采用含时区的 RFC3339。

默认写 stdout；配置 `LOG_FILE` 时追加写文件并继续写 stdout。文件打开失败不阻止启动，回退 stdout 并记录错误。应用进程持有打开的文件句柄，没有内置轮转或自动重新打开功能；容器与持久化日志要求见[部署运行时](deployment-runtime.md)。

## 字段与敏感数据

普通 HTTP 访问日志由 `middleware/structured_logger.go` 记录 method、path、status、latency_ms、client_ip，以及可选 request_id/user_id。任务、节点等普通领域事件使用 task_id、task_run_id、node_id、alert_id、integration_id、policy_id、worker 关联。

内容形状请求属于明确隐私例外，不能套普通 identity/path 字段；固定安全路由、内容 recovery、Nginx 独立脱敏、闭合低基数指标及有界清理要求统一见[内容交付领域合同](../domains/backup-content-delivery.md)。

记录启动/关闭、后台失败、可恢复跳过、禁用 SSH host key 校验等安全警告、路径拒绝、内部错误、队列溢出及外部投递结果。外部投递只带渠道标识，不带通知端点秘密。

以下内容不得写入全局日志：

- 密码、私钥、TOTP、JWT、恢复码、数据加密密钥、SMTP 密码、webhook secret、bearer token、完整秘密端点。
- 模型 hooks 或 `secure.DecryptIfNeeded` 返回的解密值、解密设置和配置导出载荷。
- SFTP/文件内容、终端流、完整命令及可能含秘密的输出、Docker 输出和卷名、Doctor/迁移预检原始证据、executor config、凭据审计原始 metadata。

诊断输出应遵循领域已有的限长和脱敏存储，不把任务日志全部复制到进程日志。意外失败必须返回调用者或以足够的安全结构化字段记录，不可静默吞错。

## 用户可见文本的共享脱敏

用户可见的运行证据、通知载荷、投递错误、演练输出和事件消息，只要可能含有命令输出或秘密，都必须在持久化、响应或外发边界使用共享 `util.SanitizeMessage`；错误对象可经 `util.SanitizeError`。不能仅对 `key=value` token 做局部替换：必须覆盖多行 PEM 私钥整个 BEGIN/END 块；若有界输出截断了 END 标记，则从 BEGIN 到文本末尾均按私钥处理。还须脱敏 URL 凭据、路径/query 中的 token 和其他秘密模式。共享实现位于 `backend/internal/util/sanitize.go`，先移除私钥块再处理其他模式，最后限制长度。

这条要求不授权输出原本禁止返回的命令正文、内容或领域私有字段。领域已有更严格的闭合错误、只返回摘要和读取历史数据时再次脱敏等规则继续有效。新增文本出口复用共享入口，不能复制出逐渐分歧的 sanitizer。

回归必须包含完整多行假 PEM，断言 BEGIN/END 及中间秘密全部缺席，并验证 URL/token 脱敏与长度边界。现有入口为 `internal/util/telegram_test.go` 中 `TestSanitizeMessage_RedactsPrivateKeyBlocks`、URL 与截断测试；涉及通知、演练或 API 出口时同时验证实际出口，不能只测辅助函数。

## WebSocket Hub 队列过载

`ws.Hub.Publish` 对广播队列保持非阻塞。每次丢弃都原子递增累计计数；每个 Hub 独立维护下一次告警时刻，通过 CAS 保证首次溢出可告警，其后每 30 秒最多一次。输出模块 `ws`、Warn 与累计 `dropped_total`，不包含 event payload。

握手读取诊断使用结构化 Debug，不能打印原始客户端错误。该规则不改变协议、连接上限、计数重置或配置。

测试使用 `publishAt(event, now)`、饱和队列和并发发布，覆盖无丢弃、首次溢出、间隔内抑制、边界并发竞争、精确累计计数及 payload 缺席；不靠 sleep 猜测时间。验证 Debug 过滤和模块/级别/字段，入口为 `internal/ws/hub_logging_test.go`。
