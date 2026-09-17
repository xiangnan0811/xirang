# 设计：全仓审计修复集成

> 分支：`fix/07-11-audit-p1-security`
> 原则：授权 fail-closed、secret 只在边界解密、wire DTO 只停留在 API 层、异步失败不伪装成业务空态。

## 1. 所有权边界

### 查询路径

```text
JWT role/user_id
  -> ownershipNodeFilter
  -> explicit requested node validation
  -> owned node query predicate
  -> sanitized response DTO
```

- admin/viewer 维持既有全局读取语义；operator 必须得到非空或空的显式 owned 集合。
- operator 的空过滤不是“无限制”，而是“使用全部 owned IDs”；owned 为空时返回空数据。
- 任一显式未授权 node ID 使整个请求返回 403，不做部分成功。
- DB/role 解析失败返回标准错误，不能降级成空过滤或全局查询。

### Restore drill 双端授权

```text
policy
  -> sandbox node must be owned
  -> source backup task must belong to an owned node
  -> drill execution
  -> evidence read requires owned source AND owned sandbox
```

共享 policy 可以关联多个节点。operator 触发演练时，manager 接收 handler 算出的 allowed source IDs；`nil` 仅表示 admin/cron 不限制，空 slice 表示 operator 无授权源。证据读取用同一双端条件，避免通过 `task_run_id` 或 summary 泄漏另一端资源。

## 2. CAPTCHA 状态矩阵

两个设置 `login.captcha_enabled` 与 `login.second_captcha_enabled` 独立解析：

| primary | second | `/auth/captcha` | login payload / validation |
|---|---|---|---|
| off | off | 只返回 flags | 不提交 challenge |
| on | off | primary challenge | 提交/校验 primary |
| off | on | second challenge | 提交/校验 second |
| on | on | 两个独立 challenge | 两者均提交/校验 |

- challenge 保存在同一个一次性 store，但 ID 独立；验证成功或失败后遵循 store 的消费语义。
- legacy `captcha` / `second_captcha` 自由文本不再被当作安全证明。
- 启用通道但 store 不可用时登录 fail-closed；生成端点使用 login rate limiter。
- 前端通过 auth API mapper 得到可选 challenge，刷新与失败后清理相应答案。

## 3. Secret 生命周期

```text
admin request plaintext
  -> validation/authorization
  -> model BeforeSave encryption
  -> enc:v2 database value
  -> model AfterFind decryption
  -> admin response plaintext OR non-admin redaction
```

- policy hooks 与三类 drill verify script 共用 model hook 边界。
- 启动迁移同时覆盖 `enc:v1:` 与历史 plaintext；迁移失败必须终止启动。
- 非 admin 更新不得清空已有 secret 字段，也不得用非空值注入脚本。
- 日志、错误 envelope、导出和审计数据不得包含 decrypted values。

## 4. 前端 DTO 与异步状态

```text
snake_case response
  -> private Raw* type
  -> defensive map* (arrays/numbers/status/dates)
  -> camelCase domain model
  -> hook state
  -> component loading | data | empty | error
```

- request payload 只在 API wrapper 内转回 snake_case。
- invalid numeric data 不得进入 state 为 `NaN`；unknown enum 降级到非成功/非授权值。
- AbortSignal 与 request identity 一起阻止旧请求覆盖新 node/token/window 的 state。
- last known data 可在后台刷新时保留，但首次加载、无样本、offline、error 必须保持不同语义。

## 5. 数据库与部署

- traffic 查询所需索引以 `000061` paired migration 同时维护 SQLite/PostgreSQL。
- 生产环境显式校验 JWT、DEK、metrics token；Gin debug 不改变 `APP_ENV` 安全语义。
- Gin 默认不信任 proxy headers；仅 `TRUSTED_PROXIES` 中的合法 CIDR/IP 可影响 `ClientIP`。
- Swagger production 默认关闭；CSP connect source 只通过显式配置扩展。

## 6. 回滚与提交边界

- 按 runtime/security、backend ownership/query、frontend boundary/state、Trellis/spec 四批提交。
- 每批优先整文件暂存；共享依赖必须与消费者同批，避免产生不能编译的中间提交。
- Trellis archive 与 journal 使用各自自动提交，排在全部工作提交之后。
- 不 amend、不 push、不创建 PR；父任务与 P3 保持 active。
